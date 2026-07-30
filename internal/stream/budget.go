package stream

import (
	"math"
	"runtime/debug"
	"sync"
)

// 全局预读预算：所有并发流的滑动窗口加起来的字节上限。
//
// 单条流的窗口已由预读配置钳住（见 Opts.window），但同时在跑的流数没有任何闸——
// 一边看视频一边转存，每条流、每个转存 worker 各占一份预读缓冲，加起来就是整台机器。
//
// 预算不够时新流退化为最小窗口（一块），而不是排队等预算：一条读不到数据的流对用户
// 就是"卡住了"，宁可让它慢，也不能让它停。
const (
	defaultMemBudget = 1 << 30 // 未设 GOMEMLIMIT 时的默认预算：1GiB
	memBudgetShare   = 4       // 设了 GOMEMLIMIT 时，预读缓冲最多占其 1/4
)

// memBudget 当前预算字节数。var 以便测试调小。
var memBudget = initialMemBudget()

// initialMemBudget 取部署侧设的 GOMEMLIMIT 为准（没设就是默认值）：
// 内存限制多少由部署的人说了算，这里只按比例分一块给预读缓冲，不自作主张。
func initialMemBudget() int64 {
	// 负数只读不改，是 runtime 给出的读取当前限制的方式；未设时为 MaxInt64。
	if lim := debug.SetMemoryLimit(-1); lim < math.MaxInt64 {
		return lim / memBudgetShare
	}
	return defaultMemBudget
}

var (
	budgetMu   sync.Mutex
	budgetUsed int64
)

// reserveWindow 为一条新流申请 want 块（每块 chunk 字节）窗口，返回实际获批的块数。
// 至少给一块：一块都不给的流一个字节也吐不出来。
func reserveWindow(want int, chunk int64) int {
	if want < 1 {
		want = 1
	}
	budgetMu.Lock()
	defer budgetMu.Unlock()
	free := (memBudget - budgetUsed) / chunk
	if free < 1 {
		free = 1
	}
	if int64(want) > free {
		want = int(free)
	}
	budgetUsed += int64(want) * chunk
	return want
}

// releaseWindow 归还预算，与 reserveWindow 一一对应（由 MultiReader.Close 调用，
// Close 自身幂等，故不会重复归还）。
func releaseWindow(n int, chunk int64) {
	budgetMu.Lock()
	budgetUsed -= int64(n) * chunk
	if budgetUsed < 0 {
		budgetUsed = 0
	}
	budgetMu.Unlock()
}
