package server

// 离线下载的可选 Referer：防盗链源站（校验 Referer 而非 IP）在普通直链与 HLS 两条
// 路径上都要能拉下来。HLS 那条需要真实 ffmpeg（本机已装；CI 无则 Skip）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"newlist/internal/media"
)

const testReferer = "https://fansone.example/watch/1"

// hotlinkServer 起一个只认 Referer 的源站：不匹配即 403。
// 返回服务器与「看到的请求」记录器（用于断言分片请求也带上了 Referer）。
func hotlinkServer(t *testing.T, h http.Handler) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref := r.Header.Get("Referer")
		mu.Lock()
		seen = append(seen, r.URL.Path+" referer="+ref)
		mu.Unlock()
		if ref != testReferer {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// submitOffline 提交一次离线下载，返回 HTTP 状态码与 task_ids。
func submitOffline(t *testing.T, api, token string, body map[string]any) (int, []string) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, out := authedJSON(t, http.MethodPost, api+"/api/fs/offline", token, string(b))
	var data struct {
		TaskIDs []string `json:"task_ids"`
	}
	_ = json.Unmarshal(out["data"], &data)
	return resp.StatusCode, data.TaskIDs
}

// waitTask 轮询单个任务到终态，返回 state 与对外错误文本。
func waitTask(t *testing.T, api, token, id string) (state, errMsg string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		_, out := authedJSON(t, http.MethodGet, api+"/api/tasks", token, "")
		var tasks []struct {
			ID    string `json:"id"`
			State string `json:"state"`
			Err   string `json:"error"`
		}
		_ = json.Unmarshal(out["data"], &tasks)
		for _, tk := range tasks {
			if tk.ID == id && (tk.State == "done" || tk.State == "error") {
				return tk.State, tk.Err
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待任务 %s 完成超时: %s", id, out["data"])
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestHTTPURL 覆盖链接校验：仅 http/https + host 非空放行；
// 含 CRLF 的一律拒（否则能借 ffmpeg -headers 追加任意请求头）。
func TestHTTPURL(t *testing.T) {
	for _, c := range []struct {
		raw string
		ok  bool
	}{
		{"https://fansone.example/", true},
		{"http://1.2.3.4:8080/a.m3u8?t=1", true},
		{"https://fansone.example/x\r\nX-Evil: 1", false}, // CRLF 注入
		{"https://fansone.example/x\nX-Evil: 1", false},
		{"ftp://x/y", false},
		{"javascript:alert(1)", false},
		{"//fansone.example/", false}, // 无 scheme
		{"fansone.example", false},
		{"https://", false}, // 无 host
		{"", false},
	} {
		if got := httpURL(c.raw) != nil; got != c.ok {
			t.Fatalf("httpURL(%q) 放行=%v，期望 %v", c.raw, got, c.ok)
		}
	}
}

// TestOfflineRefererDirect 普通直链：非法 Referer 直接 400；不填 → 403 且提示填 Referer；
// 填对 → 文件落盘且内容一致。
func TestOfflineRefererDirect(t *testing.T) {
	content := bytes.Repeat([]byte("防盗链内容!"), 500)
	up, seen := hotlinkServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))

	api, token, rootDir, _, _ := newOfflineTestServer(t)

	// 非法 Referer：不建任务，直接打回
	for _, bad := range []string{"fansone.example", "javascript:alert(1)", "https://x/a\r\nX-Evil: 1"} {
		code, _ := submitOffline(t, api, token, map[string]any{
			"urls": []string{up.URL + "/a.bin"}, "dst_dir": "/本地", "referer": bad,
		})
		if code != 400 {
			t.Fatalf("非法 Referer %q 应 400，得到 %d", bad, code)
		}
	}

	// 不填 Referer：源站 403，错误里要点明该填 Referer（而非笼统的「没有权限」）
	code, ids := submitOffline(t, api, token, map[string]any{
		"urls": []string{up.URL + "/blocked.bin"}, "dst_dir": "/本地",
	})
	if code != 200 || len(ids) != 1 {
		t.Fatalf("提交失败: code=%d ids=%v", code, ids)
	}
	state, errMsg := waitTask(t, api, token, ids[0])
	if state != "error" || !strings.Contains(errMsg, "Referer") {
		t.Fatalf("无 Referer 应失败并提示 Referer，得到 state=%s err=%q", state, errMsg)
	}

	// 填了但填错：提示 Referer 可能不对，而非笼统的「没有权限，可能需要重新授权」
	code, ids = submitOffline(t, api, token, map[string]any{
		"urls": []string{up.URL + "/wrongref.bin"}, "dst_dir": "/本地",
		"referer": "https://other.example/",
	})
	if code != 200 || len(ids) != 1 {
		t.Fatalf("提交失败: code=%d ids=%v", code, ids)
	}
	if state, errMsg = waitTask(t, api, token, ids[0]); state != "error" || !strings.Contains(errMsg, "可能不对") {
		t.Fatalf("Referer 填错应提示填错，得到 state=%s err=%q", state, errMsg)
	}

	// 填对 Referer：拉取成功
	code, ids = submitOffline(t, api, token, map[string]any{
		"urls": []string{up.URL + "/ok.bin"}, "dst_dir": "/本地", "referer": testReferer,
	})
	if code != 200 || len(ids) != 1 {
		t.Fatalf("提交失败: code=%d ids=%v", code, ids)
	}
	if state, errMsg = waitTask(t, api, token, ids[0]); state != "done" {
		t.Fatalf("带 Referer 应成功，得到 state=%s err=%q", state, errMsg)
	}
	got, err := os.ReadFile(filepath.Join(rootDir, "ok.bin"))
	if err != nil {
		t.Fatalf("读取落盘文件: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("落盘内容不一致: %d 字节", len(got))
	}
	// blocked.bin（无 Referer）+ wrongref.bin（错的）+ ok.bin（对的）
	if reqs := seen(); len(reqs) != 3 {
		t.Fatalf("源站应收到 3 次请求，得到 %v", reqs)
	}
}

// TestOfflineRefererHLS 防盗链 HLS：m3u8 与每个分片都要带上 Referer，
// 否则 ffmpeg 拿到播放列表也下不了分片。断言产物可探测（有视频有音频）且源站请求全部带头。
func TestOfflineRefererHLS(t *testing.T) {
	ffmpeg := media.LookTool("ffmpeg")
	if ffmpeg == "" || media.LookTool("ffprobe") == "" {
		t.Skip("本机无 ffmpeg/ffprobe，跳过 HLS 防盗链测试")
	}
	// 现场生成 6 秒 3 分片样片（强制关键帧，否则默认 GOP 只切出 1 片）
	hlsDir := t.TempDir()
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=duration=6:size=320x240:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6", "-shortest",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-force_key_frames", "expr:gte(t,n_forced*2)", "-c:a", "aac",
		"-f", "hls", "-hls_time", "2", "-hls_list_size", "0",
		filepath.Join(hlsDir, "index.m3u8")).CombinedOutput()
	if err != nil {
		t.Fatalf("生成 HLS 样片: %v %s", err, out)
	}

	up, seen := hotlinkServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		if strings.HasSuffix(name, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		}
		http.ServeFile(w, r, filepath.Join(hlsDir, name))
	}))

	// offlineFetchHLS 的临时文件按 NL_DATA_DIR 取，指到临时目录，别在包目录里落 ./data
	t.Setenv("NL_DATA_DIR", t.TempDir())
	api, token, rootDir, _, _ := newOfflineTestServer(t)

	code, ids := submitOffline(t, api, token, map[string]any{
		"urls": []string{up.URL + "/index.m3u8"}, "dst_dir": "/本地",
		"name": "防盗链影片", "referer": testReferer,
	})
	if code != 200 || len(ids) != 1 {
		t.Fatalf("提交失败: code=%d ids=%v", code, ids)
	}
	if state, errMsg := waitTask(t, api, token, ids[0]); state != "done" {
		t.Fatalf("带 Referer 的 HLS 应成功，得到 state=%s err=%q", state, errMsg)
	}

	// 产物能探测出视频+音频，说明分片真的下全了（只拿到播放列表会得到几 KB 的文本）
	mp4 := filepath.Join(rootDir, "防盗链影片.mp4")
	fi, err := os.Stat(mp4)
	if err != nil {
		t.Fatalf("产物不存在: %v", err)
	}
	if fi.Size() < 50*1024 {
		t.Fatalf("产物只有 %d 字节，分片没下全", fi.Size())
	}
	probe, err := exec.Command(media.LookTool("ffprobe"), "-hide_banner", "-v", "error",
		"-show_entries", "stream=codec_type", "-of", "csv=p=0", mp4).Output()
	if err != nil {
		t.Fatalf("ffprobe 产物: %v", err)
	}
	for _, want := range []string{"video", "audio"} {
		if !strings.Contains(string(probe), want) {
			t.Fatalf("产物缺 %s 流: %s", want, probe)
		}
	}

	// 关键不变量：播放列表之外，每个分片请求也带上了 Referer
	reqs := seen()
	segs := 0
	for _, r := range reqs {
		if !strings.Contains(r, "referer="+testReferer) {
			t.Fatalf("有请求没带 Referer: %s（全部: %v）", r, reqs)
		}
		if strings.Contains(r, ".ts") {
			segs++
		}
	}
	if segs < 2 {
		t.Fatalf("分片请求数应 ≥2，实际 %d: %v", segs, reqs)
	}
	t.Logf("源站请求（全部带 Referer）: %v", reqs)
}
