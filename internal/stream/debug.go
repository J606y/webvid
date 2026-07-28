package stream

import (
	"context"
	"fmt"
	"log"
	"net/http/httptrace"
	"os"
	"sync/atomic"
	"time"
)

// 拉流诊断：NL_STREAM_DEBUG=1 时逐块记录首字节延迟、耗时、速率与连接复用情况，
// 关流时给一份汇总。用来实测并发到底有没有生效——h2 会把所有 worker 压到一条 TCP
// 连接上，光看总速率分不出是"源慢"还是"并发假的"，连接复用与单块速率才分得出。
// 开关在进程启动时读一次；关闭时 newStreamStats 返回 nil，各方法皆为空操作。
var debugOn = os.Getenv("NL_STREAM_DEBUG") == "1"

// streamStats 一条流的累计诊断计数。所有方法允许 nil 接收者（诊断关闭）。
type streamStats struct {
	label  string
	opts   Opts
	length int64
	begun  time.Time

	chunks  atomic.Int64
	bytes   atomic.Int64
	retries atomic.Int64
	reused  atomic.Int64
	dialed  atomic.Int64
}

func newStreamStats(o Opts, length int64) *streamStats {
	if !debugOn {
		return nil
	}
	label := o.Label
	if label == "" {
		label = "-"
	}
	st := &streamStats{label: label, opts: o, length: length, begun: time.Now()}
	log.Printf("[stream] %s 开流: %s, 线程 %d, 分块 %s, 预读 %s(窗口 %d 块)",
		label, humanBytes(length), o.Threads, humanBytes(o.ChunkBytes),
		humanBytes(o.ReadaheadBytes), o.window())
	return st
}

func (s *streamStats) retry() {
	if s != nil {
		s.retries.Add(1)
	}
}

// begin 开一次分块请求的追踪；诊断关闭时原样返回 ctx 与 nil。
func (s *streamStats) begin(ctx context.Context, idx int, start, end int64, attempt int) (context.Context, *chunkTrace) {
	if s == nil {
		return ctx, nil
	}
	t := &chunkTrace{st: s, idx: idx, start: start, end: end, attempt: attempt, t0: time.Now()}
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			t.reused = info.Reused
			if info.Reused {
				s.reused.Add(1)
			} else {
				s.dialed.Add(1)
			}
		},
	}
	return httptrace.WithClientTrace(ctx, trace), t
}

func (s *streamStats) summary() {
	if s == nil {
		return
	}
	el := time.Since(s.begun)
	log.Printf("[stream] %s 关流: %d 块 / %s, 用时 %s, 均速 %s, 重试 %d, 连接 新建 %d / 复用 %d",
		s.label, s.chunks.Load(), humanBytes(s.bytes.Load()), el.Round(time.Millisecond),
		humanRate(s.bytes.Load(), el), s.retries.Load(), s.dialed.Load(), s.reused.Load())
}

// chunkTrace 一次分块 HTTP 请求的追踪。所有方法允许 nil 接收者（诊断关闭）。
type chunkTrace struct {
	st      *streamStats
	idx     int
	start   int64
	end     int64
	attempt int
	t0      time.Time
	tHdr    time.Time
	code    int
	reused  bool
}

func (t *chunkTrace) gotHeader(code int) {
	if t != nil {
		t.tHdr, t.code = time.Now(), code
	}
}

func (t *chunkTrace) done(size int64) {
	if t == nil {
		return
	}
	t.st.chunks.Add(1)
	t.st.bytes.Add(size)
	el := time.Since(t.t0)
	log.Printf("[stream] %s 块%d [%d-%d] %s 首字节 %s 耗时 %s %s %s%s",
		t.st.label, t.idx, t.start, t.end, humanBytes(size),
		t.tHdr.Sub(t.t0).Round(time.Millisecond), el.Round(time.Millisecond),
		humanRate(size, el), connWord(t.reused), tryNote(t.attempt))
}

func (t *chunkTrace) fail(err error) {
	if t == nil {
		return
	}
	log.Printf("[stream] %s 块%d [%d-%d] 失败 耗时 %s HTTP %d: %v%s",
		t.st.label, t.idx, t.start, t.end,
		time.Since(t.t0).Round(time.Millisecond), t.code, err, tryNote(t.attempt))
}

func connWord(reused bool) string {
	if reused {
		return "连接复用"
	}
	return "新建连接"
}

func tryNote(attempt int) string {
	if attempt <= 1 {
		return ""
	}
	return fmt.Sprintf(" 第%d次尝试", attempt)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGT"[exp])
}

func humanRate(n int64, d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return humanBytes(int64(float64(n)/d.Seconds())) + "/s"
}
