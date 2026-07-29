package util

import (
	"context"
	"sync"
)

// Gate 是一个容量可热调的并发闸：拿到名额才能开工，放回后下一个才进得来。
//
// 用在 ffmpeg / ffprobe 这类重活上——它们既吃 CPU 又拉网络，不设闸的话一屏封面
// 就能把机器榨干（用户反馈：新建索引期间 CPU 跑满）。闸值约等于愿意让出多少个核，
// 前提是每个进程都按 -threads 1 跑。
//
// SetLimit 直接换一根新管子：已经在跑的那几件仍把名额还给旧管子（各自 Acquire 时
// 捕获的那根），因此调小闸值的瞬间可能短暂多跑几件，等手头的做完就收敛到新值。
// 这个取舍换来的是全程无锁竞争、无「等待者被永久挂起」的风险。
type Gate struct {
	mu sync.Mutex
	ch chan struct{}
}

// NewGate 建闸，n <= 0 视作 1。
func NewGate(n int) *Gate {
	g := &Gate{}
	g.SetLimit(n)
	return g
}

// SetLimit 调整同时可开工的数量，n <= 0 视作 1。
func (g *Gate) SetLimit(n int) {
	if n <= 0 {
		n = 1
	}
	g.mu.Lock()
	g.ch = make(chan struct{}, n)
	g.mu.Unlock()
}

// Acquire 取一个名额，取到返回归还函数；ctx 取消则返回其错误（此时 release 为 nil）。
// 调用方拿到 release 后必须 defer 调用，否则名额永久泄漏。
func (g *Gate) Acquire(ctx context.Context) (release func(), err error) {
	g.mu.Lock()
	ch := g.ch
	g.mu.Unlock()
	select {
	case ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-ch }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
