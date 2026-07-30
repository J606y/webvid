package stream

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// pattern 生成确定性测试内容。
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + 7)
	}
	return b
}

// rangeSrv 支持 Range 的 mock 源，可注入延迟/断流/403 等行为。
type rangeSrv struct {
	content []byte
	noRange bool // 无视 Range 恒 200 全量

	mu     sync.Mutex
	starts []int64        // 每个请求的 Range 起点（记录顺序）
	tries  map[int64]int  // start → 第几次请求
	// hook 在写响应前调用；返回 true 表示 hook 已接管本次响应
	hook func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool
}

func (s *rangeSrv) maxStart() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var m int64 = -1
	for _, v := range s.starts {
		if v > m {
			m = v
		}
	}
	return m
}

func (s *rangeSrv) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.noRange {
			w.WriteHeader(http.StatusOK)
			w.Write(s.content)
			return
		}
		rh := r.Header.Get("Range")
		var start, end int64
		if _, err := fmt.Sscanf(rh, "bytes=%d-%d", &start, &end); err != nil {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		if end > int64(len(s.content)-1) {
			end = int64(len(s.content) - 1)
		}
		s.mu.Lock()
		if s.tries == nil {
			s.tries = map[int64]int{}
		}
		s.tries[start]++
		try := s.tries[start]
		s.starts = append(s.starts, start)
		hook := s.hook
		s.mu.Unlock()
		if hook != nil && hook(w, r, start, end, try) {
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(s.content)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(s.content[start : end+1])
	})
}

func fixedProvider(url string) LinkProvider {
	return func(ctx context.Context) (string, http.Header, error) { return url, nil, nil }
}

// tOpts 测试用调参：预读缓冲取 threads×chunk，使滑动窗口恰为 threads——
// 即本包早期"窗口=线程数"的行为，现存用例的断言得以原样成立。
// 预读缓冲放大窗口的那一半契约由 TestReadaheadWidensWindow 单独覆盖。
func tOpts(threads int, chunk int64) Opts {
	return Opts{Threads: threads, ChunkBytes: chunk, ReadaheadBytes: chunk * int64(threads)}
}

func readAll(t *testing.T, r io.ReadCloser) []byte {
	t.Helper()
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return b
}

func TestOrderedOutputFull(t *testing.T) {
	content := pattern(1<<20 + 12345) // 非整块边界
	srv := &rangeSrv{content: content}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), tOpts(4, 128<<10))
	got := readAll(t, mr)
	if !bytes.Equal(got, content) {
		t.Fatalf("全量内容不一致: len=%d want %d", len(got), len(content))
	}
}

func TestOrderedOutputOffsetLength(t *testing.T) {
	content := pattern(1 << 20)
	srv := &rangeSrv{content: content}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	off, ln := int64(100_000), int64(500_007)
	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), off, ln, tOpts(3, 64<<10))
	got := readAll(t, mr)
	if !bytes.Equal(got, content[off:off+ln]) {
		t.Fatalf("区间内容不一致: len=%d want %d", len(got), ln)
	}
}

// 各块注入不同延迟（后块先完成），输出仍须按序。
func TestOutOfOrderCompletion(t *testing.T) {
	content := pattern(512 << 10)
	srv := &rangeSrv{content: content}
	srv.hook = func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool {
		idx := start / (64 << 10)
		time.Sleep(time.Duration(3-idx%4) * 15 * time.Millisecond) // 前块慢、后块快
		return false
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), tOpts(4, 64<<10))
	got := readAll(t, mr)
	if !bytes.Equal(got, content) {
		t.Fatal("乱序完成后输出与源不一致")
	}
}

