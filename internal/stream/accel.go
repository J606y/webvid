// Package stream 提供远端直链的并发 Range 分块下载（多线程加速）与代理响应组装。
// 纯标准库实现，不依赖项目内其他包，便于独立单测。
package stream

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LinkProvider 返回当前可用的直链（URL + 请求头，如 PikPak 必须绑定的 UA）。
// 首个分块前惰性调用一次并缓存；分块遇过期状态码（401/403/404/410）会强制重取。
type LinkProvider func(ctx context.Context) (url string, header http.Header, err error)

// 可调常量（编译期固定，够用即可，不进配置）。
const (
	chunkAttempts = 4                      // 每块硬错误最多尝试次数
	retryBackoff  = 200 * time.Millisecond // 硬错误重试间隔基数（×attempt）
	chunkTimeout  = 60 * time.Second       // 单块请求总超时（兜底；正常失速由下面两道闸先拦下）

	// headerTimeout 响应头迟迟不来。与下面的 stallTimeout 各管一种卡法：
	// 上游"连响应头都不给"与"发了头却不再发数据"都表现为播放器空转而后台零流量，
	// 任一触发即取消本次请求，落进既有的换链/退避重试路径自愈——缺了这两道闸，
	// 卡住时要一直干等到 chunkTimeout。
	headerTimeout = 15 * time.Second

	// 云盘按请求频率限流（OneDrive/SharePoint 429/503 + Retry-After 常达数十秒）。
	// 限流等待不消耗尝试次数，只受累计预算约束——否则一个限流窗口就掐死整条流，
	// 播放器重试又加剧限流，表现为"一直重连播放不出来"。
	throttleBudget  = 2 * time.Minute  // 单块累计限流等待预算
	throttleDefault = 2 * time.Second  // 无 Retry-After 头时的等待
	throttleMax     = 30 * time.Second // 单次等待上限（防恶意/异常头长挂）

	minChunkBytes = 64 << 10 // 分块大小下限
	drainLimit    = 32 << 10 // 读满后为复用连接而清尾的字节上限（见 readBody）

	// rampBase 首块大小。读端必须等一整块下完才能吐出字节，若一上来就按 ChunkBytes
	// （默认 4MB）取块，起播与每次拖动都得先下满 4MB 才有画面。前几块改为
	// 512K/1M/2M… 逐块翻倍到 ChunkBytes：首字节延迟降一个数量级，稳态分块不变。
	// ChunkBytes ≤ rampBase 时每块都取 ChunkBytes，即退化为均匀分块。
	rampBase = 512 << 10
)

// stallTimeout 响应体连续无字节到达即判失速（见 headerTimeout）。var 以便测试调小。
var stallTimeout = 20 * time.Second

// chunkClient 并发分块拉流专用客户端。
//
// 必须禁用 HTTP/2。Go 的 h2 Transport 会把发往同一 host 的并发请求全部复用到一条 TCP
// 连接上，N 个 worker 共享同一个拥塞窗口——并发分块换不来一个字节的额外带宽，只是把一条
// 连接切成 N 段轮流启停。googleapis.com 经 ALPN 协商 h2，用 http.DefaultClient 实测拿到的
// 就是 HTTP/2.0，这正是"码率一高就一直缓冲、而浏览器直连云盘不卡"的根因。关掉 h2 后每个
// worker 独占一条 TCP 连接、各有各的拥塞窗口，多线程才真正成立（rclone / aria2 的多线程
// 下载同此做法）。改回 DefaultTransport 等于把加速功能悄悄关掉，勿动。
var chunkClient = &http.Client{Transport: newChunkTransport()}

