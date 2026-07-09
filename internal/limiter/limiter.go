// Package limiter 提供全站级字节速率限制：令牌桶（允许透支后补觉）+ Reader/Writer 包装。
// 同一个 Limiter 被多条流共享即为「总速率」限制；速率可运行时调整、0 为不限速。
package limiter

import (
	"context"
	"io"
	"sync"
	"time"
)

// chunk 限速时单次读/写的最大字节数：块越小节流越平滑，64KB 与常见缓冲一致。
const chunk = 64 * 1024

type Limiter struct {
	mu     sync.Mutex
	bps    int64 // 字节/秒；0=不限速
	tokens float64
	last   time.Time
}

func New() *Limiter { return &Limiter{} }

// SetKBps 设置速率（KB/s），0 为不限速；随时可调，新速率对在途流立即生效。
func (l *Limiter) SetKBps(kb int) {
	l.mu.Lock()
	l.bps = int64(kb) * 1024
	l.tokens = float64(l.bps) // 起手给 1 秒配额，避免首个请求空等
	l.last = time.Time{}
	l.mu.Unlock()
}

func (l *Limiter) KBps() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int(l.bps / 1024)
}

// Limited 报告当前是否处于限速状态。
func (l *Limiter) Limited() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bps > 0
}

// WaitN 记账 n 字节：桶里够就直接过，不够则按欠额睡到补齐；ctx 取消立即返回其错误。
// 允许单次 n 超过桶容量（透支），平均速率仍收敛到 bps。
func (l *Limiter) WaitN(ctx context.Context, n int) error {
	l.mu.Lock()
	if l.bps <= 0 {
		l.mu.Unlock()
		return nil
	}
	now := time.Now()
	if !l.last.IsZero() {
		l.tokens += now.Sub(l.last).Seconds() * float64(l.bps)
	}
	l.last = now
	if burst := float64(l.bps); l.tokens > burst { // 闲置累积上限 1 秒配额
		l.tokens = burst
	}
	l.tokens -= float64(n)
	if l.tokens >= 0 {
		l.mu.Unlock()
		return nil
	}
	wait := time.Duration(-l.tokens / float64(l.bps) * float64(time.Second))
	l.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Reader 包装 r：限速时按块读并记账阻塞；不限速时近乎零开销。
func (l *Limiter) Reader(ctx context.Context, r io.Reader) io.Reader {
	return &limitReader{ctx: ctx, r: r, l: l}
}

type limitReader struct {
	ctx context.Context
	r   io.Reader
	l   *Limiter
}

func (x *limitReader) Read(p []byte) (int, error) {
	if x.l.Limited() && len(p) > chunk {
		p = p[:chunk]
	}
	n, err := x.r.Read(p)
	if n > 0 {
		if werr := x.l.WaitN(x.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}

// Writer 包装 w：限速时分块写并记账阻塞；不限速时直写。
func (l *Limiter) Writer(ctx context.Context, w io.Writer) io.Writer {
	return &limitWriter{ctx: ctx, w: w, l: l}
}

type limitWriter struct {
	ctx context.Context
	w   io.Writer
	l   *Limiter
}

func (x *limitWriter) Write(p []byte) (int, error) {
	if !x.l.Limited() {
		return x.w.Write(p)
	}
	total := 0
	for len(p) > 0 {
		c := p
		if len(c) > chunk {
			c = p[:chunk]
		}
		if err := x.l.WaitN(x.ctx, len(c)); err != nil {
			return total, err
		}
		n, err := x.w.Write(c)
		total += n
		if err != nil {
			return total, err
		}
		p = p[n:]
	}
	return total, nil
}
