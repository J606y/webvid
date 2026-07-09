package pikpak

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"newlist/internal/driver"
)

// fixedNow 提供确定性时间源（captcha 签名/缓存 TTL 用）。
func fixedNow() time.Time { return time.Unix(1700000000, 0).UTC() }

// mockServer 同时充当 auth 与 drive 两主机；按 path 路由。
type mockServer struct {
	t  *testing.T
	mu sync.Mutex

	captchaCalls int
	tokenCalls   int
	signinCalls  int

	// 可编程的行为钩子
	handleFiles  func(w http.ResponseWriter, r *http.Request)
	handleFileID func(w http.ResponseWriter, r *http.Request, id string)
	handleAction func(w http.ResponseWriter, r *http.Request) bool // 返回 true 表示已处理

	lastReqHeaders http.Header
	lastBody       map[string]any
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockServer) readBody(r *http.Request) map[string]any {
	var out map[string]any
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &out)
	m.mu.Lock()
	m.lastBody = out
	m.lastReqHeaders = r.Header.Clone()
	m.mu.Unlock()
	return out
}

func (m *mockServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/v1/shield/captcha/init":
			body := m.readBody(r)
			m.mu.Lock()
			m.captchaCalls++
			m.mu.Unlock()
			// 校验 client_id query 存在
			if r.URL.Query().Get("client_id") == "" {
				m.t.Errorf("captcha/init 缺少 client_id query")
			}
			_ = body
			writeJSON(w, map[string]any{"captcha_token": "CAP" + itoa(m.captchaCalls), "expires_in": 3600})
		case p == "/v1/auth/token":
			m.readBody(r)
			m.mu.Lock()
			m.tokenCalls++
			m.mu.Unlock()
			if m.handleAction != nil && m.handleAction(w, r) {
				return
			}
			writeJSON(w, map[string]any{
				"access_token": "AT-refresh", "refresh_token": "RT-new",
				"sub": "user-1", "expires_in": 7200,
			})
		case p == "/v1/auth/signin":
			m.readBody(r)
			m.mu.Lock()
			m.signinCalls++
			m.mu.Unlock()
			writeJSON(w, map[string]any{
				"access_token": "AT-login", "refresh_token": "RT-login",
				"sub": "user-1", "expires_in": 7200,
			})
		case p == "/drive/v1/files" && r.Method == http.MethodGet:
			if m.handleFiles != nil {
				m.handleFiles(w, r)
				return
			}
			writeJSON(w, filesResp{})
		case p == "/drive/v1/files" && r.Method == http.MethodPost:
			body := m.readBody(r)
			writeJSON(w, map[string]any{"file": map[string]any{"id": "new-" + toStr(body["name"]), "name": body["name"], "kind": "drive#folder"}})
		case strings.HasPrefix(p, "/drive/v1/files/") && r.Method == http.MethodGet:
			id := strings.TrimPrefix(p, "/drive/v1/files/")
			if m.handleFileID != nil {
				m.handleFileID(w, r, id)
				return
			}
			writeJSON(w, map[string]any{})
		case strings.HasPrefix(p, "/drive/v1/files/") && r.Method == http.MethodPatch:
			m.readBody(r)
			writeJSON(w, map[string]any{})
		case p == "/drive/v1/files:batchTrash",
			p == "/drive/v1/files:batchMove",
			p == "/drive/v1/files:batchCopy":
			m.readBody(r)
			writeJSON(w, map[string]any{})
		default:
			m.t.Errorf("未预期的请求: %s %s", r.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func itoa(i int) string { return strings.TrimSpace(toStr(i)) }
func toStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		b, _ := json.Marshal(x)
		return string(b)
	default:
		b, _ := json.Marshal(v)
		return strings.Trim(string(b), `"`)
	}
}

