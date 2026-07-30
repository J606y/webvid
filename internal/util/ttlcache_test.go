package util

import (
	"strconv"
	"testing"
	"time"
)

func fixedClock() (func() time.Time, func(time.Duration)) {
	t := time.Unix(1700000000, 0)
	return func() time.Time { return t }, func(d time.Duration) { t = t.Add(d) }
}

func TestTTLCacheExpiryDeletes(t *testing.T) {
	now, advance := fixedClock()
	c := NewTTLCache[int](time.Minute, 10, 10)
	c.SetNow(now)
	c.Put("a", 1)
	c.PutMissing("b")

	if v, miss, ok := c.Get("a"); !ok || miss || v != 1 {
		t.Fatalf("Get(a) = %d,%v,%v", v, miss, ok)
	}
	if _, miss, ok := c.Get("b"); !ok || !miss {
		t.Fatalf("Get(b) 应命中负条目，得 miss=%v ok=%v", miss, ok)
	}

	advance(2 * time.Minute)
	if _, _, ok := c.Get("a"); ok {
		t.Fatal("过期条目仍命中")
	}
	if _, _, ok := c.Get("b"); ok {
		t.Fatal("过期负条目仍命中")
	}
	// 过期不是「只报 miss」，条目本身必须已经从 map 里摘掉，否则内存照旧线性长
	if n := c.Len(); n != 0 {
		t.Fatalf("过期条目未删除，Len = %d", n)
	}
}

func TestTTLCacheLRUCap(t *testing.T) {
	c := NewTTLCache[int](time.Hour, 3, 2)
	for i := 0; i < 100; i++ {
		c.Put("p"+strconv.Itoa(i), i)
		c.PutMissing("n" + strconv.Itoa(i))
	}
	if n := c.Len(); n != 5 {
		t.Fatalf("Len = %d, want 5（正 3 + 负 2）", n)
	}
	// 最近写入的三条正条目还在
	for i := 97; i < 100; i++ {
		if v, _, ok := c.Get("p" + strconv.Itoa(i)); !ok || v != i {
			t.Errorf("p%d 被淘汰了", i)
		}
	}
	if _, _, ok := c.Get("p96"); ok {
		t.Error("p96 应已被 LRU 淘汰")
	}
	// 负条目占满自己的额度，不挤占正条目
	if _, miss, ok := c.Get("n99"); !ok || !miss {
		t.Error("n99 应命中")
	}
}

// 负条目挤爆时不该把正条目带走：全量索引会把每一条查不到的路径都写进来。
func TestTTLCacheNegativeDoesNotEvictPositive(t *testing.T) {
	c := NewTTLCache[int](time.Hour, 2, 2)
	c.Put("keep1", 1)
	c.Put("keep2", 2)
	for i := 0; i < 50; i++ {
		c.PutMissing("gone" + strconv.Itoa(i))
	}
	for _, k := range []string{"keep1", "keep2"} {
		if _, miss, ok := c.Get(k); !ok || miss {
			t.Errorf("%s 被负条目挤掉了", k)
		}
	}
}

func TestTTLCacheGetRefreshesLRU(t *testing.T) {
	c := NewTTLCache[int](time.Hour, 2, 1)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Get("a")  // a 变成最近使用
	c.Put("c", 3) // 淘汰 b
	if _, _, ok := c.Get("b"); ok {
		t.Error("b 应被淘汰")
	}
	if _, _, ok := c.Get("a"); !ok {
		t.Error("a 刚用过，不该被淘汰")
	}
}

func TestTTLCacheFlipAndDelete(t *testing.T) {
	c := NewTTLCache[int](time.Hour, 4, 4)
	c.Put("x", 7)
	c.PutMissing("x") // 正翻负：不能两条链上各留一份
	if n := c.Len(); n != 1 {
		t.Fatalf("正翻负后 Len = %d, want 1", n)
	}
	if _, miss, ok := c.Get("x"); !ok || !miss {
		t.Fatalf("翻转后应是负条目，miss=%v ok=%v", miss, ok)
	}
	c.Put("x", 8) // 负翻正
	if v, miss, ok := c.Get("x"); !ok || miss || v != 8 {
		t.Fatalf("翻回正条目失败: %d,%v,%v", v, miss, ok)
	}
	c.Delete("x")
	if n := c.Len(); n != 0 {
		t.Fatalf("Delete 后 Len = %d", n)
	}
	c.Put("y", 1)
	c.Clear()
	if n := c.Len(); n != 0 {
		t.Fatalf("Clear 后 Len = %d", n)
	}
}

// 容量 <= 0 = 该类不缓存：OneDrive 直链缓存不存负条目，别让它把额度算错。
func TestTTLCacheZeroCap(t *testing.T) {
	c := NewTTLCache[int](time.Hour, 4, 0)
	c.PutMissing("n")
	if _, _, ok := c.Get("n"); ok {
		t.Error("负条目容量为 0 时不该缓存")
	}
	c.Put("p", 1)
	if _, _, ok := c.Get("p"); !ok {
		t.Error("正条目应正常缓存")
	}
}

func TestTTLCacheConcurrent(t *testing.T) {
	c := NewTTLCache[int](time.Hour, 64, 64)
	done := make(chan struct{})
	for w := 0; w < 8; w++ {
		go func(w int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 500; i++ {
				k := strconv.Itoa((w*500 + i) % 200)
				c.Put(k, i)
				c.Get(k)
				c.PutMissing("m" + k)
				if i%97 == 0 {
					c.Clear()
				}
			}
		}(w)
	}
	for w := 0; w < 8; w++ {
		<-done
	}
	if n := c.Len(); n > 128 {
		t.Fatalf("并发后 Len = %d，超过上限", n)
	}
}