// 滑动窗口：读端只消费一块就暂停，服务端看到的最大 Range 起点不得超过窗口。
func TestSlidingWindowBound(t *testing.T) {
	const chunk = 64 << 10
	content := pattern(16 * chunk)
	srv := &rangeSrv{content: content}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	threads := 2 // tOpts 的预读 = threads×chunk → 窗口 2 块
	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), tOpts(threads, chunk))
	defer mr.Close()

	one := make([]byte, 1)
	if _, err := io.ReadFull(mr, one); err != nil { // 取走块 0 → 放行到块 2
		t.Fatalf("首字节读取失败: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // 给 worker 充分时间越界（若有 bug）
	// 已消费块 0（nextRead=1），窗口=2 → 最多发到块 2（起点 2*chunk）
	if max := srv.maxStart(); max > 2*chunk {
		t.Fatalf("窗口越界: 服务端收到起点 %d > %d", max, 2*chunk)
	}
	rest := readAll(t, mr)
	if !bytes.Equal(append(one, rest...), content) {
		t.Fatal("窗口测试内容不一致")
	}
}

// 直链过期：旧代 URL 一律 403，worker 经 provider 重取新链后成功。
func TestLinkRefreshOn403(t *testing.T) {
	const chunk = 64 << 10
	content := pattern(4 * chunk)
	srv := &rangeSrv{content: content}
	var reqN, provN int
	var mu sync.Mutex
	srv.hook = func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool {
		mu.Lock()
		reqN++
		n := reqN
		mu.Unlock()
		// 第 2 个请求起，g1 的链接判为过期
		if n >= 2 && strings.HasSuffix(r.URL.Path, "/g1") {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		return false
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	provider := func(ctx context.Context) (string, http.Header, error) {
		mu.Lock()
		provN++
		g := provN
		mu.Unlock()
		return fmt.Sprintf("%s/g%d", ts.URL, g), nil, nil
	}
	mr := NewMultiReader(context.Background(), provider, 0, int64(len(content)), tOpts(1, chunk))
	got := readAll(t, mr)
	if !bytes.Equal(got, content) {
		t.Fatal("换链后内容不一致")
	}
	mu.Lock()
	defer mu.Unlock()
	if provN < 2 {
		t.Fatalf("provider 应被重调（过期换链），实际调用 %d 次", provN)
	}
}

// 断流注入：块 1 首次尝试只写半块即断连，重试后成功。
func TestBrokenStreamRetry(t *testing.T) {
	const chunk = 64 << 10
	content := pattern(3 * chunk)
	srv := &rangeSrv{content: content}
	srv.hook = func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool {
		if start == chunk && try == 1 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(srv.content[start : start+chunk/2])
			panic(http.ErrAbortHandler) // 掐断连接
		}
		return false
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), tOpts(2, chunk))
	got := readAll(t, mr)
	if !bytes.Equal(got, content) {
		t.Fatal("断流重试后内容不一致")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.tries[chunk] < 2 {
		t.Fatalf("块 1 应至少请求 2 次，实际 %d", srv.tries[chunk])
	}
}

// 源不认 Range（恒 200）：多块必须报错；单块且 offset=0 可接受。
func TestNoRangeSupport(t *testing.T) {
	content := pattern(200 << 10)
	srv := &rangeSrv{content: content, noRange: true}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), tOpts(2, 64<<10))
	_, err := io.ReadAll(mr)
	mr.Close()
	if err == nil || !strings.Contains(err.Error(), "不支持 Range") {
		t.Fatalf("多块无 Range 应报错，got %v", err)
	}

	small := pattern(50 << 10) // 单块（<64KB 下限）
	srv2 := &rangeSrv{content: small, noRange: true}
	ts2 := httptest.NewServer(srv2.handler())
	defer ts2.Close()
	mr2 := NewMultiReader(context.Background(), fixedProvider(ts2.URL), 0, int64(len(small)), tOpts(4, 64<<10))
	got := readAll(t, mr2)
	if !bytes.Equal(got, small) {
		t.Fatal("单块 200 退化路径内容不一致")
	}
}

