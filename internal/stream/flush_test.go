package stream

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// orderRecorder 记录 Flush 与首次 Write 的先后。
type orderRecorder struct {
	http.ResponseWriter
	flushed          bool
	wroteBeforeFlush bool // 第一次 Write 时还没 Flush 过 = 响应头晚于首字节
	sawWrite         bool
}

func (r *orderRecorder) Write(p []byte) (int, error) {
	if !r.sawWrite {
		r.sawWrite = true
		r.wroteBeforeFlush = !r.flushed
	}
	return r.ResponseWriter.Write(p)
}

func (r *orderRecorder) Flush() { r.flushed = true }

// TestServeFlushesHeadersBeforeBody 响应头必须在第一个字节之前就上路。
//
// 这条是真故障的回归测试：Go 的 ResponseWriter 带缓冲，WriteHeader 只记状态码，
// 响应头要等第一次 Write 才发出。而 Serve 的第一次 Write 要等 MultiReader 凑满一整块，
// 跨国拉一块 4MB 期间客户端连响应头都收不到 —— Chrome 判定请求已死、取消重发，
// 重发又从头拉块，快进一次产生 20+ 条同 Range 请求，服务器全速下载却一字节都吐不出。
func TestServeFlushesHeadersBeforeBody(t *testing.T) {
	content := pattern(200 << 10)
	up := &rangeSrv{content: content}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	rec := &orderRecorder{ResponseWriter: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/f.bin", nil)
	Serve(rec, req, "f.bin", time.Time{}, int64(len(content)), "application/octet-stream",
		fixedProvider(upstream.URL), 2, 64<<10)

	if !rec.flushed {
		t.Fatal("Serve 没有 Flush 响应头：客户端在首块下完之前收不到任何响应头")
	}
	if !rec.sawWrite {
		t.Fatal("没有写出 body，测试前提不成立")
	}
	if rec.wroteBeforeFlush {
		t.Fatal("第一个字节先于响应头 Flush 写出，等于没起作用")
	}
}

// TestServeHeadNoFlushNeeded HEAD 不进 body 分支，Flush 与否不影响；此处只保证不 panic
// 且状态码正确（包装过的 writer 可能没有 Flusher，flush() 必须能安全退化）。
func TestServeHeadNoFlushNeeded(t *testing.T) {
	content := pattern(4 << 10)
	up := &rangeSrv{content: content}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	// 故意用一个不实现 Flusher 的包装：flush() 应当静默跳过
	rec := httptest.NewRecorder()
	noFlush := struct{ http.ResponseWriter }{rec}
	req := httptest.NewRequest(http.MethodGet, "/f.bin", nil)
	Serve(noFlush, req, "f.bin", time.Time{}, int64(len(content)), "application/octet-stream",
		fixedProvider(upstream.URL), 1, 64<<10)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d，期望 200", rec.Code)
	}
	if rec.Body.Len() != len(content) {
		t.Fatalf("body 长度 %d，期望 %d", rec.Body.Len(), len(content))
	}
}
