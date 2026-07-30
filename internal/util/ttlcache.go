package util

import (
	"container/list"
	"sync"
	"time"
)

// TTLCache 是云盘驱动用的路径 / 直链缓存：TTL 过期 + LRU 容量上限。
//
// 早先各驱动都是一张裸 map：判过期只当没命中、并不删除，写入侧又是每列一次目录就把
// 全部子项塞进去。一次全量索引会 BFS 整个挂载，等于把云盘上每一条路径都留在内存里，
// 只有写操作触发的整表清空才放得掉——进程常驻内存于是随文件数线性长。
//
// 正条目（解析到了）与负条目（已确认不存在）分开限额：查一次不存在的路径就写一条，
// 上传前的探测、播放前的 Stat 都在这条路径上，混在一起算会把有用的正条目挤光。
// 两类各自一条 LRU 链，淘汰恒为 O(1)。
//
// 容量 <= 0 表示该类不缓存（写入即丢）。零值不可用，须经 NewTTLCache 构造。
type TTLCache[V any] struct {
	mu   sync.Mutex
	ttl  time.Duration
	caps [2]int // 下标 0 = 正条目，1 = 负条目
	now  func() time.Time
	lru  [2]*list.List // 队头 = 最近用过
	idx  map[string]*list.Element
}

type cacheNode[V any] struct {
	key  string
	val  V
	kind int // 0 = 正，1 = 负；同时是 caps / lru 的下标
	at   time.Time
}

// NewTTLCache 建缓存。capPos / capNeg 分别是正、负条目的条数上限。
func NewTTLCache[V any](ttl time.Duration, capPos, capNeg int) *TTLCache[V] {
	return &TTLCache[V]{
		ttl:  ttl,
		caps: [2]int{capPos, capNeg},
		now:  time.Now,
		lru:  [2]*list.List{list.New(), list.New()},
		idx:  map[string]*list.Element{},
	}
}

// SetNow 注入时钟（测试用）。须在投入使用前调用。
func (c *TTLCache[V]) SetNow(fn func() time.Time) {
	if fn == nil {
		return
	}
	c.mu.Lock()
	c.now = fn
	c.mu.Unlock()
}

// Get 取一条。missing 表示该条目记的是「已确认不存在」，此时 v 为零值。
// 过期条目当场删除，不留给 LRU 慢慢淘汰。
func (c *TTLCache[V]) Get(key string) (v V, missing, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, hit := c.idx[key]
	if !hit {
		return v, false, false
	}
	n := el.Value.(*cacheNode[V])
	if c.now().Sub(n.at) > c.ttl {
		c.removeLocked(el)
		return v, false, false
	}
	c.lru[n.kind].MoveToFront(el)
	return n.val, n.kind == 1, true
}

// Put 记一条解析结果。
func (c *TTLCache[V]) Put(key string, v V) { c.put(key, v, 0) }

// PutMissing 记一条「这个路径不存在」。
// 只缓存找得到的路径时，每查一次不存在的路径都要把父目录整个重列一遍。
func (c *TTLCache[V]) PutMissing(key string) {
	var zero V
	c.put(key, zero, 1)
}

func (c *TTLCache[V]) put(key string, v V, kind int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.idx[key]; ok {
		c.removeLocked(el) // 可能从正翻负（或反过来），换链重挂
	}
	if c.caps[kind] <= 0 {
		return
	}
	c.idx[key] = c.lru[kind].PushFront(&cacheNode[V]{key: key, val: v, kind: kind, at: c.now()})
	for c.lru[kind].Len() > c.caps[kind] {
		c.removeLocked(c.lru[kind].Back())
	}
}

// Delete 删掉一条（直链被确认失效时用）。
func (c *TTLCache[V]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.idx[key]; ok {
		c.removeLocked(el)
	}
}

// Clear 清空。写操作让路径由无变有 / 由有变无时调用。
func (c *TTLCache[V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru[0].Init()
	c.lru[1].Init()
	c.idx = map[string]*list.Element{}
}

// Len 当前条目数（正 + 负）。
func (c *TTLCache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.idx)
}

func (c *TTLCache[V]) removeLocked(el *list.Element) {
	if el == nil {
		return
	}
	n := el.Value.(*cacheNode[V])
	c.lru[n.kind].Remove(el)
	delete(c.idx, n.key)
}
