package onedrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newlist/internal/driver"
)

// newTestDriver 构造指向 mock server 的驱动（跳过 Init 的真实网络验证）。
func newTestDriver(ts *httptest.Server, root string) *OneDrive {
	d := &OneDrive{mode: modeDelegated, root: root, chunk: defaultChunkSize}
	d.cli = &client{
		mode:         modeDelegated,
		loginBase:    ts.URL,
		graphBase:    ts.URL + "/v1.0",
		driveBase:    ts.URL + "/v1.0/me/drive",
		clientID:     "cid",
		refreshToken: "rt-old",
		cfg:          driver.Config{"refresh_token": "rt-old"},
	}
	return d
}

func tokenJSON(access, refresh string) string {
	return fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_in":3600}`, access, refresh)
}

func TestTokenRefreshAndRotation(t *testing.T) {
	var gotForm string
	var persisted driver.Config
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/common/oauth2/v2.0/token" {
			b, _ := io.ReadAll(r.Body)
			gotForm = string(b)
			fmt.Fprint(w, tokenJSON("at-1", "rt-new"))
			return
		}
		t.Errorf("意外请求: %s", r.URL.Path)
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	d.cli.persist = func(cfg driver.Config) error { persisted = cfg; return nil }

	tok, err := d.cli.token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "at-1" {
		t.Fatalf("access_token = %q", tok)
	}
	for _, want := range []string{"grant_type=refresh_token", "client_id=cid", "refresh_token=rt-old"} {
		if !strings.Contains(gotForm, want) {
			t.Errorf("token 表单缺少 %s，实际: %s", want, gotForm)
		}
	}
	if persisted == nil || persisted["refresh_token"] != "rt-new" {
		t.Errorf("refresh_token 轮换未回写: %v", persisted)
	}
	// 二次调用走缓存，不再请求
	gotForm = ""
	if _, err := d.cli.token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotForm != "" {
		t.Error("expiresAt 内不应重新请求 token")
	}
}

func TestTokenRetryTransient(t *testing.T) {
	// 冷启动 EOF/5xx 属瞬时错误：应退避重试直至成功（PROGRESS.md M6 段实测现象）
	old := tokenRetryBase
	tokenRetryBase = time.Millisecond
	defer func() { tokenRetryBase = old }()

	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1: // AAD 5xx
			w.WriteHeader(503)
			fmt.Fprint(w, `oops`)
		case 2: // 连接级 EOF（冷启动典型表现）
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("httptest 不支持 Hijack")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
		default:
			fmt.Fprint(w, tokenJSON("at-ok", "rt"))
		}
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	tok, err := d.cli.token(context.Background())
	if err != nil {
		t.Fatalf("瞬时错误应重试成功，得 %v", err)
	}
	if tok != "at-ok" {
		t.Fatalf("access_token = %q", tok)
	}
	if calls.Load() != 3 {
		t.Errorf("应第 3 次成功，实际请求 %d 次", calls.Load())
	}
}

func TestTokenNoRetrySpamOnAuthError(t *testing.T) {
	// invalid_grant 等配置类错误多试无益：保持旧行为共 2 次即止
	old := tokenRetryBase
	tokenRetryBase = time.Millisecond
	defer func() { tokenRetryBase = old }()

	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"AADSTS70000: bad token"}`)
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	_, err := d.cli.token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "AADSTS70000") {
		t.Fatalf("应上抛 AAD 错误文案，得 %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("配置类错误应止步 2 次，实际请求 %d 次", calls.Load())
	}
}

func TestReq401RefreshRetry(t *testing.T) {
	var tokenCalls, apiCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/common/oauth2/v2.0/token":
			n := tokenCalls.Add(1)
			fmt.Fprint(w, tokenJSON(fmt.Sprintf("at-%d", n), "rt"))
		case strings.HasPrefix(r.URL.Path, "/v1.0/me/drive/root"):
			if apiCalls.Add(1) == 1 { // 第一次 401 逼刷新
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":{"code":"InvalidAuthenticationToken","message":"expired"}}`)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer at-2" {
				t.Errorf("重试未携带新 token: %s", got)
			}
			fmt.Fprint(w, `{"id":"1","name":"root","folder":{}}`)
		}
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	if _, err := d.Stat(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if tokenCalls.Load() != 2 || apiCalls.Load() != 2 {
		t.Errorf("tokenCalls=%d apiCalls=%d，期望 2/2", tokenCalls.Load(), apiCalls.Load())
	}
}

