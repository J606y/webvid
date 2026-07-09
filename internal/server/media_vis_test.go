package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newlist/internal/auth"
	"newlist/internal/conf"
	"newlist/internal/db"
	_ "newlist/internal/driver/local" // 注册 local 驱动（生产由 main 引入）
	"newlist/internal/fs"
	"newlist/internal/index"
	"newlist/internal/media"
	"newlist/internal/task"
	"newlist/internal/thumb"
	"newlist/internal/user"
)

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatalf("写样例文件 %s: %v", n, err)
		}
	}
}

func mediaGet(t *testing.T, url, token string) []byte {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: status=%d body=%s", url, resp.StatusCode, body)
	}
	return body
}

func pathsOf(t *testing.T, body []byte, listKey string) map[string]bool {
	t.Helper()
	var r struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("解析响应: %v body=%s", err, body)
	}
	var items []map[string]any
	if err := json.Unmarshal(r.Data[listKey], &items); err != nil {
		t.Fatalf("解析 %s: %v body=%s", listKey, err, body)
	}
	out := map[string]bool{}
	for _, it := range items {
		for _, k := range []string{"path", "dir"} {
			if v, ok := it[k].(string); ok {
				out[v] = true
			}
		}
	}
	return out
}

// 媒体可见性开关：视频库/照片墙按挂载过滤，文件管理与搜索不受影响。
func TestMediaVisibilityFilter(t *testing.T) {
	dirHidden, dirPlain, dirNested := t.TempDir(), t.TempDir(), t.TempDir()
	writeFiles(t, dirHidden, "a.mp4", "a.jpg")
	writeFiles(t, dirPlain, "b.mp4", "b.jpg")
	writeFiles(t, dirNested, "c.mp4")

	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	cf, err := conf.New(d)
	if err != nil {
		t.Fatalf("conf.New: %v", err)
	}
	secret, err := cf.JWTSecret()
	if err != nil {
		t.Fatalf("JWTSecret: %v", err)
	}
	users := user.NewStore(d)
	hash, _ := auth.HashPassword("pw123456")
	if _, err := users.Create("admin", hash, "admin", "/", true); err != nil {
		t.Fatalf("创建管理员: %v", err)
	}

	mkCfg := func(root string, extra map[string]string) string {
		m := map[string]string{"root_path": root}
		for k, v := range extra {
			m[k] = v
		}
		b, _ := json.Marshal(m)
		return string(b)
	}
	for i, s := range []struct{ path, cfg string }{
		{"/隐藏盘", mkCfg(dirHidden, map[string]string{
			"show_video": "false", "show_photo": "true", "show_search": "false"})},
		{"/可见盘", mkCfg(dirPlain, nil)}, // 无开关键 = 旧存储，缺省展示
		{"/隐藏盘/子盘", mkCfg(dirNested, map[string]string{
			"show_video": "true", "show_photo": "true", "show_search": "true"})},
	} {
		if _, err := d.Exec(
			`INSERT INTO storages(mount_path, driver, config, ord, enabled, status, created_at)
			 VALUES(?, 'local', ?, ?, 1, '', '2026-07-07T00:00:00Z')`, s.path, s.cfg, i); err != nil {
			t.Fatalf("插入存储: %v", err)
		}
	}
	f := fs.New(d)
	if err := f.Reload(context.Background()); err != nil {
		t.Fatalf("fs.Reload: %v", err)
	}
	t.Cleanup(func() {
		// local 驱动持有根目录句柄（os.OpenRoot），Windows 下不 Drop 则 TempDir 删除失败
		d.Exec(`DELETE FROM storages`)
		f.Reload(context.Background())
	})
	idx := index.New(d, f)
	if !idx.Rebuild() {
		t.Fatal("触发索引重建失败")
	}
	deadline := time.Now().Add(10 * time.Second)
	for idx.Progress().Running {
		if time.Now().After(deadline) {
			t.Fatal("索引重建超时")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e := idx.Progress().Err; e != "" {
		t.Fatalf("索引重建出错: %s", e)
	}

	srv := New(d, cf, users, f, thumb.New(f, t.TempDir()),
		media.New(f, t.TempDir(), "http://127.0.0.1:0", secret, nil), idx, nil, task.New(1), secret)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	resp, err := http.Post(ts.URL+"/api/auth/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"pw123456"}`))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	var lr struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil || lr.Data.Token == "" {
		t.Fatalf("登录响应异常: err=%v", err)
	}
	resp.Body.Close()
	api, token := ts.URL, lr.Data.Token

	// 视频库：隐藏盘的视频被过滤；缺省存储与嵌套可见挂载正常展示
	videos := pathsOf(t, mediaGet(t, api+"/api/media/list?kind=video&limit=100", token), "items")
	if videos["/隐藏盘/a.mp4"] {
		t.Fatal("show_video=false 的挂载视频不应出现在视频库")
	}
	if !videos["/可见盘/b.mp4"] {
		t.Fatal("无开关键的旧存储应缺省展示")
	}
	if !videos["/隐藏盘/子盘/c.mp4"] {
		t.Fatal("嵌套可见挂载应按最长前缀归属，不被隐藏的父挂载牵连")
	}

	// 照片墙：同一挂载 show_photo=true，两个开关互相独立
	photos := pathsOf(t, mediaGet(t, api+"/api/media/list?kind=image&limit=100", token), "items")
	if !photos["/隐藏盘/a.jpg"] || !photos["/可见盘/b.jpg"] {
		t.Fatalf("show_photo=true 的图片应展示: %v", photos)
	}

	// 最近播放：隐藏挂载的历史条目同样不展示
	var uid int64
	if err := d.QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&uid); err != nil {
		t.Fatalf("查用户 id: %v", err)
	}
	for _, p := range []string{"/隐藏盘/a.mp4", "/可见盘/b.mp4"} {
		if _, err := d.Exec(`INSERT INTO play_history(user_id, path, played_at) VALUES(?,?,?)`,
			uid, p, "2026-07-07T01:00:00Z"); err != nil {
			t.Fatalf("插入播放历史: %v", err)
		}
	}
	hist := pathsOf(t, mediaGet(t, api+"/api/media/history?kind=video&limit=100", token), "items")
	if hist["/隐藏盘/a.mp4"] || !hist["/可见盘/b.mp4"] {
		t.Fatalf("最近播放应过滤隐藏挂载: %v", hist)
	}

	// 分组接口同样过滤
	groups := pathsOf(t, mediaGet(t, api+"/api/media/groups?kind=video", token), "groups")
	if groups["/隐藏盘"] {
		t.Fatal("groups 不应含隐藏挂载的目录")
	}
	if !groups["/可见盘"] || !groups["/隐藏盘/子盘"] {
		t.Fatalf("groups 缺少可见挂载目录: %v", groups)
	}

	// 搜索：show_search=false 的挂载整体不出现在搜索结果；嵌套可见挂载不受牵连
	sr := pathsOf(t, mediaGet(t, api+"/api/fs/search?q=mp4&limit=100", token), "items")
	if sr["/隐藏盘/a.mp4"] {
		t.Fatal("show_search=false 的挂载内容不应出现在搜索结果")
	}
	if !sr["/可见盘/b.mp4"] || !sr["/隐藏盘/子盘/c.mp4"] {
		t.Fatalf("可见挂载与嵌套可见挂载应在搜索结果中: %v", sr)
	}
	// show_search 独立于 show_photo：图片在照片墙展示但搜索仍隐藏
	sr = pathsOf(t, mediaGet(t, api+"/api/fs/search?q=jpg&limit=100", token), "items")
	if sr["/隐藏盘/a.jpg"] {
		t.Fatal("show_search 应独立于 show_photo 生效")
	}

	// 文件管理不受影响：隐藏盘目录仍可正常列出
	fsBody := mediaGet(t, api+"/api/fs/list?path="+"%2F%E9%9A%90%E8%97%8F%E7%9B%98", token)
	if !strings.Contains(string(fsBody), "a.mp4") {
		t.Fatalf("文件管理应不受媒体开关影响: %s", fsBody)
	}
}