// newTestDriver 造一个指向 mock server 的驱动，已完成 Init（refresh_token 模式）。
func newTestDriver(t *testing.T, srv *httptest.Server, ms *mockServer) *PikPak {
	t.Helper()
	d := &PikPak{now: fixedNow}
	cfg := driver.Config{
		"platform":      "web",
		"username":      "u@example.com",
		"password":      "pass",
		"refresh_token": "RT-old",
		"device_id":     "test-device-123",
	}
	// 覆盖 client 的 base 到 mock server：通过在 Init 后替换，需自定义 Init 路径。
	// 直接构造 client：
	d.now = fixedNow
	d.cacheTTL = 2 * time.Minute
	d.cache = map[string]cacheEntry{}
	d.root = ""
	c := &client{
		authBase: srv.URL, driveBase: srv.URL, pf: platforms["web"],
		username: "u@example.com", password: "pass", deviceID: "test-device-123",
		refreshToken: "RT-old", cfg: cfg, now: fixedNow,
	}
	d.cli = c
	if _, err := c.token(context.Background()); err != nil {
		t.Fatalf("token: %v", err)
	}
	// 复刻 Init：取一次登录后 captcha_token（供请求头与 9 号错误重试用）。
	if err := c.refreshCaptcha(context.Background(), "GET:/drive/v1/files"); err != nil {
		t.Fatalf("refreshCaptcha: %v", err)
	}
	return d
}

func TestCaptchaSignGolden(t *testing.T) {
	got := captchaSign("YUMx5nI8ZU8Ap8pm", "2.0.0", "mypikpak.com", "test-device-123",
		1700000000000, platforms["web"].salts)
	want := "1.3bd6ce4a63cc0b3fac0fac225900455b"
	if got != want {
		t.Fatalf("captchaSign = %s, want %s", got, want)
	}
}

func TestLoginMeta(t *testing.T) {
	cases := []struct {
		in, key string
	}{
		{"u@example.com", "email"},
		{"13812345678", "phone_number"},
		{"plainuser", "username"},
	}
	for _, c := range cases {
		m := loginMeta(c.in)
		if _, ok := m[c.key]; !ok {
			t.Errorf("loginMeta(%q) 缺 %s，得 %v", c.in, c.key, m)
		}
	}
}

func TestLoginFlow(t *testing.T) {
	ms := &mockServer{t: t}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()

	var persisted driver.Config
	d := &PikPak{now: fixedNow}
	d.SetPersist(func(cfg driver.Config) error { persisted = cfg; return nil })
	cfg := driver.Config{"platform": "web", "username": "u@example.com", "password": "pass"}
	// 手动构造以指向 mock（Init 会用真实 base，故直接建 client）。
	d.cacheTTL = time.Minute
	d.cache = map[string]cacheEntry{}
	c := &client{authBase: srv.URL, driveBase: srv.URL, pf: platforms["web"],
		username: "u@example.com", password: "pass", deviceID: "dev", cfg: cfg,
		persist: d.persist, now: fixedNow}
	d.cli = c
	if _, err := c.token(context.Background()); err != nil {
		t.Fatalf("login token: %v", err)
	}
	if ms.signinCalls != 1 {
		t.Errorf("signin 调用次数 = %d, want 1", ms.signinCalls)
	}
	if ms.captchaCalls < 1 {
		t.Errorf("captcha 未调用")
	}
	if c.refreshToken != "RT-login" {
		t.Errorf("refresh_token = %s, want RT-login", c.refreshToken)
	}
	if persisted["refresh_token"] != "RT-login" {
		t.Errorf("persist 未写入新 refresh_token: %v", persisted)
	}
}

func TestLoginWithPrefilledCaptchaToken(t *testing.T) {
	ms := &mockServer{t: t}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()

	// 模拟：账号触发人机验证，用户从助手拿到已验证的 captcha_token + 来源 device_id 贴入。
	cfg := driver.Config{"platform": "web", "username": "u@example.com", "password": "pass",
		"device_id": "dev-from-helper", "captcha_token": "VERIFIED-CT"}
	c := &client{authBase: srv.URL, driveBase: srv.URL, pf: platforms["web"],
		username: "u@example.com", password: "pass", deviceID: "dev-from-helper",
		initialCaptcha: "VERIFIED-CT", cfg: cfg, now: fixedNow}

	if _, err := c.token(context.Background()); err != nil {
		t.Fatalf("token: %v", err)
	}
	if ms.signinCalls != 1 {
		t.Errorf("signin 调用 = %d, want 1", ms.signinCalls)
	}
	// 关键：预填 captcha_token 时不应再去 captcha/init（那正是会撞人机验证墙的一步）。
	if ms.captchaCalls != 0 {
		t.Errorf("预填 captcha_token 时不应调用 captcha/init，实际 %d 次", ms.captchaCalls)
	}
	ms.mu.Lock()
	body := ms.lastBody
	ms.mu.Unlock()
	if body["captcha_token"] != "VERIFIED-CT" {
		t.Errorf("signin 应使用预填 captcha_token，实际 %v", body["captcha_token"])
	}
	if c.initialCaptcha != "" {
		t.Errorf("initialCaptcha 用后应清空，实际 %q", c.initialCaptcha)
	}
	if c.refreshToken != "RT-login" {
		t.Errorf("refresh_token = %s, want RT-login", c.refreshToken)
	}
}