func TestListPagination(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/common/oauth2/v2.0/token":
			fmt.Fprint(w, tokenJSON("at", "rt"))
		case strings.Contains(r.URL.Path, "/root:/电影:/children"): // r.URL.Path 已解码
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `{"value":[{"name":"b.mkv","size":2,"file":{},"lastModifiedDateTime":"2026-07-06T10:00:00Z"}]}`)
			} else {
				fmt.Fprintf(w, `{"value":[{"name":"目录A","folder":{"childCount":1}},{"name":"a.mp4","size":1,"file":{}}],"@odata.nextLink":"%s"}`,
					ts.URL+r.URL.Path+"?page=2")
			}
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"itemNotFound","message":"nope"}}`)
		}
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	items, err := d.List(context.Background(), "电影")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("翻页合并后应 3 项，得 %d", len(items))
	}
	if !items[0].IsDir || items[0].Name != "目录A" {
		t.Errorf("目录项解析错误: %+v", items[0])
	}
	if items[2].Name != "b.mkv" || items[2].IsDir {
		t.Errorf("第二页文件解析错误: %+v", items[2])
	}
}

func TestStatNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/common/oauth2/v2.0/token" {
			fmt.Fprint(w, tokenJSON("at", "rt"))
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"code":"itemNotFound","message":"gone"}}`)
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	if _, err := d.Stat(context.Background(), "不存在.txt"); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，得 %v", err)
	}
}

func TestLinkDownloadURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/common/oauth2/v2.0/token" {
			fmt.Fprint(w, tokenJSON("at", "rt"))
			return
		}
		fmt.Fprint(w, `{"id":"1","size":42,"lastModifiedDateTime":"2026-07-06T10:00:00Z",
			"@microsoft.graph.downloadUrl":"https://dl.example.com/x"}`)
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	lk, err := d.Link(context.Background(), "a.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if lk.URL != "https://dl.example.com/x" || lk.Size != 42 {
		t.Fatalf("Link 解析错误: %+v", lk)
	}
}

func TestLinkCacheAndInvalidate(t *testing.T) {
	var linkCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/common/oauth2/v2.0/token":
			fmt.Fprint(w, tokenJSON("at", "rt"))
		case r.Method == http.MethodPatch: // Rename
			fmt.Fprint(w, `{}`)
		default:
			n := linkCalls.Add(1)
			fmt.Fprintf(w, `{"id":"1","size":%d,"lastModifiedDateTime":"2026-07-06T10:00:00Z",
				"@microsoft.graph.downloadUrl":"https://dl.example.com/v%d"}`, n, n)
		}
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	ctx := context.Background()
	lk1, err := d.Link(ctx, "a.mp4")
	if err != nil {
		t.Fatal(err)
	}
	lk2, err := d.Link(ctx, "a.mp4") // TTL 内二次调用应命中缓存
	if err != nil {
		t.Fatal(err)
	}
	if linkCalls.Load() != 1 {
		t.Fatalf("缓存未命中：API 被调 %d 次", linkCalls.Load())
	}
	if lk2.URL != lk1.URL || lk2.Size != lk1.Size {
		t.Fatalf("缓存返回不一致: %+v vs %+v", lk1, lk2)
	}
	if _, err := d.Link(ctx, "b.mp4"); err != nil { // 不同路径不共享
		t.Fatal(err)
	}
	if linkCalls.Load() != 2 {
		t.Fatalf("不同路径应各自取链，API 被调 %d 次", linkCalls.Load())
	}
	if err := d.Rename(ctx, "a.mp4", "c.mp4"); err != nil { // 写操作清空缓存
		t.Fatal(err)
	}
	if _, err := d.Link(ctx, "a.mp4"); err != nil {
		t.Fatal(err)
	}
	if linkCalls.Load() != 3 {
		t.Fatalf("写后缓存应失效，API 被调 %d 次", linkCalls.Load())
	}
}

