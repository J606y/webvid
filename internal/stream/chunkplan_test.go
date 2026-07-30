package stream

import "testing"

// walk 把整个计划走一遍，返回各块大小，并校验首尾相接、总和等于 length。
func walk(t *testing.T, p chunkPlan) []int64 {
	t.Helper()
	var sizes []int64
	var want int64
	for i := 0; i < p.n; i++ {
		start, size := p.at(i)
		if start != want {
			t.Fatalf("第 %d 块起点 %d，期望 %d（块之间必须首尾相接，不留缝不重叠）", i, start, want)
		}
		if size <= 0 {
			t.Fatalf("第 %d 块大小 %d，idx < n 时必须 > 0", i, size)
		}
		sizes = append(sizes, size)
		want = start + size
	}
	if want != p.length {
		t.Fatalf("各块合计 %d，期望覆盖满 length=%d", want, p.length)
	}
	return sizes
}

// TestChunkPlanRamp 递增段：512K → 1M → 2M → 稳态整块。
// 这是首帧延迟的关键 —— 读端必须等一整块下满才吐字节，首块 512K 而不是 4MB。
func TestChunkPlanRamp(t *testing.T) {
	const K, M = 1 << 10, 1 << 20
	p := newChunkPlan(10*M, 4*M)
	got := walk(t, p)
	want := []int64{512 * K, 1 * M, 2 * M, 4 * M, 2*M + 512*K}
	if len(got) != len(want) {
		t.Fatalf("块数 %d，期望 %d：%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 块 %d 字节，期望 %d；全部：%v", i, got[i], want[i], got)
		}
	}
}

// TestChunkPlanSmallChunkUniform chunk <= 512KB 时不递增，保持均匀分块——
// 已经把块调小的用户，行为必须一字不变。
func TestChunkPlanSmallChunkUniform(t *testing.T) {
	const K = 1 << 10
	p := newChunkPlan(1000*K, 256*K)
	if len(p.ramp) != 0 {
		t.Fatalf("chunk=256K 不该有递增段，实际 %v", p.ramp)
	}
	got := walk(t, p)
	if len(got) != 4 {
		t.Fatalf("块数 %d，期望 4：%v", len(got), got)
	}
	for i := 0; i < 3; i++ {
		if got[i] != 256*K {
			t.Fatalf("第 %d 块 %d，期望均匀的 %d", i, got[i], 256*K)
		}
	}
	if got[3] != 232*K {
		t.Fatalf("末块 %d，期望 %d", got[3], 232*K)
	}
}

// TestChunkPlanEndsInsideRamp length 短到递增段还没走完就结束：不该补出多余的块。
func TestChunkPlanEndsInsideRamp(t *testing.T) {
	const K, M = 1 << 10, 1 << 20
	p := newChunkPlan(700*K, 4*M) // 512K + 188K
	got := walk(t, p)
	if len(got) != 2 || got[0] != 512*K || got[1] != 188*K {
		t.Fatalf("期望 [512K, 188K]，实际 %v（n=%d）", got, p.n)
	}
}

// TestChunkPlanTinyLength 比首块还小的区间只应有一块，且等于 length。
func TestChunkPlanTinyLength(t *testing.T) {
	const M = 1 << 20
	p := newChunkPlan(1000, 4*M)
	got := walk(t, p)
	if len(got) != 1 || got[0] != 1000 {
		t.Fatalf("期望 [1000]，实际 %v", got)
	}
}

// TestChunkPlanExactBoundary length 正好落在递增段某块的边界上，不该多出一个空块。
func TestChunkPlanExactBoundary(t *testing.T) {
	const K, M = 1 << 10, 1 << 20
	p := newChunkPlan(1536*K, 4*M) // 正好 512K + 1M
	got := walk(t, p)
	if len(got) != 2 || got[0] != 512*K || got[1] != 1*M {
		t.Fatalf("期望 [512K, 1M]，实际 %v（n=%d）", got, p.n)
	}
}

// TestChunkPlanBigChunkRampDepth chunk 很大时递增段一路翻倍到它为止，不越过。
func TestChunkPlanBigChunkRampDepth(t *testing.T) {
	const K, M = 1 << 10, 1 << 20
	p := newChunkPlan(1 << 30, 64*M)
	want := []int64{512 * K, 1 * M, 2 * M, 4 * M, 8 * M, 16 * M, 32 * M}
	if len(p.ramp) != len(want) {
		t.Fatalf("递增段 %v，期望 %v", p.ramp, want)
	}
	for i := range want {
		if p.ramp[i] != want[i] {
			t.Fatalf("递增段第 %d 项 %d，期望 %d", i, p.ramp[i], want[i])
		}
	}
	walk(t, p) // 顺带校验整体覆盖
}
