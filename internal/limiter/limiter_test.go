package limiter

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestUnlimitedNoWait(t *testing.T) {
	l := New()
	start := time.Now()
	for i := 0; i < 100; i++ {
		if err := l.WaitN(context.Background(), 1<<20); err != nil {
			t.Fatalf("WaitN: %v", err)
		}
	}
	if el := time.Since(start); el > 100*time.Millisecond {
		t.Fatalf("不限速时不应阻塞，耗时 %v", el)
	}
}

func TestRateApprox(t *testing.T) {
	l := New()
	l.SetKBps(1024) // 1 MiB/s，起手带 1s 突发配额
	start := time.Now()
	// 消耗 2 MiB：1 MiB 吃突发，剩余 1 MiB 需 ≈1s
	for i := 0; i < 32; i++ {
		if err := l.WaitN(context.Background(), 64*1024); err != nil {
			t.Fatalf("WaitN: %v", err)
		}
	}
	el := time.Since(start)
	if el < 700*time.Millisecond || el > 2*time.Second {
		t.Fatalf("2MiB @1MiB/s 预期约 1s，实际 %v", el)
	}
}

func TestCancelUnblocks(t *testing.T) {
	l := New()
	l.SetKBps(1) // 1 KB/s；先精确吃掉 1s 突发配额，不触发等待
	if err := l.WaitN(context.Background(), 1024); err != nil {
		t.Fatalf("吃突发配额不应报错: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.WaitN(ctx, 8*1024) }() // 需睡约 8s，取消应立即打断
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("期望 context.Canceled，得到 %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消后 WaitN 未及时返回")
	}
}

func TestReaderWriterPassData(t *testing.T) {
	l := New()
	l.SetKBps(10240) // 足够快，只验数据完整
	src := strings.Repeat("数据x", 50000)
	var buf bytes.Buffer
	w := l.Writer(context.Background(), &buf)
	r := l.Reader(context.Background(), strings.NewReader(src))
	if _, err := io.Copy(w, r); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if buf.String() != src {
		t.Fatal("经限速 Reader/Writer 后数据不一致")
	}
	// 运行时改为不限速仍工作
	l.SetKBps(0)
	if l.Limited() {
		t.Fatal("SetKBps(0) 后应为不限速")
	}
}