// newChunkTransport 构造分块拉流用的 Transport。独立成函数，测试才能拿一份一模一样的
// 副本去真实验证 h2 确实被拒（见 TestChunkTransportRejectsHTTP2）——这条是整个加速
// 功能的命门，只断言字段取值不够，得对着真开了 h2 的服务器验。
func newChunkTransport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	t := base.Clone()
	t.ForceAttemptHTTP2 = false
	// 非 nil 空表 = 拒绝 ALPN 的 h2 升级（net/http 约定）。只置 ForceAttemptHTTP2 不够。
	t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	// Go 默认每 host 只留 2 条空闲连接，多出来的 worker 每块都要重做 TCP+TLS 握手。
	t.MaxIdleConnsPerHost = 32
	t.MaxIdleConns = 128
	t.IdleConnTimeout = 90 * time.Second
	t.ResponseHeaderTimeout = headerTimeout
	return t
}

// Opts 并发分块拉流的调参，来自挂载配置（见 fs.AccelOpts）。
// 这里只做结构性兜底，取值范围的钳制留在配置解析层——本包是纯机制，
// 「几 MB 到几 MB 算合理」是策略，两者不混在一处。
type Opts struct {
	Threads        int    // 并发连接数
	ChunkBytes     int64  // 稳态分块大小
	ReadaheadBytes int64  // 预读缓冲上限，决定滑动窗口能领先读端多少
	Label          string // 诊断日志标识（文件名），仅 NL_STREAM_DEBUG=1 时使用
}

func (o Opts) normalize() Opts {
	if o.Threads < 1 {
		o.Threads = 1
	}
	if o.ChunkBytes < minChunkBytes {
		o.ChunkBytes = minChunkBytes
	}
	if o.ReadaheadBytes < o.ChunkBytes {
		o.ReadaheadBytes = o.ChunkBytes // 窗口至少一块，否则读端永远等不到数据
	}
	return o
}

// window 滑动窗口容量（块数）：由预读缓冲推导，但不少于线程数——
// 窗口比线程还小会让 worker 领不到活白白空转。
func (o Opts) window() int {
	return max(int(o.ReadaheadBytes/o.ChunkBytes), o.Threads)
}

// rampOffsets 返回渐进段各块相对区间起点的偏移，末项 = 渐进段总长度。
// 块大小 512K→1M→2M… 翻倍直到追平 chunkBytes，之后转入等长分块。
// chunkBytes ≤ rampBase 时返回 [0]（渐进段为空），全程等长。
func rampOffsets(chunkBytes int64) []int64 {
	offs := []int64{0}
	for sz := int64(rampBase); sz < chunkBytes; sz *= 2 {
		offs = append(offs, offs[len(offs)-1]+sz)
	}
	return offs
}

// chunkCount 按排程算出覆盖 length 字节所需的块数（= 起点落在 [0,length) 内的块数）。
func chunkCount(length int64, rampOff []int64, chunkBytes int64) int {
	if length <= 0 {
		return 0
	}
	rampN := len(rampOff) - 1
	for i := 0; i < rampN; i++ {
		if rampOff[i+1] >= length {
			return i + 1 // 第 i 块就吃到了区间末尾
		}
	}
	rem := length - rampOff[rampN]
	if rem <= 0 {
		return rampN
	}
	return rampN + int((rem+chunkBytes-1)/chunkBytes)
}

// retryAfter 解析 Retry-After 秒数，钳制到 [throttleDefault, throttleMax]。
func retryAfter(h string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && n > 0 {
		return min(time.Duration(n)*time.Second, throttleMax)
	}
	return throttleDefault
}

// disposition 是一个非成功上游状态码的处置动作。
type disposition int

const (
	dispRelink   disposition = iota // 401/403/404/410：直链疑似过期 → 换链重试
	dispThrottle                    // 429/503：源限流 → 按 Retry-After 等待（预算内不计次）
	dispHard                        // 其它非 2xx：硬错误 → 退避重试
)

// classifyErrStatus 把一个非 2xx（且非可接受的 200-Range）上游状态码归类到处置动作。
// serve.openUpstream（单流）与 accel.doRange（分块）共用同一判定，避免"哪些码换链、
// 哪些码限流"两处各写一份而漂移——两者仅在拿到处置后的动作（返回体 vs 返回标志）不同。
func classifyErrStatus(code int) disposition {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return dispRelink
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return dispThrottle
	default:
		return dispHard
	}
}

