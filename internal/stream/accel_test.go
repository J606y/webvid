package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), 4, 128<<10)
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
	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), off, ln, 3, 64<<10)
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

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), 4, 64<<10)
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

	threads := 2 // window=2
	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), threads, chunk)
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
	mr := NewMultiReader(context.Background(), provider, 0, int64(len(content)), 1, chunk)
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

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), 2, chunk)
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

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), 2, 64<<10)
	_, err := io.ReadAll(mr)
	mr.Close()
	if err == nil || !strings.Contains(err.Error(), "不支持 Range") {
		t.Fatalf("多块无 Range 应报错，got %v", err)
	}

	small := pattern(50 << 10) // 单块（<64KB 下限）
	srv2 := &rangeSrv{content: small, noRange: true}
	ts2 := httptest.NewServer(srv2.handler())
	defer ts2.Close()
	mr2 := NewMultiReader(context.Background(), fixedProvider(ts2.URL), 0, int64(len(small)), 4, 64<<10)
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
	mr := NewMultiReader(ctx, fixedProvider(ts.URL), 0, int64(len(content)), 2, 64<<10)
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

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 10, int64(len(content))-10, 1, 64<<10)
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

func TestZeroLength(t *testing.T) {
	mr := NewMultiReader(context.Background(), fixedProvider("http://unused"), 0, 0, 4, 64<<10)
	b, err := io.ReadAll(mr)
	if err != nil || len(b) != 0 {
		t.Fatalf("length=0 应立即 EOF: n=%d err=%v", len(b), err)
	}
	mr.Close()
}