// 取消：慢源上取消 ctx，Read 立即返回、Close 不悬挂。
func TestCancel(t *testing.T) {
	content := pattern(512 << 10)
	srv := &rangeSrv{content: content}
	srv.hook = func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return false
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	mr := NewMultiReader(ctx, fixedProvider(ts.URL), 0, int64(len(content)), tOpts(2, 64<<10))
	errCh := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(mr)
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后应返回 context.Canceled，got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 Read 未及时返回")
	}
	done := make(chan struct{})
	go func() { mr.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 悬挂")
	}
}

// threads=1 顺序退化：内容正确、请求起点严格递增。
func TestSingleThread(t *testing.T) {
	content := pattern(300 << 10)
	srv := &rangeSrv{content: content}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 10, int64(len(content))-10, tOpts(1, 64<<10))
	got := readAll(t, mr)
	if !bytes.Equal(got, content[10:]) {
		t.Fatal("单线程内容不一致")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for i := 1; i < len(srv.starts); i++ {
		if srv.starts[i] <= srv.starts[i-1] {
			t.Fatalf("单线程请求起点应递增: %v", srv.starts)
		}
	}
}

// 云盘限流（OneDrive 429 + Retry-After）：按指示等待后重试须成功——
// 限流窗口不能掐死整条流，否则播放器重试风暴表现为"一直重连播放不出来"。
func TestThrottleRetryAfter(t *testing.T) {
	const chunk = 64 << 10
	content := pattern(3 * chunk)
	srv := &rangeSrv{content: content}
	srv.hook = func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool {
		if start <= chunk && try == 1 { // 前两块首次请求一律限流
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return true
		}
		return false
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	begin := time.Now()
	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), tOpts(2, chunk))
	got := readAll(t, mr)
	if !bytes.Equal(got, content) {
		t.Fatal("限流重试后内容不一致")
	}
	if time.Since(begin) < time.Second {
		t.Fatal("未按 Retry-After 等待即重试")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.tries[0] < 2 || srv.tries[chunk] < 2 {
		t.Fatalf("被限流的块应在等待后重试: tries=%v", srv.tries)
	}
}

