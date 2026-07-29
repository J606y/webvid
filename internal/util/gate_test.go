package util

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestGateLimits 闸值即同时开工上限；超出的排队等名额。
func TestGateLimits(t *testing.T) {
	g := NewGate(2)
	var now, peak int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := g.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			defer rel()
			if n := atomic.AddInt32(&now, 1); n > atomic.LoadInt32(&peak) {
				atomic.StoreInt32(&peak, n)
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&now, -1)
		}()
	}
	wg.Wait()
	if peak > 2 {
		t.Fatalf("同时开工数应 ≤2, got %d", peak)
	}
}

// TestGateCancel 闸满时 ctx 取消要能脱身，不能把调用方永久挂住。
func TestGateCancel(t *testing.T) {
	g := NewGate(1)
	rel, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(ctx); err == nil {
		t.Fatal("闸满且 ctx 到期时应返回错误")
	}
}

// TestGateRelease 归还幂等：重复调 release 不会多放名额出来。
func TestGateRelease(t *testing.T) {
	g := NewGate(1)
	rel, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rel()
	rel()
	rel()
	if _, err := g.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(ctx); err == nil {
		t.Fatal("重复归还不该把闸值放大")
	}
}
