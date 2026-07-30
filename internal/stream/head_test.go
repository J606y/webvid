package stream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 首块流式直通的命门：块 0 只到了开头一小段、远未下满时，读端就必须拿得到字节。
// 换回 io.ReadFull（整块下满才交付）这条用例必然超时——没有它，这次改动等于没做。
func TestHeadStreamsBeforeChunkComplete(t *testing.T) {
	const chunk = 512 << 10
	const teaser = 4 << 10 // 源先吐这么多，其余卡住不给
	content := pattern(4 * chunk)

	released := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(released) }) }
	defer release() // 断言失败也要放行，否则 handler goroutine 悬着

	srv := &rangeSrv{content: content}
	srv.hook = func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool {
		if start != 0 {
			return false
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[:teaser])
		w.(http.Flusher).Flush()
		select {
		case <-released:
		case <-r.Context().Done():
			return true
		}
		w.Write(content[teaser : end+1])
		return true
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), 2, chunk)
	defer mr.Close()

	head := make([]byte, teaser)
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(mr, head); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("首块开头 %d 字节读取失败: %v", teaser, err)
		}
	case <-time.After(3 * time.Second): // 远小于 stallTimeout，不会撞上停滞判定
		t.Fatal("块 0 尚未下满就该吐字节——流式直通失效")
	}
	if !bytes.Equal(head, content[:teaser]) {
		t.Fatal("首块流式输出的内容不对")
	}

	release()
	rest, err := io.ReadAll(mr)
	if err != nil {
		t.Fatalf("剩余内容读取失败: %v", err)
	}
	if !bytes.Equal(append(head, rest...), content) {
		t.Fatal("流式直通后全量内容不一致")
	}
}

// 流已经开始吐字节就无法整体重发，只能带偏移续拉。
// 源在块 0 吐出一半后掐断：必须从断点补齐，且对读端完全透明。
func TestHeadResumeAfterBreak(t *testing.T) {
	const chunk = 128 << 10
	const half = chunk / 2
	content := pattern(2 * chunk)

	srv := &rangeSrv{content: content}
	srv.hook = func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool {
		if start != 0 || try != 1 {
			return false
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[:half])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // 掐断连接
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), 2, chunk)
	got := readAll(t, mr)
	if !bytes.Equal(got, content) {
		t.Fatalf("首块续拉后内容不一致: 得到 %d 字节，期望 %d", len(got), len(content))
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.tries[half] == 0 {
		t.Fatalf("应从断点 %d 续拉，实际请求起点: %v", half, srv.starts)
	}
}

// 续拉次数耗尽即放弃：响应短传交给客户端自行续传，不能无限重开把上游打死。
func TestHeadResumeGivesUp(t *testing.T) {
	const chunk = 64 << 10
	content := pattern(2 * chunk)

	srv := &rangeSrv{content: content}
	srv.hook = func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool {
		if start >= chunk { // 块 1 正常，只掐块 0
			return false
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[start : start+1]) // 每次只给 1 字节就断
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), 2, chunk)
	defer mr.Close()
	_, err := io.ReadAll(mr)
	if err == nil {
		t.Fatal("续拉次数耗尽后应报错，而不是静默截断")
	}
	if !strings.Contains(err.Error(), "续拉") {
		t.Fatalf("错误应指明续拉耗尽，实际: %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if n := len(srv.starts); n > headResumes+3 { // 块 0 首开 + headResumes 次续拉 + 块 1
		t.Fatalf("续拉次数失控：共发出 %d 个请求 %v", n, srv.starts)
	}
}

// 首块打开阶段撞上直链过期：必须换链重试。块 0 卡死等于整条流卡死，
// 它和后续块共用 openWithRetry，这条用例守的是「首块也真的走了那套策略」。
func TestHeadRelinkOn403(t *testing.T) {
	const chunk = 64 << 10
	content := pattern(3 * chunk)

	srv := &rangeSrv{content: content}
	srv.hook = func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool {
		if start == 0 && strings.HasSuffix(r.URL.Path, "/g1") {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		return false
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	var mu sync.Mutex
	var provN int
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
		t.Fatal("首块换链后内容不一致")
	}
	mu.Lock()
	defer mu.Unlock()
	if provN < 2 {
		t.Fatalf("首块 403 应触发换链，provider 实际只调用 %d 次", provN)
	}
}

// Close 可与读端并发、可重复调用：既不能悬挂，也不能重复关连接。
func TestHeadCloseConcurrentWithRead(t *testing.T) {
	content := pattern(512 << 10)
	srv := &rangeSrv{content: content}
	srv.hook = func(w http.ResponseWriter, r *http.Request, start, end int64, try int) bool {
		select { // 上游一直不吐，把读端按在 head.Read 里
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
		return true
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), 2, 64<<10)
	go io.ReadAll(mr) //nolint:errcheck // 读到的错误不重要，这里只为占住读端
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		mr.Close()
		mr.Close() // 幂等
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 悬挂")
	}
}

// 区间比一个块还小（浏览器找 moov 时的零碎请求就长这样）：
// 只有块 0、没有 worker，全程走流式直通也必须完整正确。
func TestHeadOnlyChunk(t *testing.T) {
	content := pattern(300 << 10)
	srv := &rangeSrv{content: content}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	off, ln := int64(123_456), int64(32<<10)
	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), off, ln, 4, 4<<20)
	got := readAll(t, mr)
	if !bytes.Equal(got, content[off:off+ln]) {
		t.Fatalf("单块区间内容不一致: 得到 %d 字节，期望 %d", len(got), ln)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.starts) != 1 {
		t.Fatalf("单块区间只该发一个请求，实际 %v", srv.starts)
	}
}
