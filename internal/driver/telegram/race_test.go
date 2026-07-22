package telegram

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestDropConnRace 用 -race 守护 d.conn 的读写纪律：并发的 Drop（置空）与
// currentConn（读取，snapshot/message/getFile 的公共入口）必须全程互斥。
// 回归此前 snapshot 在锁外读 d.conn.client.API() 与 Drop 无锁置 nil 的数据竞争 + nil 解引用。
func TestDropConnRace(t *testing.T) {
	d := &Telegram{now: time.Now, cacheTTL: time.Minute}
	// 空连接：close() 只遍历空池、stop 为 nil，无网络、无副作用。
	setConn := func() {
		d.mu.Lock()
		d.conn = &conn{pools: map[int]dcPool{}}
		d.mu.Unlock()
	}
	setConn()

	var wg sync.WaitGroup
	// 多个读者不停经 currentConn 取连接。
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				if _, err := d.currentConn(); err != nil && !errors.Is(err, ErrNotLoggedIn) {
					t.Errorf("currentConn 意外错误: %v", err)
					return
				}
			}
		}()
	}
	// 一个写者反复 Drop 后重建连接。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 1000; j++ {
			_ = d.Drop()
			setConn()
		}
	}()
	wg.Wait()
}
