package stream

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// newServeTS 起「上游 Range 源 + 下游 Serve 代理」两级服务，返回下游 URL 与上游记录。
func newServeTS(t *testing.T, content []byte, size int64) (string, *rangeSrv, func()) {
	t.Helper()
	up := &rangeSrv{content: content}
	upstream := httptest.NewServer(up.handler())
	mod := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Serve(w, r, "f.bin", mod, size, "application/octet-stream",
			fixedProvider(upstream.URL), 3, 64<<10)
	}))
	return down.URL, up, func() { upstream.Close(); down.Close() }
}

func doReq(t *testing.T, method, url, rangeHdr string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rangeHdr, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

func TestServeFull(t *testing.T) {
	content := pattern(300<<10 + 17)
	url, _, done := newServeTS(t, content, int64(len(content)))
	defer done()

	resp, body := doReq(t, http.MethodGet, url, "")
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" ||
		resp.Header.Get("Content-Type") != "application/octet-stream" ||
		resp.Header.Get("Content-Length") != fmt.Sprint(len(content)) {
		t.Fatalf("响应头不符: %+v", resp.Header)
	}
	if !bytes.Equal(body, content) {
		t.Fatal("全量 body 不一致")
	}
}

func TestServeRangeOpenEnd(t *testing.T) {
	content := pattern(200 << 10)
	url, _, done := newServeTS(t, content, int64(len(content)))
	defer done()

	resp, body := doReq(t, http.MethodGet, url, "bytes=100-")
	if resp.StatusCode != 206 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	wantCR := fmt.Sprintf("bytes 100-%d/%d", len(content)-1, len(content))
	if resp.Header.Get("Content-Range") != wantCR {
		t.Fatalf("Content-Range=%q want %q", resp.Header.Get("Content-Range"), wantCR)
	}
	if !bytes.Equal(body, content[100:]) {
		t.Fatal("bytes=100- body 不一致")
	}
}

func TestServeRangeBounded(t *testing.T) {
	content := pattern(200 << 10)
	url, up, done := newServeTS(t, content, int64(len(content)))
	defer done()

	resp, body := doReq(t, http.MethodGet, url, "bytes=100-199")
	if resp.StatusCode != 206 || len(body) != 100 || !bytes.Equal(body, content[100:200]) {
		t.Fatalf("bytes=100-199: status=%d len=%d", resp.StatusCode, len(body))
	}
	// 有界区间不应向上游拉取整个文件之后的分块
	if max := up.maxStart(); max >= 200 {
		t.Fatalf("有界区间越拉: 上游收到起点 %d", max)
	}
}

func TestServeRangeSuffix(t *testing.T) {
	content := pattern(200 << 10)
	url, _, done := newServeTS(t, content, int64(len(content)))
	defer done()

	resp, body := doReq(t, http.MethodGet, url, "bytes=-50")
	if resp.StatusCode != 206 || !bytes.Equal(body, content[len(content)-50:]) {
		t.Fatalf("bytes=-50: status=%d len=%d", resp.StatusCode, len(body))
	}
}

func TestServeUnsatisfiable(t *testing.T) {
	content := pattern(1000)
	url, _, done := newServeTS(t, content, int64(len(content)))
	defer done()

	resp, _ := doReq(t, http.MethodGet, url, "bytes=99999-")
	if resp.StatusCode != 416 {
		t.Fatalf("status=%d want 416", resp.StatusCode)
	}
	if resp.Header.Get("Content-Range") != "bytes */1000" {
		t.Fatalf("Content-Range=%q", resp.Header.Get("Content-Range"))
	}
}

func TestServeHead(t *testing.T) {
	content := pattern(100 << 10)
	url, up, done := newServeTS(t, content, int64(len(content)))
	defer done()

	resp, body := doReq(t, http.MethodHead, url, "bytes=0-99")
	if resp.StatusCode != 206 || len(body) != 0 {
		t.Fatalf("HEAD: status=%d len=%d", resp.StatusCode, len(body))
	}
	if resp.Header.Get("Content-Length") != "100" {
		t.Fatalf("HEAD Content-Length=%q", resp.Header.Get("Content-Length"))
	}
	if len(up.starts) != 0 {
		t.Fatal("HEAD 不应向上游拉流")
	}
}

// 多区间请求按无 Range 处理（宽松降级为 200 全量）。
func TestServeMultiRangeFallback(t *testing.T) {
	content := pattern(10 << 10)
	url, _, done := newServeTS(t, content, int64(len(content)))
	defer done()

	resp, body := doReq(t, http.MethodGet, url, "bytes=0-1,5-6")
	if resp.StatusCode != 200 || !bytes.Equal(body, content) {
		t.Fatalf("多区间应降级 200 全量: status=%d", resp.StatusCode)
	}
}

// size 未知：单流透传，客户端 Range 原样转发、上游状态镜像。
func TestServeUnknownSizePassthrough(t *testing.T) {
	content := pattern(50 << 10)
	var mu sync.Mutex
	var seenRange string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenRange = r.Header.Get("Range")
		mu.Unlock()
		var a, b int64
		if _, err := fmt.Sscanf(seenRange, "bytes=%d-%d", &a, &b); err == nil {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, len(content)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[a : b+1])
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer upstream.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Serve(w, r, "f.bin", time.Time{}, -1, "video/mp4", fixedProvider(upstream.URL), 4, 64<<10)
	}))
	defer down.Close()

	resp, body := doReq(t, http.MethodGet, down.URL, "bytes=10-19")
	if resp.StatusCode != 206 || !bytes.Equal(body, content[10:20]) {
		t.Fatalf("透传: status=%d len=%d", resp.StatusCode, len(body))
	}
	mu.Lock()
	gotRange := seenRange
	mu.Unlock()
	if gotRange != "bytes=10-19" {
		t.Fatalf("上游应收到原样 Range，got %q", gotRange)
	}

	resp2, body2 := doReq(t, http.MethodGet, down.URL, "")
	if resp2.StatusCode != 200 || !bytes.Equal(body2, content) {
		t.Fatalf("透传全量: status=%d", resp2.StatusCode)
	}
}
