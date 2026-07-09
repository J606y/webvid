package task

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// waitState 轮询等待任务进入期望状态，超时报错。
func waitState(t *testing.T, m *Manager, id string, want State) *Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if snap, ok := m.Get(id); ok && snap.State == want {
			return snap
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap, _ := m.Get(id)
	t.Fatalf("等待状态 %s 超时，当前 %+v", want, snap)
	return nil
}

func TestSubmitDone(t *testing.T) {
	m := New(2)
	tk := m.Submit(1, "复制 a.txt", func(ctx context.Context, t *Task) error {
		t.SetTotal(100)
		t.SetFile("a.txt")
		t.Add(60)
		t.Add(40)
		return nil
	})
	snap := waitState(t, m, tk.ID, StateDone)
	if snap.Total != 100 || snap.Done != 100 {
		t.Fatalf("Done/Total 不符: %+v", snap)
	}
	if snap.CurFile != "a.txt" {
		t.Fatalf("CurFile 不符: %q", snap.CurFile)
	}
}

func TestError(t *testing.T) {
	m := New(1)
	tk := m.Submit(1, "失败任务", func(ctx context.Context, t *Task) error {
		return errors.New("boom")
	})
	snap := waitState(t, m, tk.ID, StateError)
	if snap.Err != "boom" {
		t.Fatalf("Err 不符: %q", snap.Err)
	}
}

func TestCancelRunning(t *testing.T) {
	m := New(1)
	started := make(chan struct{})
	tk := m.Submit(1, "长任务", func(ctx context.Context, t *Task) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	<-started
	if err := m.Cancel(tk.ID, 1, false); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitState(t, m, tk.ID, StateCanceled)
}

func TestCancelPending(t *testing.T) {
	m := New(1)
	block := make(chan struct{})
	started := make(chan struct{})
	m.Submit(1, "占住 worker", func(ctx context.Context, t *Task) error {
		close(started)
		<-block
		return nil
	})
	<-started
	tk := m.Submit(1, "排队中", func(ctx context.Context, t *Task) error { return nil })
	if err := m.Cancel(tk.ID, 1, false); err != nil {
		t.Fatalf("Cancel pending: %v", err)
	}
	close(block)
	snap := waitState(t, m, tk.ID, StateCanceled)
	if snap.State != StateCanceled {
		t.Fatalf("pending 取消失败: %+v", snap)
	}
}

func TestCancelPermission(t *testing.T) {
	m := New(1)
	block := make(chan struct{})
	defer close(block)
	tk := m.Submit(7, "别人的任务", func(ctx context.Context, t *Task) error {
		<-block
		return nil
	})
	if err := m.Cancel(tk.ID, 8, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("期望 ErrForbidden，得到 %v", err)
	}
	if err := m.Cancel("no-such-id", 8, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，得到 %v", err)
	}
}

func TestRetry(t *testing.T) {
	m := New(1)
	var attempts int
	var mu sync.Mutex
	tk := m.Submit(1, "先败后成", func(ctx context.Context, t *Task) error {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n == 1 {
			return errors.New("第一次失败")
		}
		t.SetTotal(10)
		t.Add(10)
		return nil
	})
	waitState(t, m, tk.ID, StateError)
	if err := m.Retry(tk.ID, 1, false); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	snap := waitState(t, m, tk.ID, StateDone)
	if snap.Err != "" || snap.Done != 10 {
		t.Fatalf("重试后状态不符: %+v", snap)
	}
	// done 状态不可再重试
	if err := m.Retry(tk.ID, 1, false); !errors.Is(err, ErrBadState) {
		t.Fatalf("期望 ErrBadState，得到 %v", err)
	}
}

func TestListOwnerFilter(t *testing.T) {
	m := New(2)
	a := m.Submit(1, "u1 的任务", func(ctx context.Context, t *Task) error { return nil })
	b := m.Submit(2, "u2 的任务", func(ctx context.Context, t *Task) error { return nil })
	waitState(t, m, a.ID, StateDone)
	waitState(t, m, b.ID, StateDone)

	if got := m.List(1, false); len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("owner 过滤失败: %+v", got)
	}
	if got := m.List(0, true); len(got) != 2 {
		t.Fatalf("admin 应看到全部: %d", len(got))
	}
}

func TestClearDone(t *testing.T) {
	m := New(2)
	a := m.Submit(1, "u1 done", func(ctx context.Context, t *Task) error { return nil })
	b := m.Submit(2, "u2 done", func(ctx context.Context, t *Task) error { return nil })
	e := m.Submit(1, "u1 error", func(ctx context.Context, t *Task) error { return errors.New("boom") })
	waitState(t, m, a.ID, StateDone)
	waitState(t, m, b.ID, StateDone)
	waitState(t, m, e.ID, StateError)
	block := make(chan struct{})
	started := make(chan struct{})
	c := m.Submit(1, "u1 running", func(ctx context.Context, t *Task) error {
		close(started)
		<-block
		return nil
	})
	<-started

	m.ClearDone(1, false)
	if _, ok := m.Get(a.ID); ok {
		t.Fatal("u1 的已成功任务应被清除")
	}
	if _, ok := m.Get(b.ID); !ok {
		t.Fatal("u2 的任务不应被 u1 清除")
	}
	if _, ok := m.Get(e.ID); !ok {
		t.Fatal("失败任务应保留（便于重试），不应被「清除已成功」删除")
	}
	if _, ok := m.Get(c.ID); !ok {
		t.Fatal("运行中任务不应被清除")
	}
	close(block)
	waitState(t, m, c.ID, StateDone)
}