func TestRefreshRotation(t *testing.T) {
	ms := &mockServer{t: t}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()

	var persisted driver.Config
	cfg := driver.Config{"refresh_token": "RT-old"}
	c := &client{authBase: srv.URL, driveBase: srv.URL, pf: platforms["web"],
		refreshToken: "RT-old", cfg: cfg, now: fixedNow,
		persist: func(cfg driver.Config) error { persisted = cfg; return nil }}
	if _, err := c.token(context.Background()); err != nil {
		t.Fatalf("token: %v", err)
	}
	if c.refreshToken != "RT-new" {
		t.Errorf("refresh_token 未轮换: %s", c.refreshToken)
	}
	if persisted["refresh_token"] != "RT-new" {
		t.Errorf("轮换未回写: %v", persisted)
	}
}

func TestRefreshTokenInvalidFallsBackToLogin(t *testing.T) {
	ms := &mockServer{t: t}
	ms.handleAction = func(w http.ResponseWriter, r *http.Request) bool {
		// 首次 refresh_token 请求返回 4126
		writeJSON(w, map[string]any{"error_code": 4126, "error": "invalid_grant"})
		return true
	}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()

	cfg := driver.Config{"refresh_token": "RT-bad"}
	c := &client{authBase: srv.URL, driveBase: srv.URL, pf: platforms["web"],
		username: "u@example.com", password: "pass", deviceID: "dev",
		refreshToken: "RT-bad", cfg: cfg, now: fixedNow}
	if _, err := c.token(context.Background()); err != nil {
		t.Fatalf("token: %v", err)
	}
	if ms.signinCalls != 1 {
		t.Errorf("4126 后应转登录，signin 调用 = %d", ms.signinCalls)
	}
	if c.accessToken != "AT-login" {
		t.Errorf("access_token = %s, want AT-login", c.accessToken)
	}
}

func TestListPaging(t *testing.T) {
	ms := &mockServer{t: t}
	page := 0
	ms.handleFiles = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("parent_id") != "" {
			// 子目录，返回空
			writeJSON(w, filesResp{})
			return
		}
		page++
		if page == 1 {
			writeJSON(w, filesResp{
				Files: []rawFile{
					{ID: "d1", Kind: "drive#folder", Name: "电影", ModifiedTime: "2026-01-01T00:00:00Z"},
					{ID: "f1", Kind: "drive#file", Name: "a.mp4", Size: "123", ModifiedTime: "2026-01-02T00:00:00Z"},
				},
				NextPageToken: "PAGE2",
			})
		} else {
			if r.URL.Query().Get("page_token") != "PAGE2" {
				t.Errorf("第二页 page_token 缺失")
			}
			writeJSON(w, filesResp{Files: []rawFile{{ID: "f2", Kind: "drive#file", Name: "b.mkv", Size: "456"}}})
		}
	}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	d := newTestDriver(t, srv, ms)

	out, err := d.List(context.Background(), "/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("List 返回 %d 项, want 3", len(out))
	}
	if !out[0].IsDir || out[0].Name != "电影" {
		t.Errorf("首项应为目录 电影，得 %+v", out[0])
	}
	if out[1].Size != 123 {
		t.Errorf("size 解析错误: %d", out[1].Size)
	}
}