// 换链之后仍是 403：必须改按限流退避，不能继续当"链过期"烧重试次数。
// 403 是 Google Drive 限流的主力返回码（rateLimitExceeded），当成链过期处理的话，
// 4 次尝试配 0/200/400/600ms 退避，1.2 秒就把整块判死，2 分钟的限流预算一秒没用上——
// 用户那边就是"只有 Google Drive 的片子播着播着断了"。
func TestForbiddenAfterRelinkBecomesThrottle(t *testing.T) {
	old := throttleDefault
	throttleDefault = 10 * time.Millisecond
	defer func() { throttleDefault = old }()

	const chunk = 64 << 10
	content := pattern(2 * chunk)
	srv := &rangeSrv{content: content}
	var mu sync.Mutex
	var n403, provN int
	srv.hook = func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool {
		if start != 0 {
			return false
		}
		mu.Lock()
		n403++
		n := n403
		mu.Unlock()
		if n <= 6 { // 远超 chunkAttempts，只有走限流预算才活得下来
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		return false
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	provider := func(ctx context.Context) (string, http.Header, error) {
		mu.Lock()
		provN++
		g := provN
		mu.Unlock()
		return fmt.Sprintf("%s/g%d", ts.URL, g), nil, nil
	}
	mr := NewMultiReader(context.Background(), provider, 0, int64(len(content)), tOpts(1, chunk))
	if got := readAll(t, mr); !bytes.Equal(got, content) {
		t.Fatal("持续 403 下应靠限流退避读完，内容不一致")
	}
	mu.Lock()
	defer mu.Unlock()
	// 首次取链 1 次 + 换链 1 次。每次 403 都换链等于每次都去强刷一遍 token，
	// 而 token 刷新是持锁做网络 I/O 的，该存储上的一切操作会跟着串行阻塞。
	if provN != 2 {
		t.Fatalf("换链应只发生一次，provider 实际被调用 %d 次", provN)
	}
}

func TestZeroLength(t *testing.T) {
	mr := NewMultiReader(context.Background(), fixedProvider("http://unused"), 0, 0, tOpts(4, 64<<10))
	b, err := io.ReadAll(mr)
	if err != nil || len(b) != 0 {
		t.Fatalf("length=0 应立即 EOF: n=%d err=%v", len(b), err)
	}
	mr.Close()
}

// ---- 传输层 ----

// 整个多线程加速都押在"每个 worker 独占一条 TCP 连接"上。一旦 h2 生效，Go 会把所有
// worker 的请求复用到同一条连接、共享一个拥塞窗口，加速就悄无声息地退化成单连接——
// 没有报错、没有日志，只有用户那边"码率一高就一直缓冲"。故对着真开了 h2 的服务器验。
func TestChunkTransportRejectsHTTP2(t *testing.T) {
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Proto))
	}))
	ts.EnableHTTP2 = true
	ts.StartTLS()
	defer ts.Close()

	// 对照组：httptest 自带的客户端对同一服务器应拿到 h2。这一步不通过，
	// 说明服务端根本没提供 h2，下面"拿到 1.1"就是个空断言。
	ctrl, body := getProto(t, ts.Client(), ts.URL)
	if ctrl != "HTTP/2.0" || body != "HTTP/2.0" {
		t.Fatalf("对照组未协商到 h2（proto=%s body=%s），本用例无效", ctrl, body)
	}

	// 受测组：只借用测试服务器的 CA 信任，ALPN 策略仍用 newChunkTransport 自己的。
	tr := newChunkTransport()
	tr.TLSClientConfig = &tls.Config{
		RootCAs: ts.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs,
	}
	got, gotBody := getProto(t, &http.Client{Transport: tr}, ts.URL)
	if got != "HTTP/1.1" || gotBody != "HTTP/1.1" {
		t.Fatalf("分块拉流仍在走 h2（proto=%s body=%s）：并发会被复用到一条连接上，加速失效",
			got, gotBody)
	}
	if tr.ResponseHeaderTimeout == 0 {
		t.Fatal("分块拉流必须设响应头超时，否则源挂起时要干等到 chunkTimeout")
	}
	if tr.MaxIdleConnsPerHost < 8 {
		t.Fatalf("每 host 空闲连接仅 %d，worker 会反复重做 TCP+TLS 握手", tr.MaxIdleConnsPerHost)
	}
}

func getProto(t *testing.T, c *http.Client, url string) (string, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	return resp.Proto, string(b)
}

// 单流（ffmpeg 读源那条路）同样不应走 h2。
func TestSingleClientRejectsHTTP2(t *testing.T) {
	tr, ok := singleClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("singleClient 的 Transport 类型异常")
	}
	if tr.ForceAttemptHTTP2 {
		t.Fatal("singleClient 仍会尝试 h2")
	}
	if tr.TLSNextProto == nil {
		t.Fatal("TLSNextProto 为 nil 会让 net/http 自动装配 h2")
	}
	if len(tr.TLSNextProto) != 0 {
		t.Fatalf("TLSNextProto 应为空表，实际 %d 项", len(tr.TLSNextProto))
	}
}

// ---- 诊断 ----