func TestRemove(t *testing.T) {
	m := New(1)
	// 终态任务（此处失败态）可单条删除
	e := m.Submit(1, "失败", func(ctx context.Context, t *Task) error { return errors.New("boom") })
	waitState(t, m, e.ID, StateError)
	if err := m.Remove(e.ID, 1, false); err != nil {
		t.Fatalf("Remove 失败任务: %v", err)
	}
	if _, ok := m.Get(e.ID); ok {
		t.Fatal("删除后不应仍存在")
	}
	if err := m.Remove(e.ID, 1, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应 ErrNotFound，得到 %v", err)
	}
	// 运行中任务不可删除（须先取消），且校验权限
	block := make(chan struct{})
	started := make(chan struct{})
	r := m.Submit(1, "运行中", func(ctx context.Context, t *Task) error {
		close(started)
		<-block
		return nil
	})
	<-started
	if err := m.Remove(r.ID, 2, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("他人删除应 ErrForbidden，得到 %v", err)
	}
	if err := m.Remove(r.ID, 1, false); !errors.Is(err, ErrBadState) {
		t.Fatalf("运行中删除应 ErrBadState，得到 %v", err)
	}
	close(block)
	waitState(t, m, r.ID, StateDone)
}

func TestConcurrentSubmit(t *testing.T) {
	m := New(4)
	var wg sync.WaitGroup
	ids := make([]string, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tk := m.Submit(int64(i%3), fmt.Sprintf("任务%d", i), func(ctx context.Context, t *Task) error {
				t.SetTotal(4)
				for j := 0; j < 4; j++ {
					t.Add(1)
				}
				return nil
			})
			ids[i] = tk.ID
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		waitState(t, m, id, StateDone)
	}
	if got := m.List(0, true); len(got) != 50 {
		t.Fatalf("应有 50 个任务，得到 %d", len(got))
	}
}

func TestGroupsIsolated(t *testing.T) {
	m := New(1)
	block := make(chan struct{})
	started := make(chan struct{})
	// 占满 copy 组唯一 worker
	m.Submit(1, "占住 copy", func(ctx context.Context, t *Task) error {
		close(started)
		<-block
		return nil
	})
	<-started
	defer close(block)
	// offline 组独立队列，不受 copy 组占用影响
	tk := m.SubmitIn(GroupOffline, 1, "离线任务", func(ctx context.Context, t *Task) error { return nil })
	snap := waitState(t, m, tk.ID, StateDone)
	if snap.Group != GroupOffline {
		t.Fatalf("Group 不符: %q", snap.Group)
	}
}

func TestSetWorkers(t *testing.T) {
	m := New(1)
	if got := m.Workers(GroupCopy); got != 1 {
		t.Fatalf("初始 copy workers 应为 1，得到 %d", got)
	}
	// 扩容后两个长任务可并行
	m.SetWorkers(GroupCopy, 2)
	if got := m.Workers(GroupCopy); got != 2 {
		t.Fatalf("扩容后应为 2，得到 %d", got)
	}
	block := make(chan struct{})
	var running sync.WaitGroup
	running.Add(2)
	for i := 0; i < 2; i++ {
		m.Submit(1, fmt.Sprintf("并行%d", i), func(ctx context.Context, t *Task) error {
			running.Done()
			<-block
			return nil
		})
	}
	running.Wait() // 两个都进入运行 = 确有 2 个 worker
	// 运行中收缩：不打断在跑任务，收缩后仍能消费新任务
	m.SetWorkers(GroupCopy, 1)
	if got := m.Workers(GroupCopy); got != 1 {
		t.Fatalf("收缩后应为 1，得到 %d", got)
	}
	close(block)
	tk := m.Submit(1, "收缩后新任务", func(ctx context.Context, t *Task) error { return nil })
	waitState(t, m, tk.ID, StateDone)
	// 钳位
	m.SetWorkers(GroupOffline, 0)
	if got := m.Workers(GroupOffline); got != 1 {
		t.Fatalf("0 应钳到 1，得到 %d", got)
	}
	m.SetWorkers(GroupOffline, 999)
	if got := m.Workers(GroupOffline); got != maxWorkers {
		t.Fatalf("999 应钳到 %d，得到 %d", maxWorkers, got)
	}
}

func TestSpeedCalc(t *testing.T) {
	m := New(1)
	tk := m.Submit(1, "测速", func(ctx context.Context, t *Task) error {
		t.SetTotal(2000)
		t.Add(1000)
		time.Sleep(600 * time.Millisecond)
		t.Add(1000)
		// 此刻速度应已按 Δbytes/Δt 计算（≈1000/0.6s），完成前读一次
		return nil
	})
	// 等完成后 Speed 归零，只验证不 panic 且 done
	waitState(t, m, tk.ID, StateDone)
}
