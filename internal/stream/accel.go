// Package stream 提供远端直链的并发 Range 分块下载（多线程加速）与代理响应组装。
// 纯标准库实现，不依赖项目内其他包，便于独立单测。
package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LinkProvider 返回当前可用的直链（URL + 请求头，如 PikPak 必须绑定的 UA）。
// 首个分块前惰性调用一次并缓存；分块遇过期状态码（401/403/404/410）会强制重取。
type LinkProvider func(ctx context.Context) (url string, header http.Header, err error)

// 可调常量（编译期固定，够用即可，不进配置）。
const (
	chunkAttempts = 4                      // 每块硬错误最多尝试次数
	retryBackoff  = 200 * time.Millisecond // 硬错误重试间隔基数（×attempt）

	// 挂住判定取激进值。读端严格按序等块，一块挂住就把整条流和它后面所有块一起堵死
	// （后面的块下好了也只能在内存里排队），所以"早发现"的收益远大于"多重发一次"的成本。
	// 重试只是重发一个 Range 请求；而发现得晚，播放器会先把整个响应判死。
	// 限流等待不受这两道闸约束——那条路走 m.pause 与 throttleBudget，见下。
	headerTimeout = 5 * time.Second // 响应头多久不来即认定挂住
	stallTimeout  = 6 * time.Second // 响应体连续多久没有新字节即认定挂住

	// 整块总时限按块大小折算，不再一刀切：慢链路上一个 64MB 的块本就需要时间，
	// 而一个 512KB 的首块拖到十几秒就已经不正常了。
	chunkBase    = 12 * time.Second // 固定开销（建连、TLS、寻址、服务端寻块）
	chunkMinRate = 256 << 10        // 折算用的保底速率（字节/秒）

	// 云盘按请求频率限流（OneDrive/SharePoint 429/503 + Retry-After 常达数十秒）。
	// 限流等待不消耗尝试次数，只受累计预算约束——否则一个限流窗口就掐死整条流，
	// 播放器重试又加剧限流，表现为"一直重连播放不出来"。
	throttleBudget  = 2 * time.Minute  // 单块累计限流等待预算
	throttleDefault = 2 * time.Second  // 无 Retry-After 头时的等待
	throttleMax     = 30 * time.Second // 单次等待上限（防恶意/异常头长挂）
)

// chunkDeadline 单块请求的总时限：固定开销 + 按保底速率折算的传输时间。
func chunkDeadline(size int64) time.Duration {
	return chunkBase + time.Duration(size)*time.Second/chunkMinRate
}

// chunkClient 分块专用客户端，只为加上响应头超时——标准库默认不限，
// 上游一挂起就只能等总时限耗尽。
//
// 刻意只改这一项：HTTP/2 协商、连接复用、连接池大小全部保持标准库默认。
// 那几项是另一码事，不在本次范围内。
var chunkClient = &http.Client{Transport: func() http.RoundTripper {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	t2 := t.Clone()
	t2.ResponseHeaderTimeout = headerTimeout
	return t2
}()}

// stallReader 监视"响应体连续多久没有新字节"：每读到数据就重置计时，到点即取消请求，
// 让上层落进既有的换链/退避重试。上游把连接挂住而不断开是跨国链路上的常客，
// 只看总时限的话要等到时限耗尽才发现，中间全是白等。
type stallReader struct {
	r     io.Reader
	d     time.Duration
	timer *time.Timer
	fired atomic.Bool
}

func newStallReader(r io.Reader, d time.Duration, cancel context.CancelFunc) *stallReader {
	s := &stallReader{r: r, d: d}
	s.timer = time.AfterFunc(d, func() {
		s.fired.Store(true)
		cancel()
	})
	return s
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.timer.Reset(s.d)
	}
	// 取消导致的读失败要报成"停滞"，否则日志里只剩一句 context canceled，
	// 与客户端主动断开、整条流退出混在一起分不出来。
	if err != nil && s.fired.Load() {
		return n, fmt.Errorf("响应体停滞超过 %s", s.d)
	}
	return n, err
}

func (s *stallReader) stop() { s.timer.Stop() }

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