var errNoRange = errors.New("源不支持 Range 分块")

// chunkResult 一个分块的下载结果。
type chunkResult struct {
	buf []byte
	err error
}

// stallReader 失速看门狗：包住响应体，每次 Read 前重置定时器，
// 连续 idle 时长无字节到达即 cancel 整个请求。ResponseHeaderTimeout 只管响应头，
// "头发了、数据不发"这种卡法得靠这层才能秒级发现。
type stallReader struct {
	r     io.Reader
	timer *time.Timer
	idle  time.Duration
}

func newStallReader(r io.Reader, idle time.Duration, cancel context.CancelFunc) *stallReader {
	return &stallReader{r: r, idle: idle, timer: time.AfterFunc(idle, cancel)}
}

func (s *stallReader) Read(p []byte) (int, error) {
	s.timer.Reset(s.idle)
	return s.r.Read(p)
}

func (s *stallReader) stop() { s.timer.Stop() }

// MultiReader 并发取 Range 分块、滑动窗口按序输出的 io.ReadCloser。
type MultiReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	client *http.Client

	provider LinkProvider
	offset   int64
	length   int64
	chunk    int64
	chunks   int
	window   int
	rampOff  []int64 // 渐进段各块相对偏移（末项 = 渐进段总长）
	rampN    int     // 渐进段块数

	dbg *streamStats // nil = 诊断关闭

	// 直链缓存（换链单飞：worker 带着自己用过的 gen 来刷，gen 未变才真调 provider）
	linkMu   sync.Mutex
	linkURL  string
	linkHdr  http.Header
	linkGen  int
	linkInit bool

	mu       sync.Mutex
	cond     *sync.Cond
	results  map[int]chunkResult
	nextJob  int // 下一个待领取的块序号
	nextRead int // 读端正在等待的块序号
	cur      []byte
	readErr  error
	closed   bool
	wg       sync.WaitGroup
}

// NewMultiReader 输出直链内容的 [offset, offset+length) 区间，共 length 字节（必须 >0）。
// o.Threads 个 worker 并发取块，滑动窗口由 o.ReadaheadBytes 决定（见 Opts.window）。
// 返回的 Reader 非并发安全（单读者）；Close 幂等，取消所有在途请求并等 worker 退出。
func NewMultiReader(ctx context.Context, provider LinkProvider, offset, length int64, o Opts) io.ReadCloser {
	o = o.normalize()
	cctx, cancel := context.WithCancel(ctx)
	rampOff := rampOffsets(o.ChunkBytes)
	m := &MultiReader{
		ctx:      cctx,
		cancel:   cancel,
		client:   chunkClient,
		provider: provider,
		offset:   offset,
		length:   length,
		chunk:    o.ChunkBytes,
		chunks:   chunkCount(length, rampOff, o.ChunkBytes),
		window:   o.window(),
		rampOff:  rampOff,
		rampN:    len(rampOff) - 1,
		dbg:      newStreamStats(o, length),
		results:  map[int]chunkResult{},
	}
	m.cond = sync.NewCond(&m.mu)
	if length <= 0 {
		m.readErr = io.EOF
		return m
	}
	threads := min(o.Threads, m.chunks)
	m.wg.Add(threads)
	for i := 0; i < threads; i++ {
		go m.worker()
	}
	// ctx 取消时唤醒所有等待者（Read 阻塞在 Cond 上感知不到 ctx）
	go func() {
		<-cctx.Done()
		m.cond.Broadcast()
	}()
	return m
}

// worker 循环领块→下载→投递，直到块发完或 ctx 取消。
func (m *MultiReader) worker() {
	defer m.wg.Done()
	for {
		m.mu.Lock()
		for m.nextJob < m.chunks && m.nextJob >= m.nextRead+m.window && m.ctx.Err() == nil {
			m.cond.Wait() // 窗口满，等读端消费
		}
		if m.nextJob >= m.chunks || m.ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		idx := m.nextJob
		m.nextJob++
		m.mu.Unlock()

		buf, err := m.fetchChunk(idx)

		m.mu.Lock()
		m.results[idx] = chunkResult{buf: buf, err: err}
		m.cond.Broadcast()
		abort := err != nil
		m.mu.Unlock()
		if abort {
			return // 一块彻底失败即整体失败，无需继续
		}
	}
}

