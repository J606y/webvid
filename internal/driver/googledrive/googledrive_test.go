package googledrive

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"newlist/internal/driver"
)

// 编译期确认实现了预期的能力接口（fs.go 按类型断言探测）。
var (
	_ driver.Driver          = (*GDrive)(nil)
	_ driver.Writer          = (*GDrive)(nil)
	_ driver.Uploader        = (*GDrive)(nil)
	_ driver.ConfigPersister = (*GDrive)(nil)
	_ driver.LinkRefresher   = (*GDrive)(nil)
)

func TestMapDriveError(t *testing.T) {
	mk := func(status int, reason, msg string) *gError {
		ge := &gError{}
		ge.Error.Code = status
		ge.Error.Message = msg
		if reason != "" {
			ge.Error.Errors = append(ge.Error.Errors, struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			}{Reason: reason, Message: msg})
		}
		return ge
	}
	cases := []struct {
		name   string
		status int
		reason string
		want   error
	}{
		{"notfound-status", 404, "", driver.ErrNotFound},
		{"notfound-reason", 400, "notFound", driver.ErrNotFound},
		{"quota", 403, "storageQuotaExceeded", driver.ErrQuota},
		{"denied", 403, "insufficientPermissions", driver.ErrDenied},
		{"denied-appfile", 403, "appNotAuthorizedToFile", driver.ErrDenied},
		{"ratelimit-falls-upstream", 403, "rateLimitExceeded", driver.ErrUpstream}, // 映射层不特判，重试在 req()
		{"upstream", 400, "badRequest", driver.ErrUpstream},
		{"upstream-500", 500, "backendError", driver.ErrUpstream},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := mapDriveError(c.status, mk(c.status, c.reason, "msg"))
			if !errors.Is(err, c.want) {
				t.Fatalf("mapDriveError(%d,%q)=%v，期望 errors.Is %v", c.status, c.reason, err, c.want)
			}
			if c.want == driver.ErrUpstream && !strings.Contains(err.Error(), "HTTP") {
				t.Fatalf("ErrUpstream 应带原始状态，得到 %q", err.Error())
			}
		})
	}
}

func TestIsRateLimited(t *testing.T) {
	if !isRateLimited(429, "") || !isRateLimited(403, "rateLimitExceeded") || !isRateLimited(403, "userRateLimitExceeded") {
		t.Fatal("应判为限流")
	}
	if isRateLimited(403, "insufficientPermissions") || isRateLimited(200, "") {
		t.Fatal("不应判为限流")
	}
}

func TestCheckName(t *testing.T) {
	good := []string{"a.mp4", "电影 2024", "a-b_c.mkv"}
	bad := []string{"", ".", "..", "a/b", "a\\b", "a\x00b", strings.Repeat("x", 256)}
	for _, n := range good {
		if err := checkName(n); err != nil {
			t.Errorf("checkName(%q) 应通过，得到 %v", n, err)
		}
	}
	for _, n := range bad {
		if err := checkName(n); err == nil {
			t.Errorf("checkName(%q) 应拒绝", n)
		}
	}
}

func TestAuthURL(t *testing.T) {
	u := AuthURL("cid.apps", "https://x.example/api/googledrive/callback", "st8")
	pu, err := url.Parse(u)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	q := pu.Query()
	checks := map[string]string{
		"client_id":     "cid.apps",
		"redirect_uri":  "https://x.example/api/googledrive/callback",
		"response_type": "code",
		"scope":         driveScope,
		"access_type":   "offline",
		"prompt":        "consent",
		"state":         "st8",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("AuthURL 参数 %s=%q，期望 %q", k, got, want)
		}
	}
}

func TestOAuthManager(t *testing.T) {
	m := &OAuthManager{pending: map[string]pendingAuth{}}
	m.Put("state1", 7, "https://x/cb")
	id, uri, ok := m.Take("state1")
	if !ok || id != 7 || uri != "https://x/cb" {
		t.Fatalf("Take 返回 %d,%q,%v", id, uri, ok)
	}
	if _, _, ok := m.Take("state1"); ok { // 单次有效
		t.Fatal("state 应已被消费")
	}
	if _, _, ok := m.Take("nope"); ok {
		t.Fatal("未知 state 应 false")
	}
}

