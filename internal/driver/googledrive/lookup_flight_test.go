package googledrive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newlist/internal/driver"
)

// TestLookupSingleflight 同一路径的并发解析只翻一次页。
//
// 现实里这是常态：一屏封面、预载 worker 和播放请求会在同一瞬间打到同一个目录上。
// 没有单飞时，每个调用各自把每一级父目录完整翻一遍（pageSize=1000 顺序翻页），
// 目录一大就是成百上千次列举，还全都撞在 Google 的限流上。
func TestLookupSingleflight(t *testing.T) {
	var listCalls atomic.Int64
	folder := func(id, name string) map[string]any {
		return map[string]any{"id": id, "name": name, "mimeType": folderMime}
	}
	children := map[string][]map[string]any{
		"ROOT": {folder("F1", "电影")},
		"F1":   {folder("F2", "2024")},
		"F2":   {map[string]any{"id": "X", "name": "a.mp4", "mimeType": "video/mp4", "size": "123"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
		case r.URL.Path == "/files/root":
			json.NewEncoder(w).Encode(map[string]any{"id": "ROOT", "mimeType": folderMime})
		case r.URL.Path == "/files":
			listCalls.Add(1)
			time.Sleep(50 * time.Millisecond) // 拉长在途窗口，确保并发调用真的重叠
			q := r.URL.Query().Get("q")
			parent := ""
			if i := strings.Index(q, "' in parents"); i > 0 {
				parent = q[1:i]
			}
			json.NewEncoder(w).Encode(map[string]any{"files": children[parent]})
		default:
			http.NotFound(w, r)
		}
	}))
	oldAPI, oldTok := driveAPIBase, tokenURL
	driveAPIBase, tokenURL = srv.URL, srv.URL+"/token"
	defer func() { driveAPIBase, tokenURL = oldAPI, oldTok; srv.Close() }()

	d := &GDrive{}
	if err := d.Init(context.Background(), driver.Config{
		"client_id": "c", "client_secret": "s", "refresh_token": "r",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	listCalls.Store(0) // Init 自己的根探测不算

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := d.lookup(context.Background(), "电影/2024/a.mp4")
			if err != nil || f.id != "X" {
				t.Errorf("解析失败: %+v err=%v", f, err)
			}
		}()
	}
	wg.Wait()

	// 一次完整解析要列三层：ROOT → 电影 → 2024。并发多少个都不该超过这个数。
	if got := listCalls.Load(); got > 3 {
		t.Fatalf("%d 个并发解析只该列 3 次目录，实际列了 %d 次", n, got)
	}
}
