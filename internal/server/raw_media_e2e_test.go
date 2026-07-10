package server

// 端到端回归：代理模式挂载（刁难上游：限流 + 反复断流）→ 本机回环 /api/raw
// 单流透传 → 真实 ffmpeg/ffprobe 探测并 remux 出完整 HLS。
// 复刻线上问题现场（OneDrive 开代理后部分视频"一直重连播放不出来"）：
//   - 上游首个请求 429 + Retry-After（云盘请求频率限流）→ ServeSingle 须等待后重试；
//   - 之后每个 Range 响应只发约 60% 就掐断连接 → ffmpeg 须凭 -reconnect 带新
//     Range 续传，而不是把断流当 EOF 静默产出截断片。
// 需要本机 ffmpeg/ffprobe，缺则 Skip（与 internal/media 会话测试同策略）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
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

func TestProxyLoopbackTranscodeE2E(t *testing.T) {
	ffmpeg := media.LookTool("ffmpeg")
	if ffmpeg == "" || media.LookTool("ffprobe") == "" {
		t.Skip("本机无 ffmpeg/ffprobe，跳过代理转码端到端测试")
	}

	// 样片：h264+aac（探测决策为 remux/event 模式），6 秒 640x360 保证体积足以触发多次断流
	dir := t.TempDir()
	sample := filepath.Join(dir, "sample.mkv")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=duration=6:size=640x360:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6", "-shortest",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", sample).CombinedOutput(); err != nil {
		t.Fatalf("生成样片: %v %s", err, out)
	}
	content, err := os.ReadFile(sample)
	if err != nil {
		t.Fatal(err)
	}

	// 刁难上游：首个请求 429；其余 206 只发 60%（下限 256KB）即掐断
	const cutFloor = 256 << 10
	var reqN atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqN.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var a, b int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &a, &b); err != nil {
			a, b = 0, int64(len(content)-1)
		}
		if b > int64(len(content)-1) {
			b = int64(len(content) - 1)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, len(content)))
		w.Header().Set("Content-Length", strconv.FormatInt(b-a+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		want := b - a + 1
		send := max(want*6/10, cutFloor)
		if send >= want { // 剩余不多：完整发出，保证收敛
			w.Write(content[a : b+1])
			return
		}
		w.Write(content[a : a+send])
		panic(http.ErrAbortHandler) // 掐断连接（半途断流）
	}))
	t.Cleanup(upstream.Close)

	// 全链路组装：真实监听端口 → media 回环 baseURL 指向本服务
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
	cfg, _ := json.Marshal(map[string]string{
		"link_url": upstream.URL, "size": strconv.Itoa(len(content)),
		"file_name": "sample.mkv", "proxy": "true", "threads": "3", "chunk_mb": "1",
	})
	if _, err := d.Exec(
		`INSERT INTO storages(mount_path, driver, config, ord, enabled, status, created_at)
		 VALUES('/代理盘', 'rangetest', ?, 0, 1, '', '2026-07-06T00:00:00Z')`, string(cfg)); err != nil {
		t.Fatalf("插入存储: %v", err)
	}
	f := fs.New(d)
	if err := f.Reload(context.Background()); err != nil {
		t.Fatalf("fs.Reload: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	md := media.New(f, t.TempDir(), "http://"+l.Addr().String(), secret, nil)
	t.Cleanup(md.Close) // 同步终止 ffmpeg，否则 Windows 下 TempDir 句柄未释放删不掉
	srv := New(d, cf, users, f, thumb.New(f, t.TempDir()), md, index.New(d, f), nil, task.New(1), secret)
	ts := httptest.NewUnstartedServer(srv.Router())
	ts.Listener.Close()
	ts.Listener = l
	ts.Start()
	t.Cleanup(ts.Close)

	// 探测 + remux：event copy 模式跑完全片出 ENDLIST；限流等待 + 多次断流续传均在途中
	u := &user.User{ID: 1, Username: "admin", Role: "admin", BasePath: "/", Enabled: true}
	deadline := time.Now().Add(90 * time.Second)
	var playlist string
	for {
		b, err := md.Playlist(context.Background(), u, "/代理盘/sample.mkv")
		if err != nil {
			t.Fatalf("Playlist: %v", err)
		}
		playlist = string(b)
		if strings.Contains(playlist, "#EXT-X-ENDLIST") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 ENDLIST 超时（断流续传未生效？），当前列表:\n%s", playlist)
		}
		time.Sleep(300 * time.Millisecond)
	}

	// 完整性：EXTINF 时长合计 ≈ 全片 6s。断流被当 EOF 的旧行为会静默产出截断片
	//（列表合法但时长明显变短），这里按 ≥5.5s 断言
	var total float64
	for _, line := range strings.Split(playlist, "\n") {
		if v, ok := strings.CutPrefix(line, "#EXTINF:"); ok {
			f, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), ","), 64)
			total += f
		}
	}
	if total < 5.5 {
		t.Fatalf("remux 产出疑似截断: EXTINF 合计 %.2fs < 5.5s，列表:\n%s", total, playlist)
	}
	// 刁难确已生效：429 + 至少一次断流续传 → 上游请求数必然 ≥ 3
	if n := reqN.Load(); n < 3 {
		t.Fatalf("上游仅收到 %d 个请求，刁难场景未被触发", n)
	}
}