func TestLookupCacheAndNotFound(t *testing.T) {
	ms := &mockServer{t: t}
	var calls int
	ms.handleFiles = func(w http.ResponseWriter, r *http.Request) {
		calls++
		pid := r.URL.Query().Get("parent_id")
		switch pid {
		case "": // 根
			writeJSON(w, filesResp{Files: []rawFile{{ID: "d1", Kind: "drive#folder", Name: "电影"}}})
		case "d1":
			writeJSON(w, filesResp{Files: []rawFile{{ID: "f1", Kind: "drive#file", Name: "片.mp4", Size: "1"}}})
		default:
			writeJSON(w, filesResp{})
		}
	}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	d := newTestDriver(t, srv, ms)

	f, err := d.lookup(context.Background(), "/电影/片.mp4")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if f.id != "f1" {
		t.Errorf("id = %s, want f1", f.id)
	}
	callsAfter := calls
	// 第二次应命中缓存，不再发新请求
	if _, err := d.lookup(context.Background(), "/电影/片.mp4"); err != nil {
		t.Fatalf("lookup2: %v", err)
	}
	if calls != callsAfter {
		t.Errorf("第二次 lookup 应命中缓存，但发起了 %d 次新请求", calls-callsAfter)
	}
	// 不存在
	if _, err := d.lookup(context.Background(), "/电影/缺失.mp4"); err != driver.ErrNotFound {
		t.Errorf("缺失文件应 ErrNotFound，得 %v", err)
	}
}

func TestLinkSelection(t *testing.T) {
	ms := &mockServer{t: t}
	ms.handleFiles = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("parent_id") == "" {
			writeJSON(w, filesResp{Files: []rawFile{
				{ID: "f1", Kind: "drive#file", Name: "wcl.mp4"},
				{ID: "f2", Kind: "drive#file", Name: "media.mp4"},
			}})
			return
		}
		writeJSON(w, filesResp{})
	}
	ms.handleFileID = func(w http.ResponseWriter, r *http.Request, id string) {
		switch id {
		case "f1":
			writeJSON(w, map[string]any{"id": "f1", "web_content_link": "https://dl/wcl"})
		case "f2":
			writeJSON(w, map[string]any{"id": "f2", "web_content_link": "",
				"medias": []map[string]any{{"link": map[string]any{"url": "https://dl/media"}}}})
		}
	}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	d := newTestDriver(t, srv, ms)

	lk, err := d.Link(context.Background(), "/wcl.mp4")
	if err != nil || lk.URL != "https://dl/wcl" {
		t.Fatalf("web_content_link 优先: %v %+v", err, lk)
	}
	if lk.Header.Get("User-Agent") == "" {
		t.Errorf("Link 缺 User-Agent 头")
	}
	lk2, err := d.Link(context.Background(), "/media.mp4")
	if err != nil || lk2.URL != "https://dl/media" {
		t.Fatalf("回退 medias: %v %+v", err, lk2)
	}
}

func TestMakeDirLastExists(t *testing.T) {
	ms := &mockServer{t: t}
	ms.handleFiles = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("parent_id") == "" {
			writeJSON(w, filesResp{Files: []rawFile{{ID: "d1", Kind: "drive#folder", Name: "已存在"}}})
			return
		}
		writeJSON(w, filesResp{})
	}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	d := newTestDriver(t, srv, ms)

	if err := d.MakeDir(context.Background(), "/已存在"); err != driver.ErrExist {
		t.Errorf("最后一级已存在应 ErrExist，得 %v", err)
	}
	// 新目录创建成功
	if err := d.MakeDir(context.Background(), "/新目录"); err != nil {
		t.Errorf("新目录 MakeDir: %v", err)
	}
}

func TestMoveExistsPrecheck(t *testing.T) {
	ms := &mockServer{t: t}
	moved := false
	ms.handleFiles = func(w http.ResponseWriter, r *http.Request) {
		pid := r.URL.Query().Get("parent_id")
		switch pid {
		case "": // 根：有 src 文件 与 dst 目录
			writeJSON(w, filesResp{Files: []rawFile{
				{ID: "f1", Kind: "drive#file", Name: "x.mp4"},
				{ID: "d1", Kind: "drive#folder", Name: "目标"},
			}})
		case "d1": // 目标目录里已有同名
			writeJSON(w, filesResp{Files: []rawFile{{ID: "f9", Kind: "drive#file", Name: "x.mp4"}}})
		default:
			writeJSON(w, filesResp{})
		}
	}
	ms.handleAction = nil
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	d := newTestDriver(t, srv, ms)
	_ = moved

	if err := d.Move(context.Background(), "/x.mp4", "/目标"); err != driver.ErrExist {
		t.Errorf("目标重名应 ErrExist，得 %v", err)
	}
}