// RefreshLink 强制绕过缓存换新链（直链提前作废时，Link 在 TTL 内只会返回同一条死链）。
func TestRefreshLinkBypassesCache(t *testing.T) {
	var linkCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/common/oauth2/v2.0/token" {
			fmt.Fprint(w, tokenJSON("at", "rt"))
			return
		}
		n := linkCalls.Add(1)
		fmt.Fprintf(w, `{"id":"1","size":7,"lastModifiedDateTime":"2026-07-06T10:00:00Z",
			"@microsoft.graph.downloadUrl":"https://dl.example.com/v%d"}`, n)
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	ctx := context.Background()
	lk1, err := d.Link(ctx, "a.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Link(ctx, "a.mp4"); err != nil { // 缓存命中
		t.Fatal(err)
	}
	if linkCalls.Load() != 1 {
		t.Fatalf("TTL 内 Link 应命中缓存，API 被调 %d 次", linkCalls.Load())
	}
	lk2, err := d.RefreshLink(ctx, "a.mp4") // 强制换新
	if err != nil {
		t.Fatal(err)
	}
	if linkCalls.Load() != 2 || lk2.URL == lk1.URL {
		t.Fatalf("RefreshLink 应绕过缓存取新链: calls=%d url=%q", linkCalls.Load(), lk2.URL)
	}
	lk3, err := d.Link(ctx, "a.mp4") // 新链回填缓存
	if err != nil {
		t.Fatal(err)
	}
	if linkCalls.Load() != 2 || lk3.URL != lk2.URL {
		t.Fatalf("RefreshLink 后新链应入缓存: calls=%d", linkCalls.Load())
	}
}

// Stat 复用直链缓存：Link 后 TTL 内 Stat 不再打 Graph（代理/转码每次打开都 Stat+Link，
// ffmpeg 探测+seek 连开多次会放大 Graph 请求量）。
func TestStatFromLinkCache(t *testing.T) {
	var apiCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/common/oauth2/v2.0/token" {
			fmt.Fprint(w, tokenJSON("at", "rt"))
			return
		}
		apiCalls.Add(1)
		fmt.Fprint(w, `{"id":"1","name":"a.mp4","size":42,"file":{},
			"lastModifiedDateTime":"2026-07-06T10:00:00Z",
			"@microsoft.graph.downloadUrl":"https://dl.example.com/x"}`)
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	ctx := context.Background()
	if _, err := d.Link(ctx, "dir/a.mp4"); err != nil {
		t.Fatal(err)
	}
	fi, err := d.Stat(ctx, "dir/a.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if apiCalls.Load() != 1 {
		t.Fatalf("缓存内 Stat 不应再打 API，实际 %d 次", apiCalls.Load())
	}
	mod, _ := time.Parse(time.RFC3339, "2026-07-06T10:00:00Z")
	if fi.Name != "a.mp4" || fi.Size != 42 || fi.IsDir || !fi.Modified.Equal(mod) {
		t.Fatalf("缓存 Stat 字段不符: %+v", fi)
	}
	if _, err := d.Stat(ctx, "other.mp4"); err != nil { // 未缓存路径仍走 API
		t.Fatal(err)
	}
	if apiCalls.Load() != 2 {
		t.Fatalf("未缓存路径 Stat 应走 API，实际 %d 次", apiCalls.Load())
	}
}

func TestMakeDirLastExists(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/common/oauth2/v2.0/token" {
			fmt.Fprint(w, tokenJSON("at", "rt"))
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		// 中间级 A 已存在；最后一级 B 也已存在 → 应报 ErrExist
		w.WriteHeader(409)
		fmt.Fprint(w, `{"error":{"code":"nameAlreadyExists","message":"exists"}}`)
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	err := d.MakeDir(context.Background(), "A/B")
	if !errors.Is(err, driver.ErrExist) {
		t.Fatalf("最后一级已存在应 ErrExist，得 %v", err)
	}
}

func TestPutSmall(t *testing.T) {
	var got []byte
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/common/oauth2/v2.0/token" {
			fmt.Fprint(w, tokenJSON("at", "rt"))
			return
		}
		if r.Method == http.MethodPut {
			gotPath = r.URL.Path
			got, _ = io.ReadAll(r.Body)
			w.WriteHeader(201)
			fmt.Fprint(w, `{"id":"new"}`)
			return
		}
		t.Errorf("意外请求 %s %s", r.Method, r.URL.Path)
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	data := []byte("hello onedrive")
	err := d.Put(context.Background(), "文档", "说明.md", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("上传内容不一致: %q", got)
	}
	if !strings.Contains(gotPath, "/content") {
		t.Errorf("应走 PUT content: %s", gotPath)
	}
}