// 诊断默认关闭，没有用例就等于这段代码永远没被跑过——真到线上要排查时才发现它 panic
// 或数字不对就晚了。这里把开关打开跑一遍完整链路，并核对汇总的字节数确实对得上。
func TestDebugTrace(t *testing.T) {
	oldDbg := debugOn
	debugOn = true
	defer func() { debugOn = oldDbg }()

	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) }()

	const chunk = 64 << 10
	content := pattern(4 * chunk)
	srv := &rangeSrv{content: content}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)),
		Opts{Threads: 2, ChunkBytes: chunk, ReadaheadBytes: 4 * chunk, Label: "诊断.bin"})
	if got := readAll(t, mr); !bytes.Equal(got, content) {
		t.Fatal("开诊断后内容不一致")
	}
	mr.Close() // 汇总在 Close 里出

	out := buf.String()
	t.Log("诊断日志样张：\n" + out) // -v 时可直接看到线上会打成什么样
	for _, want := range []string{"诊断.bin", "开流", "关流", "块0", "连接"} {
		if !strings.Contains(out, want) {
			t.Fatalf("诊断日志缺少 %q:\n%s", want, out)
		}
	}
	// 汇总的块数与字节数必须与实际相符，否则这份日志会把排查带偏
	if !strings.Contains(out, "4 块 / 256.0KB") {
		t.Fatalf("汇总数字不符（应为 4 块 / 256.0KB）:\n%s", out)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0B"}, {512, "512B"}, {1024, "1.0KB"}, {1536, "1.5KB"},
		{4 << 20, "4.0MB"}, {int64(3.5 * (1 << 30)), "3.5GB"}, {2 << 40, "2.0TB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.n); got != c.want {
			t.Fatalf("humanBytes(%d) = %s, want %s", c.n, got, c.want)
		}
	}
}

// 每个 worker 应当自始至终复用同一条连接：拿到的连接数不超过线程数，其余全是复用。
// 退化时不会报错也不会变慢到看得出来（本地测试尤其看不出），只是每块都白白多一次
// TCP+TLS 握手——跨国链路上一次就是几百毫秒，这正是当初"拉流很不积极"的一部分。
func TestConnectionsAreReused(t *testing.T) {
	oldDbg := debugOn
	debugOn = true
	defer func() { debugOn = oldDbg }()
	oldOut := log.Writer()
	log.SetOutput(io.Discard) // 本用例只看计数，不看日志
	defer log.SetOutput(oldOut)

	const chunk = 64 << 10
	const threads = 3
	content := pattern(12 * chunk)
	srv := &rangeSrv{content: content}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)),
		Opts{Threads: threads, ChunkBytes: chunk, ReadaheadBytes: 4 * chunk})
	if got := readAll(t, mr); !bytes.Equal(got, content) {
		t.Fatal("内容不一致")
	}
	m, ok := mr.(*MultiReader)
	if !ok {
		t.Fatal("类型异常")
	}
	dialed, reused := m.dbg.dialed.Load(), m.dbg.reused.Load()
	if dialed > threads {
		t.Fatalf("新建连接 %d 条，超过线程数 %d：连接没被复用，每块都在重新握手", dialed, threads)
	}
	if reused < int64(12-threads) {
		t.Fatalf("复用 %d 次，应至少 %d 次（共 12 块）", reused, 12-threads)
	}
}

// ---- 分块排程 ----

// newSchedReader 只装排程所需字段，用来单独验证 chunkRange 这个纯函数。
func newSchedReader(offset, length, chunkBytes int64) *MultiReader {
	off := rampOffsets(chunkBytes)
	return &MultiReader{offset: offset, length: length, chunk: chunkBytes,
		chunks: chunkCount(length, off, chunkBytes), rampOff: off, rampN: len(off) - 1}
}