func TestMoveBody(t *testing.T) {
	ms := &mockServer{t: t}
	ms.handleFiles = func(w http.ResponseWriter, r *http.Request) {
		pid := r.URL.Query().Get("parent_id")
		switch pid {
		case "":
			writeJSON(w, filesResp{Files: []rawFile{
				{ID: "f1", Kind: "drive#file", Name: "x.mp4"},
				{ID: "d1", Kind: "drive#folder", Name: "目标"},
			}})
		default: // 目标目录空
			writeJSON(w, filesResp{})
		}
	}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	d := newTestDriver(t, srv, ms)

	if err := d.Move(context.Background(), "/x.mp4", "/目标"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	ms.mu.Lock()
	body := ms.lastBody
	ms.mu.Unlock()
	ids, _ := body["ids"].([]any)
	if len(ids) != 1 || ids[0] != "f1" {
		t.Errorf("batchMove ids 错误: %v", body["ids"])
	}
	to, _ := body["to"].(map[string]any)
	if to == nil || to["parent_id"] != "d1" {
		t.Errorf("batchMove to.parent_id 错误: %v", body["to"])
	}
}

func TestRemoveRootRejected(t *testing.T) {
	ms := &mockServer{t: t}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	d := newTestDriver(t, srv, ms)
	if err := d.Remove(context.Background(), "/"); err != driver.ErrNotSupported {
		t.Errorf("删根应 ErrNotSupported，得 %v", err)
	}
}

func TestRequestHeaders(t *testing.T) {
	ms := &mockServer{t: t}
	ms.handleFiles = func(w http.ResponseWriter, r *http.Request) {
		ms.mu.Lock()
		ms.lastReqHeaders = r.Header.Clone()
		ms.mu.Unlock()
		writeJSON(w, filesResp{})
	}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	d := newTestDriver(t, srv, ms)
	if _, err := d.List(context.Background(), "/"); err != nil {
		t.Fatalf("List: %v", err)
	}
	h := ms.lastReqHeaders
	if !strings.HasPrefix(h.Get("Authorization"), "Bearer ") {
		t.Errorf("缺 Bearer: %q", h.Get("Authorization"))
	}
	if h.Get("X-Device-ID") == "" {
		t.Errorf("缺 X-Device-ID")
	}
	if h.Get("User-Agent") == "" {
		t.Errorf("缺 User-Agent")
	}
	// captcha token 在 Init/token 后应已存在
	if h.Get("X-Captcha-Token") == "" {
		t.Errorf("缺 X-Captcha-Token")
	}
}

func TestAccessTokenExpiryRetry(t *testing.T) {
	ms := &mockServer{t: t}
	first := true
	ms.handleFiles = func(w http.ResponseWriter, r *http.Request) {
		if first {
			first = false
			writeJSON(w, map[string]any{"error_code": 16, "error": "token_expired"})
			return
		}
		writeJSON(w, filesResp{Files: []rawFile{{ID: "f1", Kind: "drive#file", Name: "ok.mp4"}}})
	}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	d := newTestDriver(t, srv, ms)

	out, err := d.List(context.Background(), "/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 || out[0].Name != "ok.mp4" {
		t.Errorf("token 过期重试后应成功: %+v", out)
	}
	if ms.tokenCalls < 1 {
		t.Errorf("应触发 token 刷新")
	}
}

func TestCaptchaExpiryRetry(t *testing.T) {
	ms := &mockServer{t: t}
	first := true
	ms.handleFiles = func(w http.ResponseWriter, r *http.Request) {
		if first {
			first = false
			writeJSON(w, map[string]any{"error_code": 9, "error": "captcha_invalid"})
			return
		}
		writeJSON(w, filesResp{Files: []rawFile{{ID: "f1", Kind: "drive#file", Name: "ok.mp4"}}})
	}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	d := newTestDriver(t, srv, ms)
	before := ms.captchaCalls

	out, err := d.List(context.Background(), "/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("captcha 过期重试后应成功: %+v", out)
	}
	if ms.captchaCalls <= before {
		t.Errorf("应触发 captcha 重刷")
	}
}

func TestRateLimitError(t *testing.T) {
	ms := &mockServer{t: t}
	ms.handleFiles = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"error_code": 10, "error": "file_space_not_enough"})
	}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	d := newTestDriver(t, srv, ms)
	_, err := d.List(context.Background(), "/")
	if err == nil || !strings.Contains(err.Error(), "操作频繁") {
		t.Errorf("error_code=10 应报操作频繁，得 %v", err)
	}
}
