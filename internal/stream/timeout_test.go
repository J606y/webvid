package stream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestChunkDeadlineScales 总时限随块大小折算，不是一刀切。
// 512KB 的首块拖十几秒就不正常；64MB 的块在慢链路上本就需要时间，
// 一个固定常量必然一头太松一头太紧。
func TestChunkDeadlineScales(t *testing.T) {
	const K, M = 1 << 10, 1 << 20
	cases := []struct {
		size int64
		want time.Duration
	}{
		{512 * K, chunkBase + 2*time.Second},
		{4 * M, chunkBase + 16*time.Second},
		{64 * M, chunkBase + 256*time.Second},
	}
	for _, c := range cases {
		if got := chunkDeadline(c.size); got != c.want {
			t.Errorf("chunkDeadline(%d) = %s，期望 %s", c.size, got, c.want)
		}
	}
	if chunkDeadline(512*K) >= chunkDeadline(64*M) {
		t.Fatal("大块的时限必须比小块宽")
	}
}

// TestStallReaderFiresOnSilence 响应体停住不动时必须被判死并给出可辨认的错误，
// 而不是让上层只看到一句 context canceled——那与客户端主动断开分不出来。
func TestStallReaderFiresOnSilence(t *testing.T) {
	// 一个永远不再吐字节、也不结束的读者
	pr, pw := io.Pipe()
	defer pw.Close()

	_, cancel := context.WithCancel(context.Background())
	fired := make(chan struct{})
	sr := newStallReader(pr, 60*time.Millisecond, func() {
		cancel()
		pr.CloseWithError(context.Canceled) // 模拟请求被取消导致读失败
		close(fired)
	})
	defer sr.stop()

	buf := make([]byte, 4)
	_, err := sr.Read(buf)
	if err == nil {
		t.Fatal("停滞后 Read 应当报错")
	}
	<-fired
	if !strings.Contains(err.Error(), "停滞") {
		t.Fatalf("错误应指明是停滞，实际：%v", err)
	}
}

// TestStallReaderResetsOnData 持续有字节就不该被判死，哪怕总耗时远超停滞阈值。
// 慢但活着的链路必须能读完，否则激进的闸会把正常播放误杀。
func TestStallReaderResetsOnData(t *testing.T) {
	const total = 20
	slow := &drip{n: total, gap: 15 * time.Millisecond}

	killed := false
	sr := newStallReader(slow, 60*time.Millisecond, func() { killed = true })
	defer sr.stop()

	got, err := io.ReadAll(sr)
	if err != nil {
		t.Fatalf("慢但不停滞的流应当读完，实际报错：%v", err)
	}
	if len(got) != total {
		t.Fatalf("读到 %d 字节，期望 %d", len(got), total)
	}
	if killed {
		t.Fatal("每 15ms 有一个字节，60ms 的停滞闸不该触发")
	}
}

// drip 每次 Read 前先等 gap，再吐一个字节，共 n 次。
type drip struct {
	n   int
	gap time.Duration
}

func (d *drip) Read(p []byte) (int, error) {
	if d.n == 0 {
		return 0, io.EOF
	}
	time.Sleep(d.gap)
	d.n--
	p[0] = 'x'
	return 1, nil
}

// TestChunkClientHasHeaderTimeout 分块客户端必须带响应头超时，且不得改动 HTTP/2 协商
// 与连接池默认值——那几项本次刻意不碰。
func TestChunkClientHasHeaderTimeout(t *testing.T) {
	tr, ok := chunkClient.Transport.(*http.Transport)
	if !ok {
		t.Skip("标准库默认 Transport 类型有变，跳过")
	}
	if tr.ResponseHeaderTimeout != headerTimeout {
		t.Fatalf("ResponseHeaderTimeout = %s，期望 %s", tr.ResponseHeaderTimeout, headerTimeout)
	}
	def, _ := http.DefaultTransport.(*http.Transport)
	if def == nil {
		return
	}
	if tr.ForceAttemptHTTP2 != def.ForceAttemptHTTP2 {
		t.Fatal("不该改动 ForceAttemptHTTP2：HTTP/2 协商本次不在范围内")
	}
	if tr.MaxIdleConnsPerHost != def.MaxIdleConnsPerHost {
		t.Fatal("不该改动 MaxIdleConnsPerHost：连接池本次不在范围内")
	}
}

// TestServeHeaderTimeoutStuckUpstream 上游只回响应头前的静默：整块时限之内就该被
// 判死并重试，而不是等到时限耗尽。这里只验"不会永远挂着"。
func TestServeHeaderTimeoutStuckUpstream(t *testing.T) {
	if testing.Short() {
		t.Skip("依赖真实等待，-short 跳过")
	}
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			time.Sleep(headerTimeout + time.Second) // 第一次故意不回头
			return
		}
		w.Header().Set("Content-Range", "bytes 0-3/4")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("abcd"))
	}))
	defer up.Close()

	mr := NewMultiReader(context.Background(), fixedProvider(up.URL), 0, 4, 1, 64<<10)
	defer mr.Close()
	got, err := io.ReadAll(mr)
	if err != nil {
		t.Fatalf("第一次挂住后应换一次重试并成功，实际：%v", err)
	}
	if string(got) != "abcd" {
		t.Fatalf("读到 %q，期望 abcd", got)
	}
	if hits < 2 {
		t.Fatalf("上游只被请求了 %d 次，说明没有发生重试", hits)
	}
}
