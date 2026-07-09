package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"newlist/internal/auth"
	"newlist/internal/conf"
	"newlist/internal/db"
	"newlist/internal/fs"
	"newlist/internal/index"
	"newlist/internal/media"
	"newlist/internal/task"
	"newlist/internal/thumb"
	"newlist/internal/user"
)

// newOfflineTestServer 本地存储全链路：sqlite + local 驱动 + Router。
// 返回 API base、token、本地存储根目录、server 实例与任务管理器（供断言热生效）。
func newOfflineTestServer(t *testing.T) (api, token, rootDir string, srv *Server, tm *task.Manager) {
	t.Helper()
	// 生产 safeControl 会拦回环，但 httptest 上游恒在 127.0.0.1——测试期放行。
	oldControl := offlineDialControl
	offlineDialControl = func(network, address string, c syscall.RawConn) error { return nil }
	t.Cleanup(func() { offlineDialControl = oldControl })
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
	rootDir = t.TempDir()
	cfgJSON, _ := json.Marshal(map[string]string{"root_path": rootDir})
	if _, err := d.Exec(
		`INSERT INTO storages(mount_path, driver, config, ord, enabled, status, created_at)
		 VALUES('/本地', 'local', ?, 0, 1, '', '2026-07-08T00:00:00Z')`, string(cfgJSON)); err != nil {
		t.Fatalf("插入存储: %v", err)
	}
	f := fs.New(d)
	if err := f.Reload(context.Background()); err != nil {
		t.Fatalf("fs.Reload: %v", err)
	}
	// local 驱动经 os.OpenRoot 持有根目录句柄，Windows 上会挡住 TempDir 清理；
	// 结束时清空存储再 Reload 触发 Drop 释放句柄（cleanup LIFO，先于 TempDir 删除执行）。
	t.Cleanup(func() {
		d.Exec(`DELETE FROM storages`)
		f.Reload(context.Background())
	})
	tm = task.New(1)
	srv = New(d, cf, users, f, thumb.New(f, t.TempDir()),
		media.New(f, t.TempDir(), "http://127.0.0.1:0", secret, nil), index.New(d, f), nil, tm, secret)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/api/auth/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"pw123456"}`))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	var lr struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil || lr.Data.Token == "" {
		t.Fatalf("登录响应异常: err=%v", err)
	}
	return ts.URL, lr.Data.Token, rootDir, srv, tm
}

func authedJSON(t *testing.T, method, url, token, body string) (*http.Response, map[string]json.RawMessage) {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var out map[string]json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestOfflineDownload(t *testing.T) {
	content := bytes.Repeat([]byte("离线下载内容!"), 1000)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cd":
			w.Header().Set("Content-Disposition", `attachment; filename="资源包.zip"`)
			w.Write(content)
		case "/plain/file.bin":
			w.Write(content)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	api, token, rootDir, _, _ := newOfflineTestServer(t)

	// 非 http(s) 拒绝
	resp, _ := authedJSON(t, http.MethodPost, api+"/api/fs/offline", token,
		`{"urls":["ftp://x/y"],"dst_dir":"/本地"}`)
	if resp.StatusCode != 400 {
		t.Fatalf("ftp 链接应 400，得到 %d", resp.StatusCode)
	}

	// 两个任务：Content-Disposition 命名 + URL 末段命名
	body, _ := json.Marshal(map[string]any{
		"urls":    []string{upstream.URL + "/cd", upstream.URL + "/plain/file.bin"},
		"dst_dir": "/本地",
	})
	resp, out := authedJSON(t, http.MethodPost, api+"/api/fs/offline", token, string(body))
	if resp.StatusCode != 200 {
		t.Fatalf("offline 提交失败: %d", resp.StatusCode)
	}
	var data struct {
		TaskIDs []string `json:"task_ids"`
	}
	if err := json.Unmarshal(out["data"], &data); err != nil || len(data.TaskIDs) != 2 {
		t.Fatalf("应返回 2 个 task_id: %s", out["data"])
	}

	// 轮询任务直到全部 done
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, out := authedJSON(t, http.MethodGet, api+"/api/tasks", token, "")
		var tasks []struct {
			ID    string `json:"id"`
			State string `json:"state"`
			Group string `json:"group"`
			Err   string `json:"error"`
		}
		_ = json.Unmarshal(out["data"], &tasks)
		done, bad := 0, ""
		for _, tk := range tasks {
			if tk.Group != task.GroupOffline {
				t.Fatalf("任务组应为 offline: %+v", tk)
			}
			switch tk.State {
			case "done":
				done++
			case "error":
				bad = tk.Err
			}
		}
		if bad != "" {
			t.Fatalf("离线任务失败: %s", bad)
		}
		if done == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待任务完成超时: %s", out["data"])
		}
		time.Sleep(50 * time.Millisecond)
	}

	for _, name := range []string{"资源包.zip", "file.bin"} {
		b, err := os.ReadFile(filepath.Join(rootDir, name))
		if err != nil {
			t.Fatalf("读取 %s: %v", name, err)
		}
		if !bytes.Equal(b, content) {
			t.Fatalf("%s 内容不一致", name)
		}
	}
}

func TestSettingsHotApply(t *testing.T) {
	api, token, _, srv, tm := newOfflineTestServer(t)

	// 默认值
	_, out := authedJSON(t, http.MethodGet, api+"/api/admin/settings", token, "")
	var got map[string]any
	_ = json.Unmarshal(out["data"], &got)
	if got["copy_workers"].(float64) != 2 || got["upload_workers"].(float64) != 2 {
		t.Fatalf("默认线程数不符: %v", got)
	}

	// PUT 后热生效：worker 池扩容 + 限速器改速（含钳位）
	resp, _ := authedJSON(t, http.MethodPut, api+"/api/admin/settings", token,
		`{"site_title":"T","copy_workers":5,"offline_workers":3,"upload_workers":99,
		  "copy_speed_kb":2048,"upload_speed_kb":512,"download_speed_kb":-7}`)
	if resp.StatusCode != 200 {
		t.Fatalf("settings PUT: %d", resp.StatusCode)
	}
	if n := tm.Workers(task.GroupCopy); n != 5 {
		t.Fatalf("copy workers 应热扩到 5，得到 %d", n)
	}
	if n := tm.Workers(task.GroupOffline); n != 3 {
		t.Fatalf("offline workers 应为 3，得到 %d", n)
	}
	if kb := srv.limCopy.KBps(); kb != 2048 {
		t.Fatalf("复制限速应 2048，得到 %d", kb)
	}
	if kb := srv.limUp.KBps(); kb != 512 {
		t.Fatalf("上传限速应 512，得到 %d", kb)
	}
	if kb := srv.limDown.KBps(); kb != 0 {
		t.Fatalf("下载限速 -7 应钳到 0，得到 %d", kb)
	}
	// upload_workers 钳到上限 8，并透出到 /public/settings
	_, out = authedJSON(t, http.MethodGet, api+"/api/public/settings", token, "")
	var pub map[string]any
	_ = json.Unmarshal(out["data"], &pub)
	if pub["upload_workers"].(float64) != 8 {
		t.Fatalf("upload_workers 99 应钳到 8: %v", pub)
	}
}
