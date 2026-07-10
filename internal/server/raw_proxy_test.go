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
	"sync/atomic"
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

// rangeTestDriver 测试专用驱动（仅注册于 _test 二进制）：单文件（file_name，默认 big.bin），
// Link 返回配置的直链。
type rangeTestDriver struct {
	url  string
	size int64
	name string
}

func init() {
	driver.Register(driver.Meta{Name: "rangetest", Label: "测试直链"}, func() driver.Driver {
		return &rangeTestDriver{}
	})
}

func (d *rangeTestDriver) Init(_ context.Context, cfg driver.Config) error {
	d.url = cfg["link_url"]
	d.size, _ = strconv.ParseInt(cfg["size"], 10, 64)
	d.name = cfg["file_name"]
	if d.name == "" {
		d.name = "big.bin"
	}
	return nil
}
func (d *rangeTestDriver) Drop() error { return nil }
func (d *rangeTestDriver) List(_ context.Context, rel string) ([]model.FileInfo, error) {
	if rel != "" {
		return nil, driver.ErrNotFound
	}
	return []model.FileInfo{{Name: d.name, Size: d.size}}, nil
}
func (d *rangeTestDriver) Stat(_ context.Context, rel string) (model.FileInfo, error) {
	switch rel {
	case "":
		return model.FileInfo{Name: "", IsDir: true}, nil
	case d.name:
		return model.FileInfo{Name: d.name, Size: d.size}, nil
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

// rawTestEnv 是 newRawTestServer 组装的全链路环境。
type rawTestEnv struct {
	api, token, upstreamURL string
	internalTok             string        // media 内部回环鉴权头值（单流透传分支）
	upstreamReqs            *atomic.Int64 // 上游收到的请求数
}

// newRawTestServer 组装真实全链路：sqlite + 两个 rangetest 存储（开/关代理）+ Router。
func newRawTestServer(t *testing.T, content []byte) rawTestEnv {
	t.Helper()
	var reqs atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
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
	md := media.New(f, t.TempDir(), "http://127.0.0.1:0", secret, nil)
	srv := New(d, cf, users, f, thumb.New(f, t.TempDir()),
		md, index.New(d, f), nil, task.New(1), secret)
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
	return rawTestEnv{api: ts.URL, token: lr.Data.Token, upstreamURL: upstream.URL,
		internalTok: md.InternalToken(), upstreamReqs: &reqs}
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
	env := newRawTestServer(t, content)
	api, token, upstreamURL := env.api, env.token, env.upstreamURL

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

// 内部读取方（ffmpeg/ffprobe，凭 X-Internal-Auth）走单连接透传：
// 整个响应只对上游发 1 个请求；普通客户端仍走分块并发。
// 分块模式整读大文件的"每块一请求"风暴会触发云盘请求频率限流（429/503），
// 断流后播放器反复重试表现为"一直重连播放不出来"。
func TestRawProxyInternalSingleStream(t *testing.T) {
	content := proxyPattern(3<<20 + 999) // 3MB+，chunk 1MB
	env := newRawTestServer(t, content)

	// 内部请求：Range 整读 → 上游恰好 1 个请求
	base := env.upstreamReqs.Load()
	req, _ := http.NewRequest(http.MethodGet, env.api+"/api/raw/代理盘/big.bin", nil)
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("X-Internal-Auth", env.internalTok)
	req.Header.Set("Range", "bytes=100-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || !bytes.Equal(body, content[100:]) {
		t.Fatalf("内部单流: status=%d len=%d", resp.StatusCode, len(body))
	}
	if n := env.upstreamReqs.Load() - base; n != 1 {
		t.Fatalf("内部单流应只发 1 个上游请求，实际 %d", n)
	}

	// 对照：普通客户端整读 → 分块并发（3MB/1MB ≥ 3 个上游请求）
	base = env.upstreamReqs.Load()
	resp2, body2 := rawReq(t, http.MethodGet, env.api+"/api/raw/代理盘/big.bin", env.token, "bytes=0-")
	if resp2.StatusCode != 206 || !bytes.Equal(body2, content) {
		t.Fatalf("普通分块: status=%d len=%d", resp2.StatusCode, len(body2))
	}
	if n := env.upstreamReqs.Load() - base; n < 3 {
		t.Fatalf("普通客户端应走分块并发，上游请求 %d < 3", n)
	}
}