// mockDrive 起一个假的 Drive/token 端点，供 lookup 路径解析测试。
// 目录树：root(id=ROOT) → 电影(id=F1) → 2024(id=F2) → a.mp4(id=X, size=123)
// 返回的计数器统计目录列举次数——路径解析的真实开销就在这里。
func mockDrive(t *testing.T) (*httptest.Server, *int, func()) {
	t.Helper()
	folder := func(id, name string) map[string]any {
		return map[string]any{"id": id, "name": name, "mimeType": folderMime}
	}
	file := func(id, name string, size string) map[string]any {
		return map[string]any{"id": id, "name": name, "mimeType": "video/mp4", "size": size}
	}
	children := map[string][]map[string]any{
		"ROOT": {folder("F1", "电影")},
		"F1":   {folder("F2", "2024")},
		"F2":   {file("X", "a.mp4", "123")},
	}
	listCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
		case r.URL.Path == "/files/root":
			json.NewEncoder(w).Encode(map[string]any{"id": "ROOT", "mimeType": folderMime})
		case r.URL.Path == "/files": // list：q=" 'PARENT' in parents ..."
			listCalls++
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
	driveAPIBase = srv.URL
	tokenURL = srv.URL + "/token"
	return srv, &listCalls, func() { driveAPIBase, tokenURL = oldAPI, oldTok; srv.Close() }
}

// TestLookupNegativeCache 查不存在的路径不该每次都把父目录重列一遍。转存的续传判断、
// 播放前的探测都走这条路，旧实现只缓存「找得到」的路径，查一次不存在就列一轮目录。
func TestLookupNegativeCache(t *testing.T) {
	_, listCalls, restore := mockDrive(t)
	defer restore()

	d := &GDrive{}
	if err := d.Init(context.Background(), driver.Config{
		"client_id": "c", "client_secret": "s", "refresh_token": "r",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	const missing = "电影/2024/不存在.mp4"
	if _, err := d.lookup(context.Background(), missing); err != driver.ErrNotFound {
		t.Fatalf("应为 ErrNotFound，得 %v", err)
	}
	first := *listCalls
	if first == 0 {
		t.Fatal("首次查询应真的发起列举")
	}
	for i := 0; i < 5; i++ {
		if _, err := d.lookup(context.Background(), missing); err != driver.ErrNotFound {
			t.Fatalf("第 %d 次仍应为 ErrNotFound，得 %v", i+2, err)
		}
	}
	if *listCalls != first {
		t.Fatalf("重复查不存在的路径应命中负缓存，却多发了 %d 次列举", *listCalls-first)
	}
	// 负缓存只针对这一个路径，不能波及同目录下真实存在的条目
	if f, err := d.lookup(context.Background(), "电影/2024/a.mp4"); err != nil || f.id != "X" {
		t.Fatalf("同目录已存在的文件仍应解析成功: %+v err=%v", f, err)
	}

	// 写操作后负缓存必须失效，否则刚上传的文件会被判成不存在，播放/续传全错。
	// Put/MakeDir/Rename/Move/Copy 成功后都调 cacheClear，这里以它为代表。
	d.cacheClear()
	if _, err := d.lookup(context.Background(), missing); err != driver.ErrNotFound {
		t.Fatalf("清缓存后仍应为 ErrNotFound，得 %v", err)
	}
	if *listCalls == first {
		t.Fatal("cacheClear 后应重新发起列举——负缓存没被清掉")
	}
}

func TestLookupResolvesPath(t *testing.T) {
	srv, _, restore := mockDrive(t)
	defer restore()
	_ = srv

	d := &GDrive{}
	if err := d.Init(context.Background(), driver.Config{
		"client_id": "c", "client_secret": "s", "refresh_token": "r",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if d.root != "ROOT" {
		t.Fatalf("root 应解析为 ROOT，得到 %q", d.root)
	}
	// 深层文件解析
	f, err := d.lookup(context.Background(), "电影/2024/a.mp4")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if f.id != "X" || f.isDir || f.size != 123 {
		t.Fatalf("解析结果异常: %+v", f)
	}
	// 中间目录
	dir, err := d.lookup(context.Background(), "电影/2024")
	if err != nil || dir.id != "F2" || !dir.isDir {
		t.Fatalf("中间目录解析异常: %+v err=%v", dir, err)
	}
	// 不存在
	if _, err := d.lookup(context.Background(), "电影/不存在.mp4"); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("应 ErrNotFound，得到 %v", err)
	}
}
