package stream

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
)

// withBudget 把全局预算改小，跑完恢复（预算与用量都是包级状态）。
func withBudget(t *testing.T, n int64) {
	t.Helper()
	oldBudget, oldUsed := memBudget, budgetUsed
	memBudget, budgetUsed = n, 0
	t.Cleanup(func() { memBudget, budgetUsed = oldBudget, oldUsed })
}

// 预算够就照给，不够就少给，用完归还——归还后下一条流又能拿满。
func TestReserveWindowBudget(t *testing.T) {
	const chunk = 1 << 20
	withBudget(t, 8*chunk)

	if got := reserveWindow(4, chunk); got != 4 {
		t.Fatalf("预算充足应给满 4 块，实际 %d", got)
	}
	if got := reserveWindow(8, chunk); got != 4 { // 只剩 4 块
		t.Fatalf("应只剩 4 块，实际 %d", got)
	}
	// 预算见底也必须给一块：一块都不给的流一个字节都吐不出来
	if got := reserveWindow(8, chunk); got != 1 {
		t.Fatalf("预算耗尽应兜底 1 块，实际 %d", got)
	}
	releaseWindow(4, chunk)
	releaseWindow(4, chunk)
	releaseWindow(1, chunk)
	if budgetUsed != 0 {
		t.Fatalf("全部归还后用量应归零，实际 %d", budgetUsed)
	}
	if got := reserveWindow(8, chunk); got != 8 {
		t.Fatalf("归还后应能再拿满 8 块，实际 %d", got)
	}
	releaseWindow(8, chunk)
}

// 预算吃紧只让窗口变小，不影响内容正确性，Close 之后预算全额归还。
func TestBudgetShrinksWindowNotContent(t *testing.T) {
	const chunk = 64 << 10
	withBudget(t, 2*chunk)

	content := pattern(16 * chunk)
	srv := &rangeSrv{content: content}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	// 要 8 块窗口，预算只够 2 块
	o := Opts{Threads: 4, ChunkBytes: chunk, ReadaheadBytes: 8 * chunk}
	mr := NewMultiReader(context.Background(), fixedProvider(ts.URL), 0, int64(len(content)), o)
	if w := mr.(*MultiReader).window; w != 2 {
		t.Fatalf("窗口应被预算压到 2 块，实际 %d", w)
	}
	if got := readAll(t, mr); !bytes.Equal(got, content) {
		t.Fatal("窗口变小后内容不一致")
	}
	if budgetUsed != 0 {
		t.Fatalf("关流后预算应全额归还，实际仍占 %d", budgetUsed)
	}
}
