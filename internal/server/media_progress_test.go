package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newlist/internal/auth"
	"newlist/internal/conf"
	"newlist/internal/db"
	_ "newlist/internal/driver/local"
	"newlist/internal/fs"
	"newlist/internal/index"
	"newlist/internal/media"
	"newlist/internal/task"
	"newlist/internal/thumb"
	"newlist/internal/user"
)

// mediaPost 发 JSON POST，返回响应体与状态码。
func mediaPost(t *testing.T, url, token, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	out := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, e := resp.Body.Read(buf)
		out = append(out, buf[:n]...)
		if e != nil {
			break
		}
	}
	return resp.StatusCode, out
}

// dataFloat 从 {data:{key:float}} 响应取字段。
func dataFloat(t *testing.T, body []byte, key string) float64 {
	t.Helper()
	var r struct {
		Data map[string]float64 `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("解析响应: %v body=%s", err, body)
	}
	return r.Data[key]
}

// 断点续播：played 上报 position/duration，progress 读回、history 带回，
// 接近片尾归零，图片上报不带进度。
func TestPlaybackProgress(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, "movie.mp4", "photo.jpg")

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
	cfg, _ := json.Marshal(map[string]string{"root_path": root})
	if _, err := d.Exec(
		`INSERT INTO storages(mount_path, driver, config, ord, enabled, status, created_at)
		 VALUES('/m', 'local', ?, 0, 1, '', '2026-07-07T00:00:00Z')`, string(cfg)); err != nil {
		t.Fatalf("插入存储: %v", err)
	}
	f := fs.New(d)
	if err := f.Reload(context.Background()); err != nil {
		t.Fatalf("fs.Reload: %v", err)
	}
	t.Cleanup(func() {
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
	json.NewDecoder(resp.Body).Decode(&lr)
	resp.Body.Close()
	api, token := ts.URL, lr.Data.Token

	// 初始无历史 → progress=0
	if p := dataFloat(t, mediaGet(t, api+"/api/media/progress?path=/m/movie.mp4", token), "position"); p != 0 {
		t.Fatalf("初始 position 应为 0，实际 %v", p)
	}

	// 上报播放到 120s / 全长 600s
	if code, body := mediaPost(t, api+"/api/media/played", token,
		`{"path":"/m/movie.mp4","position":120,"duration":600}`); code != 200 {
		t.Fatalf("上报进度失败 code=%d body=%s", code, body)
	}
	if p := dataFloat(t, mediaGet(t, api+"/api/media/progress?path=/m/movie.mp4", token), "position"); p != 120 {
		t.Fatalf("续播位置应为 120，实际 %v", p)
	}
	if dur := dataFloat(t, mediaGet(t, api+"/api/media/progress?path=/m/movie.mp4", token), "duration"); dur != 600 {
		t.Fatalf("续播时长应为 600，实际 %v", dur)
	}

	// history 带回进度字段
	histBody := mediaGet(t, api+"/api/media/history?kind=video&limit=10", token)
	var hr struct {
		Data struct {
			Items []struct {
				Path     string  `json:"path"`
				Position float64 `json:"position"`
				Duration float64 `json:"duration"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(histBody, &hr); err != nil {
		t.Fatalf("解析 history: %v", err)
	}
	if len(hr.Data.Items) != 1 || hr.Data.Items[0].Position != 120 || hr.Data.Items[0].Duration != 600 {
		t.Fatalf("history 未带回进度: %+v", hr.Data.Items)
	}

	// duration=0 的上报（播放器 ready 早于元数据）不得覆盖已知时长，position 仍更新
	if code, _ := mediaPost(t, api+"/api/media/played", token,
		`{"path":"/m/movie.mp4","position":200,"duration":0}`); code != 200 {
		t.Fatal("无时长上报失败")
	}
	if p := dataFloat(t, mediaGet(t, api+"/api/media/progress?path=/m/movie.mp4", token), "position"); p != 200 {
		t.Fatalf("position 应更新为 200，实际 %v", p)
	}
	if dur := dataFloat(t, mediaGet(t, api+"/api/media/progress?path=/m/movie.mp4", token), "duration"); dur != 600 {
		t.Fatalf("duration=0 上报不应覆盖已知时长，应仍为 600，实际 %v", dur)
	}

	// 看到接近片尾（≥95%）→ 归零，下次从头
	if code, _ := mediaPost(t, api+"/api/media/played", token,
		`{"path":"/m/movie.mp4","position":580,"duration":600}`); code != 200 {
		t.Fatal("片尾上报失败")
	}
	if p := dataFloat(t, mediaGet(t, api+"/api/media/progress?path=/m/movie.mp4", token), "position"); p != 0 {
		t.Fatalf("看完应归零，实际 %v", p)
	}

	// 图片上报（无 position/duration）仍成功，且不影响"最近查看"
	if code, _ := mediaPost(t, api+"/api/media/played", token, `{"path":"/m/photo.jpg"}`); code != 200 {
		t.Fatal("图片上报失败")
	}
	if p := dataFloat(t, mediaGet(t, api+"/api/media/progress?path=/m/photo.jpg", token), "position"); p != 0 {
		t.Fatalf("图片无进度，position 应为 0，实际 %v", p)
	}

	// 不存在的文件上报 404
	if code, _ := mediaPost(t, api+"/api/media/played", token, `{"path":"/m/nope.mp4","position":5}`); code != 404 {
		t.Fatalf("不存在文件应 404，实际 %d", code)
	}
}