// 排程必须把 [offset, offset+length) 首尾相接地铺满：不重叠、不留缝、末块不越界。
// 这段算错就是静默的数据损坏（花屏 / 解码失败），比任何性能问题都严重。
func TestChunkScheduleTiles(t *testing.T) {
	chunks := []int64{64 << 10, 512 << 10, 1 << 20, 4 << 20, 8 << 20}
	lengths := []int64{1, 12345, 512 << 10, (512 << 10) + 1, 1 << 20, 3<<20 + 7, 4 << 20, 16<<20 + 999}
	offsets := []int64{0, 1, 4096, 7 << 20}

	for _, cb := range chunks {
		for _, ln := range lengths {
			for _, off := range offsets {
				m := newSchedReader(off, ln, cb)
				if m.chunks < 1 {
					t.Fatalf("chunk=%d len=%d off=%d: 块数 %d", cb, ln, off, m.chunks)
				}
				want := off // 下一块应有的起点
				for i := 0; i < m.chunks; i++ {
					s, e := m.chunkRange(i)
					if s != want {
						t.Fatalf("chunk=%d len=%d off=%d 块%d: 起点 %d，应为 %d（有缝或重叠）",
							cb, ln, off, i, s, want)
					}
					if e < s {
						t.Fatalf("chunk=%d len=%d off=%d 块%d: 空块 [%d-%d]", cb, ln, off, i, s, e)
					}
					if e > off+ln-1 {
						t.Fatalf("chunk=%d len=%d off=%d 块%d: 末尾 %d 越界（上限 %d）",
							cb, ln, off, i, e, off+ln-1)
					}
					want = e + 1
				}
				if want != off+ln {
					t.Fatalf("chunk=%d len=%d off=%d: 只铺到 %d，应铺满到 %d",
						cb, ln, off, want, off+ln)
				}
			}
		}
	}
}

// 渐进分块：生产配置（4MB）下首块须显著小于稳态分块——读端要等一整块下完才吐字节，
// 首块多大就是起播与每次拖动的首字节延迟。稳态块仍须是完整的 chunkBytes。
func TestRampShrinksFirstChunk(t *testing.T) {
	const cb = 4 << 20
	m := newSchedReader(0, 64<<20, cb)
	if _, e := m.chunkRange(0); e+1 != rampBase {
		t.Fatalf("首块应为 %d 字节，实际 %d", rampBase, e+1)
	}
	// 渐进段逐块翻倍，追平 chunkBytes 后转入等长
	for i := 1; i < m.rampN; i++ {
		s, e := m.chunkRange(i)
		if got, want := e-s+1, int64(rampBase)<<i; got != want {
			t.Fatalf("块%d 大小 %d，应为 %d", i, got, want)
		}
	}
	s, e := m.chunkRange(m.rampN + 3)
	if got := e - s + 1; got != cb {
		t.Fatalf("稳态块大小 %d，应为 %d", got, cb)
	}
}

// chunkBytes ≤ rampBase 时渐进段为空，全程等长——小分块配置的行为不变。
func TestRampDegradesToUniform(t *testing.T) {
	for _, cb := range []int64{64 << 10, 128 << 10, rampBase} {
		m := newSchedReader(0, 4<<20, cb)
		if m.rampN != 0 {
			t.Fatalf("chunk=%d 不应有渐进段，实际 %d 块", cb, m.rampN)
		}
		for i := 0; i < 5; i++ {
			s, e := m.chunkRange(i)
			if got := e - s + 1; got != cb {
				t.Fatalf("chunk=%d 块%d 大小 %d，应等长", cb, i, got)
			}
		}
	}
}

// ---- 调参 ----

func TestOptsNormalize(t *testing.T) {
	cases := []struct{ in, want Opts }{
		{Opts{}, Opts{Threads: 1, ChunkBytes: minChunkBytes, ReadaheadBytes: minChunkBytes}},
		{Opts{Threads: -3, ChunkBytes: 1 << 10},
			Opts{Threads: 1, ChunkBytes: minChunkBytes, ReadaheadBytes: minChunkBytes}},
		// 预读不足一块 → 提到一块，否则窗口为 0，读端永远等不到数据
		{Opts{Threads: 4, ChunkBytes: 4 << 20, ReadaheadBytes: 1 << 20},
			Opts{Threads: 4, ChunkBytes: 4 << 20, ReadaheadBytes: 4 << 20}},
		{Opts{Threads: 4, ChunkBytes: 4 << 20, ReadaheadBytes: 32 << 20},
			Opts{Threads: 4, ChunkBytes: 4 << 20, ReadaheadBytes: 32 << 20}},
	}
	for i, c := range cases {
		if got := c.in.normalize(); got != c.want {
			t.Fatalf("case %d: got %+v want %+v", i, got, c.want)
		}
	}
}

