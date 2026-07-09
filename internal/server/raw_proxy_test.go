package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"newlist/internal/auth"
	"newlist/internal/conf"
	"newlist/internal/db"
	"newlist/internal/driver"
	"newlist/internal/fs"
	"newlist/internal/index"
	"newlist/internal/media"
	"newlist/internal/model"
	"newlist/internal/task"
	"newlist/internal/thumb"
	"newlist/internal/user"
)

// rangeTestDriver 测试专用驱动（仅注册于 _test 二进制）：单文件 big.bin，Link 返回配置的直链。
type rangeTestDriver struct {
	url  string
	size int64
}

func init() {
	driver.Register(driver.Meta{Name: "rangetest", Label: "测试直链"}, func() driver.Driver {
		return &rangeTestDriver{}
	})
}

func (d *rangeTestDriver) Init(_ context.Context, cfg driver.Config) error {
	d.url = cfg["link_url"]
	d.size, _ = strconv.ParseInt(cfg["size"], 10, 64)
	return nil
}
func (d *rangeTestDriver) Drop() error { return nil }
func (d *rangeTestDriver) List(_ context.Context, rel string) ([]model.FileInfo, error) {
	if rel != "" {
		return nil, driver.ErrNotFound
	}
	return []model.FileInfo{{Name: "big.bin", Size: d.size}}, nil
}
func (d *rangeTestDriver) Stat(_ context.Context, rel string) (model.FileInfo, error) {
	switch rel {
	case "":
		return model.FileInfo{Name: "", IsDir: true}, nil
	case "big.bin":
		return model.FileInfo{Name: "big.bin", Size: d.size}, nil
	}
	return model.FileInfo{}, driver.ErrNotFound
}
func (d *rangeTestDriver) Link(context.Context, string) (*driver.Link, error) {
	return &driver.Link{URL: d.url}, nil
}

func proxyPattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + 7)
	}
	return b
}

// newRawTestServer 组装真实全链路：sqlite + 两个 rangetest 存储（开/关代理）+ Router。
// 返回 API base、登录 token、直链上游 URL。
func newRawTestServer(t *testing.T, content []byte) (api, token, upstreamURL string) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var a, b int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &a, &b); err != nil {
			w.WriteHeader(http.StatusOK)
			w.Write(content)
			return
		}
		if b > int64(len(content)-1) {
			b = int64(len(content) - 1)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[a : b+1])
	}))
	t.Cleanup(upstream.Close)

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
	mkCfg := func(proxy string) string {
		b, _ := json.Marshal(map[string]string{
			"link_url": upstream.URL, "size": strconv.Itoa(len(content)),
			"proxy": proxy, "threads": "3", "chunk_mb": "1",
		})
		return string(b)
	}
	for i, m := range []struct{ path, cfg string }{
		{"/代理盘", mkCfg("true")},
		{"/直链盘", mkCfg("false")},
	} {
		if _, err := d.Exec(
			`INSERT INTO storages(mount_path, driver, config, ord, enabled, status, created_at)
			 VALUES(?, 'rangetest', ?, ?, 1, '', '2026-07-06T00:00:00Z')`, m.path, m.cfg, i); err != nil {
			t.Fatalf("插入存储: %v", err)
		}
	}
	f := fs.New(d)
	if err := f.Reload(context.Background()); err != nil {
		t.Fatalf("fs.Reload: %v", err)
	}
	srv := New(d, cf, users, f, thumb.New(f, t.TempDir()),
		media.New(f, t.TempDir(), "http://127.0.0.1:0", secret, nil), index.New(d, f), nil, task.New(1), secret)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	// 登录拿 token
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
	return ts.URL, lr.Data.Token, upstream.URL
}

func rawReq(t *testing.T, method, url, token, rangeHdr string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // 不跟随 302，便于断言
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

// 全链路：登录 → raw 代理模式 200/206/HEAD；直链模式 302。
func TestRawProxyFullChain(t *testing.T) {
	content := proxyPattern(3<<20 + 999) // 3MB+，chunk 1MB → 多块并发
	api, token, upstreamURL := newRawTestServer(t, content)

	// 代理模式：200 全量
	resp, body := rawReq(t, http.MethodGet, api+"/api/raw/代理盘/big.bin", token, "")
	if resp.StatusCode != 200 || !bytes.Equal(body, content) {
		t.Fatalf("代理全量: status=%d len=%d want=%d", resp.StatusCode, len(body), len(content))
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatal("代理模式应声明 Accept-Ranges: bytes")
	}

	// 代理模式：Range 透传 → 206 切片
	resp, body = rawReq(t, http.MethodGet, api+"/api/raw/代理盘/big.bin", token, "bytes=1048570-1048999")
	if resp.StatusCode != 206 || !bytes.Equal(body, content[1048570:1049000]) {
		t.Fatalf("代理 Range: status=%d len=%d", resp.StatusCode, len(body))
	}
	wantCR := fmt.Sprintf("bytes 1048570-1048999/%d", len(content))
	if resp.Header.Get("Content-Range") != wantCR {
		t.Fatalf("Content-Range=%q want %q", resp.Header.Get("Content-Range"), wantCR)
	}

	// 代理模式：HEAD 只回头
	resp, body = rawReq(t, http.MethodHead, api+"/api/raw/代理盘/big.bin", token, "")
	if resp.StatusCode != 200 || len(body) != 0 ||
		resp.Header.Get("Content-Length") != strconv.Itoa(len(content)) {
		t.Fatalf("代理 HEAD: status=%d len=%d cl=%s", resp.StatusCode, len(body), resp.Header.Get("Content-Length"))
	}

	// 直链模式（proxy=false）：302 到上游
	resp, _ = rawReq(t, http.MethodGet, api+"/api/raw/直链盘/big.bin", token, "")
	if resp.StatusCode != 302 || resp.Header.Get("Location") != upstreamURL {
		t.Fatalf("直链模式应 302 到上游: status=%d loc=%q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// 未登录 401
	req, _ := http.NewRequest(http.MethodGet, api+"/api/raw/代理盘/big.bin", nil)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != 401 {
		t.Fatalf("未登录应 401，got %d", r2.StatusCode)
	}
}
