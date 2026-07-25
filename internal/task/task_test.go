package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"newlist/internal/model"
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
	// 终态无在途文件，CurFile 随之清空
	if snap.CurFile != "" || snap.Active != 0 {
		t.Fatalf("终态在途应清空: CurFile=%q Active=%d", snap.CurFile, snap.Active)
	}
}

// plan 建一份清单（下标即上报用的 i）。
func plan(tk *Task, paths ...string) {
	items := make([]model.TransferFile, len(paths))
	for i, p := range paths {
		items[i] = model.TransferFile{Path: p, Size: 10}
	}
	tk.SetFiles(items)
}

// TestFileTrackingStable 锁住展示语义：并发在途时取最早开始的文件，
// 后开始的顶不掉它，只有它自己完成才前进——抽屉里的文件名因此不会跳。
func TestFileTrackingStable(t *testing.T) {
	tk := &Task{}
	plan(tk, "剧集/a.mp4", "剧集/b.mp4", "剧集/c.mp4")
	tk.FileStart(0)
	tk.FileStart(1)
	tk.FileStart(2)
	// 展示只给文件名，不给整条子路径——抽屉窄，路径会被截断成看不出是哪个文件
	if s := tk.snapshot(); s.CurFile != "a.mp4" || s.Active != 3 {
		t.Fatalf("三个在途应展示最早的 a.mp4/3，实际 %q/%d", s.CurFile, s.Active)
	}
	tk.FileDone(1, nil) // 后开始的先完成，展示不受影响
	if s := tk.snapshot(); s.CurFile != "a.mp4" || s.Active != 2 {
		t.Fatalf("b 完成后应仍展示 a.mp4/2，实际 %q/%d", s.CurFile, s.Active)
	}
	tk.FileDone(0, nil) // 展示的那个完成了，才前进到剩下最早的
	if s := tk.snapshot(); s.CurFile != "c.mp4" || s.Active != 1 {
		t.Fatalf("a 完成后应前进到 c.mp4/1，实际 %q/%d", s.CurFile, s.Active)
	}
	tk.FileDone(2, nil)
	if s := tk.snapshot(); s.CurFile != "" || s.Active != 0 {
		t.Fatalf("全部完成应清空，实际 %q/%d", s.CurFile, s.Active)
	}
	tk.FileDone(99, nil)   // 越界下标不应 panic
	tk.FileStart(-1)       // 同上
	tk.SetFile("only.mp4") // 单文件语义（离线下载）
	if s := tk.snapshot(); s.CurFile != "only.mp4" || s.Active != 1 {
		t.Fatalf("SetFile 应置为唯一在途，实际 %q/%d", s.CurFile, s.Active)
	}
}

// TestFileStates 锁住清单的状态与计数：完成/跳过/失败/取消各自归位，计数增量维护要对得上。
func TestFileStates(t *testing.T) {
	tk := &Task{}
	plan(tk, "a.mkv", "b.mkv", "c.mkv", "d.mkv")
	if c := tk.snapshot().Files; c.Total != 4 || c.Pending != 4 {
		t.Fatalf("规划后应 4 项全等待，实际 %+v", c)
	}
	tk.FileStart(0)
	tk.AddFile(0, 4)
	tk.FileDone(0, nil)
	tk.AddFile(1, 10)
	tk.FileSkip(1)
	tk.FileStart(2)
	tk.FileDone(2, errors.New("boom"))
	tk.FileStart(3)
	tk.FileDone(3, context.Canceled)

	c := tk.snapshot().Files
	if c.Done != 1 || c.Skipped != 1 || c.Error != 1 || c.Pending != 1 || c.Running != 0 {
		t.Fatalf("四种结局计数不符: %+v", c)
	}
	page, err := (&Manager{tasks: map[string]*Task{"x": tk}}).Files("x", 0, true, FilesQuery{})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(page.Items) != 4 || page.Total != 4 {
		t.Fatalf("应取回 4 项，实际 %d/%d", len(page.Items), page.Total)
	}
	// 完成的按整个文件大小记满；失败的留人话原因；取消的回落等待且进度作废
	if f := page.Items[0]; f.State != FileDone || f.Done != f.Size {
		t.Fatalf("完成项不符: %+v", f)
	}
	if f := page.Items[1]; f.State != FileSkipped || f.Done != f.Size {
		t.Fatalf("跳过项应记满进度: %+v", f)
	}
	if f := page.Items[2]; f.State != FileError || !strings.Contains(f.Err, "boom") {
		t.Fatalf("失败项应带原因: %+v", f)
	}
	if f := page.Items[3]; f.State != FilePending || f.Done != 0 {
		t.Fatalf("取消项应回落等待且进度归零: %+v", f)
	}
}

// TestFilesQuery：按状态筛、按路径搜、分页——几万条清单只按需给一页。
func TestFilesQuery(t *testing.T) {
	tk := &Task{}
	plan(tk, "剧集/S01E01.mkv", "剧集/S01E02.mkv", "花絮/预告.mp4", "封面.jpg")
	tk.FileStart(0)
	tk.FileDone(0, nil)
	m := &Manager{tasks: map[string]*Task{"x": tk}}

	page, _ := m.Files("x", 0, true, FilesQuery{State: FilePending})
	if page.Total != 3 || page.Counts.Total != 4 {
		t.Fatalf("状态筛选应剩 3 项、计数仍为全量 4: total=%d counts=%+v", page.Total, page.Counts)
	}
	page, _ = m.Files("x", 0, true, FilesQuery{Q: "s01e"})
	if page.Total != 2 {
		t.Fatalf("路径搜索应命中 2 项（不区分大小写），实际 %d", page.Total)
	}
	page, _ = m.Files("x", 0, true, FilesQuery{Offset: 1, Limit: 2})
	if len(page.Items) != 2 || page.Total != 4 || page.Items[0].Path != "剧集/S01E02.mkv" {
		t.Fatalf("分页不符: items=%d total=%d first=%q", len(page.Items), page.Total, page.Items[0].Path)
	}
	if _, err := m.Files("x", 9, false, FilesQuery{}); !errors.Is(err, ErrForbidden) {
		t.Fatal("非所有者且非 admin 应被拒")
	}
}

func TestError(t *testing.T) {
	m := New(1)
	tk := m.Submit(1, "失败任务", func(ctx context.Context, t *Task) error {
		return errors.New("boom")
	})
	// 任务失败原因经 util.Humanize 转成人话后再存：纯英文错误包一层「操作失败：」，
	// 原始错误只进服务端日志（前端任务列表不再出现裸英文）。
	snap := waitState(t, m, tk.ID, StateError)
	if !strings.Contains(snap.Err, "boom") || !strings.Contains(snap.Err, "操作失败") {
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