// chunkRange 第 idx 块在源文件中的 [start, end]（end 含）。纯函数，由渐进排程推导。
func (m *MultiReader) chunkRange(idx int) (start, end int64) {
	var rel, size int64
	if idx < m.rampN {
		rel, size = m.rampOff[idx], m.rampOff[idx+1]-m.rampOff[idx]
	} else {
		rel, size = m.rampOff[m.rampN]+int64(idx-m.rampN)*m.chunk, m.chunk
	}
	start = m.offset + rel
	end = start + size - 1
	if last := m.offset + m.length - 1; end > last {
		end = last
	}
	return
}

// getLink 取当前直链；usedGen==当前 gen 且 force 时才真调 provider（换链单飞）。
func (m *MultiReader) getLink(usedGen int, force bool) (string, http.Header, int, error) {
	m.linkMu.Lock()
	defer m.linkMu.Unlock()
	if !m.linkInit || (force && usedGen == m.linkGen) {
		u, h, err := m.provider(m.ctx)
		if err != nil {
			return "", nil, m.linkGen, err
		}
		m.linkURL, m.linkHdr = u, h
		m.linkGen++
		m.linkInit = true
	}
	return m.linkURL, m.linkHdr, m.linkGen, nil
}

// pause ctx 感知的睡眠；返回 false 表示 ctx 已取消。
func (m *MultiReader) pause(d time.Duration) bool {
	select {
	case <-m.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// fetchChunk 下载一个分块：硬错误带退避重试、直链过期换链、限流按 Retry-After 等待。
func (m *MultiReader) fetchChunk(idx int) ([]byte, error) {
	start, end := m.chunkRange(idx)
	size := end - start + 1
	var lastErr error
	gen := 0
	refresh := false
	var throttled time.Duration
	for attempt := 1; attempt <= chunkAttempts; {
		url, hdr, g, err := m.getLink(gen, refresh)
		if err != nil {
			lastErr = fmt.Errorf("获取直链失败: %w", err)
			refresh = false
			attempt++
			if attempt <= chunkAttempts && !m.pause(retryBackoff*time.Duration(attempt-1)) {
				return nil, m.ctx.Err()
			}
			continue
		}
		gen, refresh = g, false
		buf, retryRefresh, wait, err := m.doRange(url, hdr, start, end, size, idx, attempt)
		if err == nil {
			return buf, nil
		}
		lastErr = err
		refresh = retryRefresh
		if m.ctx.Err() != nil {
			return nil, m.ctx.Err()
		}
		if errors.Is(err, errNoRange) {
			return nil, err // 源不支持 Range，重试无意义
		}
		m.dbg.retry()
		if wait > 0 && throttled+wait <= throttleBudget {
			throttled += wait // 限流等待不消耗尝试次数，流照常存活（这段时间无新数据而已）
			if !m.pause(wait) {
				return nil, m.ctx.Err()
			}
			continue
		}
		attempt++
		if attempt <= chunkAttempts && !m.pause(retryBackoff*time.Duration(attempt-1)) {
			return nil, m.ctx.Err()
		}
	}
	err := fmt.Errorf("分块 %d [%d-%d] 下载失败（已重试）: %w", idx, start, end, lastErr)
	log.Printf("[stream] %v", err)
	return nil, err
}

// doRange 发一次 Range 请求读满分块；返回 (数据, 是否应换链重试, 限流等待时长, 错误)。
func (m *MultiReader) doRange(url string, hdr http.Header, start, end, size int64, idx, attempt int) ([]byte, bool, time.Duration, error) {
	rctx, cancel := context.WithTimeout(m.ctx, chunkTimeout)
	defer cancel()
	tctx, tr := m.dbg.begin(rctx, idx, start, end, attempt)
	req, err := http.NewRequestWithContext(tctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, 0, err
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := m.client.Do(req)
	if err != nil {
		tr.fail(err)
		return nil, false, 0, err
	}
	defer resp.Body.Close()
	tr.gotHeader(resp.StatusCode)

	switch resp.StatusCode {
	case http.StatusPartialContent:
		buf, err := m.readBody(resp.Body, size, cancel)
		if err != nil {
			tr.fail(err)
			return nil, false, 0, fmt.Errorf("分块读取中断: %w", err)
		}
		tr.done(size)
		return buf, false, 0, nil
	case http.StatusOK:
		// 服务器不认 Range：仅当整个请求区间就是文件开头的唯一一块时可接受
		if idx == 0 && m.chunks == 1 && m.offset == 0 {
			buf, err := m.readBody(resp.Body, size, cancel)
			if err != nil {
				tr.fail(err)
				return nil, false, 0, fmt.Errorf("读取源失败: %w", err)
			}
			tr.done(size)
			return buf, false, 0, nil
		}
		tr.fail(errNoRange)
		return nil, false, 0, errNoRange
	}
	switch classifyErrStatus(resp.StatusCode) {
	case dispRelink:
		err := fmt.Errorf("直链疑似过期: HTTP %d", resp.StatusCode)
		tr.fail(err)
		return nil, true, 0, err
	case dispThrottle:
		err := fmt.Errorf("源限流: HTTP %d", resp.StatusCode)
		tr.fail(err)
		return nil, false, retryAfter(resp.Header.Get("Retry-After")), err
	default:
		err := fmt.Errorf("拉取分块失败: HTTP %d", resp.StatusCode)
		tr.fail(err)
		return nil, false, 0, err
	}
}

// readBody 读满 size 字节，全程挂失速看门狗。
func (m *MultiReader) readBody(body io.Reader, size int64, cancel context.CancelFunc) ([]byte, error) {
	sr := newStallReader(body, stallTimeout, cancel)
	defer sr.stop()
	buf := make([]byte, size)
	if _, err := io.ReadFull(sr, buf); err != nil {
		return nil, err
	}
	// 读满还不够，得把尾巴（分块编码的结束块等）读到 EOF，net/http 才会把连接放回空闲池。
	// 只读完就 Close 的话收尾是异步的，赶不上下一块的请求，实测每块都在重新握手——
	// 跨国链路上一次 TCP+TLS 往返就是几百毫秒，分块越多亏得越狠。
	// 残余超过 drainLimit 说明源发得比要的多，不奉陪，直接关掉这条连接。
	io.CopyN(io.Discard, sr, drainLimit)
	return buf, nil
}

// Read 按序输出分块内容；某块彻底失败后恒返回该错误。
func (m *MultiReader) Read(p []byte) (int, error) {
	if len(m.cur) == 0 {
		m.mu.Lock()
		for {
			if m.readErr != nil {
				m.mu.Unlock()
				return 0, m.readErr
			}
			if err := m.ctx.Err(); err != nil {
				m.mu.Unlock()
				return 0, err
			}
			if m.nextRead >= m.chunks {
				m.readErr = io.EOF
				m.mu.Unlock()
				return 0, io.EOF
			}
			if r, ok := m.results[m.nextRead]; ok {
				delete(m.results, m.nextRead)
				if r.err != nil {
					m.readErr = r.err
					m.mu.Unlock()
					return 0, r.err
				}
				m.cur = r.buf
				m.nextRead++
				m.cond.Broadcast() // 窗口前移，放行 worker
				break
			}
			m.cond.Wait()
		}
		m.mu.Unlock()
	}
	n := copy(p, m.cur)
	m.cur = m.cur[n:]
	return n, nil
}

// Close 幂等：取消在途请求、唤醒阻塞的 Read、等全部 worker 退出。
func (m *MultiReader) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.cond.Broadcast()
	m.wg.Wait()
	m.dbg.summary()
	return nil
}
