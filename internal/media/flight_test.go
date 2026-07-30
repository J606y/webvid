package media

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestDecideSingleflight 同一文件的并发探测只跑一趟 ffprobe。
//
// 详情卡与播放页会同时来问、用户连点两下也会，各探各的就是两个 ffprobe 各占一个闸位，
// 而闸总共才两个 —— 排队直接翻倍。
//
// 手法：把闸压到 1 并自己占住，领跑者必然卡在闸上，此刻在途表里应当只有一条。
func TestDecideSingleflight(t *testing.T) {
	svc, u := newSvc(t, samples(t))
	ctx := context.Background()
	p := "/vid/long.mkv"

	fi, err := svc.fs.Get(ctx, u, p)
	if err != nil {
		t.Fatalf("取文件信息: %v", err)
	}

	svc.jobs.SetLimit(1)
	hold, err := svc.jobs.Acquire(ctx) // 占住唯一名额
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Decide(ctx, u, p, fi); err != nil {
				t.Errorf("Decide: %v", err)
			}
		}()
	}

	// 等它们各自就位：一个卡在闸上，其余挂在在途表上
	deadline := time.Now().Add(5 * time.Second)
	for {
		svc.mu.Lock()
		n := len(svc.flight)
		svc.mu.Unlock()
		if n > 1 {
			t.Fatalf("同一文件的并发探测应只有 1 次在途，实际 %d", n)
		}
		if n == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	hold()
	wg.Wait()

	// 收工后在途表必须清空，否则后续同 key 的探测会永远等一个不会来的结果
	svc.mu.Lock()
	left := len(svc.flight)
	svc.mu.Unlock()
	if left != 0 {
		t.Fatalf("探测结束后在途表应清空，实际残留 %d 条", left)
	}
}
