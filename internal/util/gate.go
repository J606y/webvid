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
//
// 闸上分两档优先级：AcquirePriority 是用户正在等的前台活，Acquire 是后台活。
// 名额总数不变，插队只改变谁先拿到。
type Gate struct {
	mu sync.Mutex
	ch chan struct{}

	// hand 是交棒通道：归还名额时先问一句「有前台在等吗」，有就直接交过去、不放回
	// 管子——否则后台那几件活会在同一瞬间把刚空出的位置抢回去，前台要一直等到后台
	// 自己跑完（一次 ffprobe 最长 45 秒），点开视频就是在这里干等。
	// 传的是名额所在的那根管子：SetLimit 换管后接棒者仍还给原来那根，语义同上。
	hand chan chan struct{}
}

// NewGate 建闸，n <= 0 视作 1。
func NewGate(n int) *Gate {
	g := &Gate{hand: make(chan chan struct{})}
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

// Acquire 取一个名额（后台优先级：排在前台等待者之后），取到返回归还函数；
// ctx 取消则返回其错误（此时 release 为 nil）。
// 调用方拿到 release 后必须 defer 调用，否则名额永久泄漏。
func (g *Gate) Acquire(ctx context.Context) (release func(), err error) {
	ch := g.pipe()
	select {
	case ch <- struct{}{}:
		return g.releaser(ch), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// AcquirePriority 取一个名额，用于用户正在等的前台活：除了照常等管子放行，还接
// 在跑的那件活归还时的交棒，因此不会被后台的排队者挤到后面。
// 最坏仍要等手头那件后台活跑完——插队换不来抢占，只保证一空出来就轮到前台。
func (g *Gate) AcquirePriority(ctx context.Context) (release func(), err error) {
	ch := g.pipe()
	select { // 有空位直接进，不必惊动交接
	case ch <- struct{}{}:
		return g.releaser(ch), nil
	default:
	}
	select {
	case ch <- struct{}{}:
		return g.releaser(ch), nil
	case handed := <-g.hand:
		return g.releaser(handed), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *Gate) pipe() chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ch
}

// releaser 生成归还函数（幂等）：先试着把名额直接交给等待中的前台，没人等才放回管子。
// hand 无缓冲，送得出去就说明对方已经阻塞在接收上，select 一旦成交即完成移交，
// 名额不会在半路丢掉。零值 Gate 的 hand 为 nil，两条路径各自退化为原行为。
func (g *Gate) releaser(ch chan struct{}) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			select {
			case g.hand <- ch:
			default:
				<-ch
			}
		})
	}
}