func TestPutSessionChunksWithRetry(t *testing.T) {
	const chunk = 320 * 1024 * 2 // 640KiB，须为 320KiB 倍数
	total := chunk*7 + 1000      // ≈4.4MiB：超过 simpleUploadLimit 才走分块会话，共 8 块
	payload := bytes.Repeat([]byte{0xAB}, total)

	var ranges []string
	var failedOnce atomic.Bool
	var received bytes.Buffer
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/common/oauth2/v2.0/token":
			fmt.Fprint(w, tokenJSON("at", "rt"))
		case strings.Contains(r.URL.Path, "/createUploadSession"):
			fmt.Fprintf(w, `{"uploadUrl":"%s/upload-session"}`, ts.URL)
		case r.URL.Path == "/upload-session":
			cr := r.Header.Get("Content-Range")
			body, _ := io.ReadAll(r.Body)
			// 第二块第一次尝试返回 500，验证重试
			if strings.HasPrefix(cr, fmt.Sprintf("bytes %d-", chunk)) && !failedOnce.Swap(true) {
				w.WriteHeader(500)
				fmt.Fprint(w, `{"error":{"code":"generalException","message":"flaky"}}`)
				return
			}
			ranges = append(ranges, cr)
			received.Write(body)
			w.WriteHeader(202)
			fmt.Fprint(w, `{}`)
		}
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	d.chunk = chunk
	err := d.Put(context.Background(), "", "big.bin", bytes.NewReader(payload), int64(total))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for off := 0; off < total; off += chunk {
		end := off + chunk - 1
		if end > total-1 {
			end = total - 1
		}
		want = append(want, fmt.Sprintf("bytes %d-%d/%d", off, end, total))
	}
	if len(ranges) != len(want) {
		t.Fatalf("应成功上传 %d 块，得 %d: %v", len(want), len(ranges), ranges)
	}
	for i := range want {
		if ranges[i] != want[i] {
			t.Errorf("块 %d Content-Range = %s，期望 %s", i, ranges[i], want[i])
		}
	}
	if !bytes.Equal(received.Bytes(), payload) {
		t.Error("拼装后的内容与原始不一致")
	}
	if !failedOnce.Load() {
		t.Error("测试未触发 500 分支")
	}
}

func TestCopyPollsMonitor(t *testing.T) {
	var polls atomic.Int32
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/common/oauth2/v2.0/token":
			fmt.Fprint(w, tokenJSON("at", "rt"))
		case strings.Contains(r.URL.Path, ":/copy"):
			w.Header().Set("Location", ts.URL+"/monitor")
			w.WriteHeader(202)
		case r.URL.Path == "/monitor":
			if r.Header.Get("Authorization") != "" {
				t.Error("监控 URL 不应带 Bearer")
			}
			if polls.Add(1) < 2 {
				fmt.Fprint(w, `{"status":"inProgress"}`)
			} else {
				fmt.Fprint(w, `{"status":"completed"}`)
			}
		default: // Copy 前置：GET 目标目录
			fmt.Fprint(w, `{"id":"dst-id","parentReference":{"driveId":"drv-1"}}`)
		}
	}))
	defer ts.Close()

	d := newTestDriver(ts, "")
	if err := d.Copy(context.Background(), "src.txt", "目标目录"); err != nil {
		t.Fatal(err)
	}
	if polls.Load() < 2 {
		t.Errorf("应轮询至 completed，polls=%d", polls.Load())
	}
}

func TestCheckName(t *testing.T) {
	bad := []string{"", ".", "..", "a/b", `a\b`, "a:b", "a*b", "con?", "x|y", "\"q\"", " lead", "trail ", "dot."}
	for _, n := range bad {
		if err := checkName(n); !errors.Is(err, driver.ErrBadName) {
			t.Errorf("checkName(%q) 应拒绝", n)
		}
	}
	good := []string{"电影", "a b.mp4", "第01集·启程.mkv", "normal_name-1.txt"}
	for _, n := range good {
		if err := checkName(n); err != nil {
			t.Errorf("checkName(%q) 应通过: %v", n, err)
		}
	}
}

func TestItemURL(t *testing.T) {
	c := &client{driveBase: "https://g/v1.0/me/drive"}
	cases := []struct{ root, rel, suffix, want string }{
		{"", "", "/children", "https://g/v1.0/me/drive/root/children"},
		{"", "电影/a b.mp4", "", "https://g/v1.0/me/drive/root:/%E7%94%B5%E5%BD%B1/a%20b.mp4:"},
		{"媒体库", "x.txt", "/content", "https://g/v1.0/me/drive/root:/%E5%AA%92%E4%BD%93%E5%BA%93/x.txt:/content"},
		{"/媒体库/", "", "", "https://g/v1.0/me/drive/root:/%E5%AA%92%E4%BD%93%E5%BA%93:"},
	}
	for _, tc := range cases {
		if got := c.itemURL(tc.root, tc.rel, tc.suffix); got != tc.want {
			t.Errorf("itemURL(%q,%q,%q) = %s\n want %s", tc.root, tc.rel, tc.suffix, got, tc.want)
		}
	}
}
