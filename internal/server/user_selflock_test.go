package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

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

// jsonPut 发 JSON PUT，返回状态码与响应体。
func jsonPut(t *testing.T, url, token, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// 停用或降级当前登录的账号会当场自锁：保存成功，紧接着的任一请求即被判 401
// 踢回登录页，且再也登不进来。后端必须拦住，改别人不受影响。
func TestUserSelfLock(t *testing.T) {
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
	me, err := users.Create("admin", hash, "admin", "/", true)
	if err != nil {
		t.Fatalf("创建管理员: %v", err)
	}
	other, err := users.Create("bob", hash, "user", "/", true)
	if err != nil {
		t.Fatalf("创建普通用户: %v", err)
	}
	// 再留一个管理员，避免撞上「最后一个管理员」那条独立校验，确保测的是自锁防呆
	if _, err := users.Create("root", hash, "admin", "/", true); err != nil {
		t.Fatalf("创建第二管理员: %v", err)
	}

	f := fs.New(d)
	srv := New(d, cf, users, f, thumb.New(f, t.TempDir()),
		media.New(f, t.TempDir(), "http://127.0.0.1:0", secret, nil), index.New(d, f), nil, task.New(1), secret)
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
	token := lr.Data.Token
	selfURL := ts.URL + "/api/admin/users/" + strconv.FormatInt(me.ID, 10)

	// 停用自己 → 400
	if code, body := jsonPut(t, selfURL, token,
		`{"username":"admin","role":"admin","base_path":"/","can_write":true,"enabled":false}`); code != 400 {
		t.Fatalf("停用自己应 400，实际 %d body=%s", code, body)
	}
	// 降级自己 → 400
	if code, body := jsonPut(t, selfURL, token,
		`{"username":"admin","role":"user","base_path":"/","can_write":true,"enabled":true}`); code != 400 {
		t.Fatalf("降级自己应 400，实际 %d body=%s", code, body)
	}
	// 保持管理员且启用时，改自己的其余字段照常
	if code, body := jsonPut(t, selfURL, token,
		`{"username":"admin","role":"admin","base_path":"/媒体","can_write":true,"enabled":true}`); code != 200 {
		t.Fatalf("改自己其余字段应 200，实际 %d body=%s", code, body)
	}
	// 自锁没被拦住的话账号已废，这里应仍能读到用户列表
	u, err := users.GetByID(me.ID)
	if err != nil || !u.Enabled || u.Role != "admin" {
		t.Fatalf("当前账号应仍是启用的管理员，实际 %+v err=%v", u, err)
	}

	// 停用别人不受影响
	if code, body := jsonPut(t, ts.URL+"/api/admin/users/"+strconv.FormatInt(other.ID, 10), token,
		`{"username":"bob","role":"user","base_path":"/","can_write":false,"enabled":false}`); code != 200 {
		t.Fatalf("停用其他账号应 200，实际 %d body=%s", code, body)
	}
}