// MultiReader 并发取 Range 分块、滑动窗口按序输出的 io.ReadCloser。
type MultiReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	client *http.Client

	provider LinkProvider
	offset   int64
	length   int64
	plan     chunkPlan
	chunks   int
	window   int

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
// threads 个 worker 并发取块，窗口=threads 限制内存 ≈ (threads+1)×chunkBytes。
// 返回的 Reader 非并发安全（单读者）；Close 幂等，取消所有在途请求并等 worker 退出。
func NewMultiReader(ctx context.Context, provider LinkProvider, offset, length int64, threads int, chunkBytes int64) io.ReadCloser {
	if threads < 1 {
		threads = 1
	}
	if chunkBytes < 64*1024 {
		chunkBytes = 64 * 1024
	}
	cctx, cancel := context.WithCancel(ctx)
	plan := newChunkPlan(length, chunkBytes)
	chunks := plan.n
	m := &MultiReader{
		ctx:      cctx,
		cancel:   cancel,
		client:   chunkClient,
		provider: provider,
		offset:   offset,
		length:   length,
		plan:     plan,
		chunks:   chunks,
		window:   threads,
		results:  map[int]chunkResult{},
	}
	m.cond = sync.NewCond(&m.mu)
	if length <= 0 {
		m.readErr = io.EOF
		return m
	}
	if threads > chunks {
		threads = chunks
	}
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

// chunkRange 第 idx 块在源文件中的 [start, end]（end 含）。
func (m *MultiReader) chunkRange(idx int) (start, end int64) {
	rel, size := m.plan.at(idx)
	start = m.offset + rel
	return start, start + size - 1
}

// firstChunk 递增分块的起点大小。读端必须等一整块下满才能吐出第一个字节（见 Read），
// 所以起播与每次拖动都要先付一整块的等待——4MB 的块在跨国链路上就是好几秒白屏。
const firstChunk = 512 << 10

// chunkPlan 把 [0, length) 切成块：前几块从 firstChunk 逐块翻倍到 chunk，其余等长。
// 首帧只等 512KB，稳态回到整块（大块才摊薄请求数与跨国握手开销）。
// chunk <= firstChunk 时递增段为空，退化成均匀分块——小分块的配置行为一字不变。
type chunkPlan struct {
	ramp   []int64 // 递增段各块大小（512K、1M、2M…，均 < chunk）
	prefix []int64 // ramp 的前缀和，len = len(ramp)+1，prefix[0] = 0
	chunk  int64
	length int64
	n      int // 总块数
}

func newChunkPlan(length, chunk int64) chunkPlan {
	p := chunkPlan{chunk: chunk, length: length, prefix: []int64{0}}
	for sz := int64(firstChunk); sz < chunk; sz *= 2 {
		p.ramp = append(p.ramp, sz)
		p.prefix = append(p.prefix, p.prefix[len(p.prefix)-1]+sz)
	}
	// length 落在递增段内就到此为止，不必再补整块
	for i := 1; i < len(p.prefix); i++ {
		if p.prefix[i] >= length {
			p.n = i
			return p
		}
	}
	rest := length - p.prefix[len(p.prefix)-1]
	p.n = len(p.ramp) + int((rest+chunk-1)/chunk)
	return p
}

// at 第 idx 块相对区间起点的偏移与大小（末块按 length 截断）。
// idx 恒小于 n，故返回的 size 必 > 0。
func (p chunkPlan) at(idx int) (start, size int64) {
	if idx < len(p.ramp) {
		start, size = p.prefix[idx], p.ramp[idx]
	} else {
		start = p.prefix[len(p.prefix)-1] + int64(idx-len(p.ramp))*p.chunk
		size = p.chunk
	}
	if start >= p.length {
		return p.length, 0
	}
	if start+size > p.length {
		size = p.length - start
	}
	return start, size
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
		buf, retryRefresh, wait, err := m.doRange(url, hdr, start, end, size, idx)
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
func (m *MultiReader) doRange(url string, hdr http.Header, start, end, size int64, idx int) ([]byte, bool, time.Duration, error) {
	rctx, cancel := context.WithTimeout(m.ctx, chunkDeadline(size))
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
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
		return nil, false, 0, err
	}
	defer resp.Body.Close()
	// 响应体停滞监视只覆盖真正读取的那两个分支；非 2xx 分支不读体，无需监视。
	switch resp.StatusCode {
	case http.StatusPartialContent:
		body := newStallReader(resp.Body, stallTimeout, cancel)
		defer body.stop()
		buf := make([]byte, size)
		if _, err := io.ReadFull(body, buf); err != nil {
			return nil, false, 0, fmt.Errorf("分块读取中断: %w", err)
		}
		return buf, false, 0, nil
	case http.StatusOK:
		// 服务器不认 Range：仅当整个请求区间就是文件开头的唯一一块时可接受
		if idx == 0 && m.chunks == 1 && m.offset == 0 {
			body := newStallReader(resp.Body, stallTimeout, cancel)
			defer body.stop()
			buf := make([]byte, size)
			if _, err := io.ReadFull(body, buf); err != nil {
				return nil, false, 0, fmt.Errorf("读取源失败: %w", err)
			}
			return buf, false, 0, nil
		}
		return nil, false, 0, errNoRange
	}
	switch classifyErrStatus(resp.StatusCode) {
	case dispRelink:
		return nil, true, 0, fmt.Errorf("直链疑似过期: HTTP %d", resp.StatusCode)
	case dispThrottle:
		return nil, false, retryAfter(resp.Header.Get("Retry-After")),
			fmt.Errorf("源限流: HTTP %d", resp.StatusCode)
	default:
		return nil, false, 0, fmt.Errorf("拉取分块失败: HTTP %d", resp.StatusCode)
	}
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
	return nil
}