func TestOptsWindow(t *testing.T) {
	if got := (Opts{Threads: 4, ChunkBytes: 4 << 20, ReadaheadBytes: 32 << 20}).window(); got != 8 {
		t.Fatalf("窗口应为 8，实际 %d", got)
	}
	// 窗口只认预读：线程再多也不能把这条流的常驻内存顶上去（8 线程 × 4MB 分块，
	// 预读只给 8MB → 就是 2 块，不是 8 块）
	if got := (Opts{Threads: 8, ChunkBytes: 4 << 20, ReadaheadBytes: 8 << 20}).window(); got != 2 {
		t.Fatalf("窗口应按预读算出 2，实际 %d", got)
	}
	// 极值配置也得守住预读：32 线程 × 64MB 分块，预读 128MB → 2 块（128MB），而非 2GB
	if got := (Opts{Threads: 32, ChunkBytes: 64 << 20, ReadaheadBytes: 128 << 20}).window(); got != 2 {
		t.Fatalf("极值配置窗口应为 2，实际 %d", got)
	}
}

// 预读缓冲放大窗口：worker 得以跑在读端前面，某块慢不再让整条流停摆。
// 与 TestSlidingWindowBound 合起来覆盖 window() 的两头。
func TestReadaheadWidensWindow(t *testing.T) {
	const chunk = 64 << 10
	content := pattern(32 * chunk)
	srv := &rangeSrv{content: content}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	// 单线程，但预读给到 8 块 → 窗口 8
	o := Opts{Threads: 1, ChunkBytes: chunk, ReadaheadBytes: 8 * chunk}
	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), o)
	defer mr.Close()

	one := make([]byte, 1)
	if _, err := io.ReadFull(mr, one); err != nil {
		t.Fatalf("首字节读取失败: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // 让 worker 把窗口跑满
	max := srv.maxStart()
	if max > 8*chunk { // 已消费块 0（nextRead=1），窗口 8 → 最远发到块 8
		t.Fatalf("窗口越界: 服务端收到起点 %d > %d", max, 8*chunk)
	}
	if max < 4*chunk { // 老行为（窗口=线程数=1）最远只到块 1，说明预读没生效
		t.Fatalf("预读未生效: 最远起点仅 %d，应远超单线程窗口", max)
	}
	rest := readAll(t, mr)
	if !bytes.Equal(append(one, rest...), content) {
		t.Fatal("预读窗口测试内容不一致")
	}
}

// ---- 失速看门狗 ----

// 源发完响应头就再也不给数据：必须在 stallTimeout 量级内失败并重试，
// 而不是干等到 chunkTimeout——那正是"卡很久、后台一点网络活动都没有"的老毛病。
func TestStallWatchdog(t *testing.T) {
	old := stallTimeout
	stallTimeout = 200 * time.Millisecond
	defer func() { stallTimeout = old }()

	const chunk = 64 << 10
	content := pattern(4 * chunk)
	srv := &rangeSrv{content: content}
	srv.hook = func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush() // 头先出去，之后一个字节也不发
		<-r.Context().Done()
		return true
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	begin := time.Now()
	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), tOpts(1, chunk))
	_, err := io.ReadAll(mr)
	mr.Close()
	el := time.Since(begin)

	if err == nil {
		t.Fatal("源失速应报错，实际读取成功")
	}
	if el > 10*time.Second {
		t.Fatalf("失速判定耗时 %v，看门狗未生效（退化成了干等 chunkTimeout）", el)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.tries[0] < 2 {
		t.Fatalf("失速后应重试，块 0 实际只请求 %d 次", srv.tries[0])
	}
}
