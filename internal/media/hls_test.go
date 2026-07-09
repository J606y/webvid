package media

// HLS 会话测试需要真实 ffmpeg/ffprobe（本机 winget 已装；CI 无则整体 Skip）。
// 样片用 lavfi testsrc2+sine 现场生成，包级只生成一次。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"newlist/internal/db"
	"newlist/internal/driver"
	_ "newlist/internal/driver/local" // 注册 local 驱动
	"newlist/internal/fs"
	"newlist/internal/user"
)

var (
	genOnce sync.Once
	genDir  string
	genErr  error
)

// samples 生成样片目录（remux.mkv / transcode.mkv / audiofix.mkv / long.mkv / adts.ts）。
func samples(t *testing.T) string {
	t.Helper()
	ffmpeg := LookTool("ffmpeg")
	if ffmpeg == "" || LookTool("ffprobe") == "" {
		t.Skip("本机无 ffmpeg/ffprobe，跳过 HLS 会话测试")
	}
	genOnce.Do(func() {
		genDir, genErr = os.MkdirTemp("", "nlmedia")
		if genErr != nil {
			return
		}
		gen := func(name, dur, size string, codec ...string) {
			if genErr != nil {
				return
			}
			args := []string{"-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "testsrc2=duration=" + dur + ":size=" + size + ":rate=25",
				"-f", "lavfi", "-i", "sine=frequency=440:duration=" + dur,
				"-shortest"}
			args = append(args, codec...)
			args = append(args, filepath.Join(genDir, name))
			if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
				genErr = fmt.Errorf("生成 %s: %v %s", name, err, out)
			}
		}
		gen("remux.mkv", "10", "320x240", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac")
		gen("transcode.mkv", "10", "320x240", "-c:v", "mpeg4", "-c:a", "mp3")
		gen("audiofix.mkv", "10", "320x240", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "ac3")
		gen("long.mkv", "60", "640x480", "-c:v", "mpeg4", "-c:a", "mp3")
		gen("adts.ts", "10", "320x240", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac")
	})
	if genErr != nil {
		t.Fatalf("样片生成失败: %v", genErr)
	}
	return genDir
}

// newSvc 建一个挂着 /vid → root 的真实 local FS + media 服务。
func newSvc(t *testing.T, root string) (*Service, *user.User) {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	cfg, _ := json.Marshal(map[string]string{"root_path": root})
	if _, err := d.Exec(
		`INSERT INTO storages(mount_path, driver, config, ord, enabled, status, created_at)
		 VALUES('/vid', 'local', ?, 0, 1, '', '2026-07-07T00:00:00Z')`, string(cfg)); err != nil {
		t.Fatalf("插入存储: %v", err)
	}
	f := fs.New(d)
	if err := f.Reload(context.Background()); err != nil {
		t.Fatalf("fs.Reload: %v", err)
	}
	svc := New(f, t.TempDir(), "http://127.0.0.1:0", []byte("test-secret"), nil)
	t.Cleanup(func() {
		// 同步销毁全部会话（等 ffmpeg 退出释放句柄），否则 Windows 下 TempDir 删不掉
		svc.mu.Lock()
		ss := svc.sessions
		svc.sessions = map[string]*session{}
		svc.mu.Unlock()
		for _, se := range ss {
			se.destroy()
		}
		d.Exec(`DELETE FROM storages`)
		f.Reload(context.Background())
		d.Close()
	})
	return svc, &user.User{ID: 1, Username: "admin", Role: "admin", BasePath: "/", Enabled: true}
}

func waitPlaylist(t *testing.T, svc *Service, u *user.User, p, needle string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		b, err := svc.Playlist(context.Background(), u, p)
		if err != nil {
			t.Fatalf("Playlist(%s): %v", p, err)
		}
		if strings.Contains(string(b), needle) {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待播放列表出现 %q 超时，当前:\n%s", needle, b)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// probeConcat 把 init.mp4 与某分片拼起来交给 ffprobe，返回 (时长, start_time)。
func probeConcat(t *testing.T, initFile, segFile string) (dur, start float64) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "concat.mp4")
	out, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, fp := range []string{initFile, segFile} {
		in, err := os.Open(fp)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(out, in)
		in.Close()
	}
	out.Close()
	po, err := runProbe(context.Background(), LookTool("ffprobe"), tmp, "")
	if err != nil {
		t.Fatalf("ffprobe 拼接分片失败: %v", err)
	}
	fmt.Sscanf(po.Format.Duration, "%f", &dur)
	// start_time 不在精简结构里，重新跑一次拿 format=start_time
	cmd := exec.Command(LookTool("ffprobe"), "-v", "error",
		"-show_entries", "format=start_time", "-of", "csv=p=0", tmp)
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe start_time: %v", err)
	}
	fmt.Sscanf(strings.TrimSpace(string(b)), "%f", &start)
	return dur, start
}

func TestEventSessionRemux(t *testing.T) {
	svc, u := newSvc(t, samples(t))
	ctx := context.Background()
	p := "/vid/remux.mkv"

	b := waitPlaylist(t, svc, u, p, "#EXTINF", 15*time.Second)
	if !bytes.Contains(b, []byte(`#EXT-X-MAP:URI="init.mp4"`)) {
		t.Fatalf("播放列表缺 EXT-X-MAP:\n%s", b)
	}
	sess := svc.sessions[p]
	if sess == nil || sess.vod || !sess.dec.VideoCopy || !sess.dec.AudioCopy {
		t.Fatalf("期望 event 模式纯 remux 会话，实际 %+v", sess)
	}
	// copy 远快于实时，很快应出 ENDLIST（完整 VOD 可全片拖动）
	waitPlaylist(t, svc, u, p, "#EXT-X-ENDLIST", 20*time.Second)

	for _, name := range []string{"init.mp4", "seg_0.m4s"} {
		fp, err := svc.Segment(ctx, u, p, name)
		if err != nil {
			t.Fatalf("Segment(%s): %v", name, err)
		}
		if st, err := os.Stat(fp); err != nil || st.Size() == 0 {
			t.Fatalf("分片 %s 不存在或为空", name)
		}
	}
}

// 回归：mpegts（.ts/.m2ts）里的 AAC 是 ADTS 帧，-c:a copy 进 fMP4 必须挂
// aac_adtstoasc bsf，否则 ffmpeg 报 Malformed AAC bitstream 首个音频包即死。
// 注意：ffmpeg 挂掉时 hls muxer 仍会 finalize 出「截断分片+ENDLIST」的完整格式列表
// （实测 0.28s 一片），只等 ENDLIST 分不出成败——必须查 runErr 与产物音频流/时长。
func TestEventSessionRemuxADTS(t *testing.T) {
	svc, u := newSvc(t, samples(t))
	ctx := context.Background()
	p := "/vid/adts.ts"
	waitPlaylist(t, svc, u, p, "#EXTINF", 15*time.Second)
	sess := svc.sessions[p]
	if sess == nil || sess.vod || !sess.dec.VideoCopy || !sess.dec.AudioCopy || !sess.dec.AudioAAC {
		t.Fatalf("期望 event 纯 remux 且 AudioAAC 会话，实际 %+v", sess.dec)
	}
	waitPlaylist(t, svc, u, p, "#EXT-X-ENDLIST", 20*time.Second)

	sess.mu.Lock()
	done := sess.runDone
	sess.mu.Unlock()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("等待 ffmpeg 退出超时")
	}
	sess.mu.Lock()
	rerr := sess.runErr
	sess.mu.Unlock()
	if rerr != nil {
		t.Fatalf("remux 失败: %v", rerr)
	}

	// 产物必须含音频流且时长≈全片（无 bsf 的失败产物是 0.28s 纯视频截断片）
	initFp, err := svc.Segment(ctx, u, p, "init.mp4")
	if err != nil {
		t.Fatalf("Segment(init.mp4): %v", err)
	}
	segFp, err := svc.Segment(ctx, u, p, "seg_0.m4s")
	if err != nil {
		t.Fatalf("Segment(seg_0): %v", err)
	}
	tmp := filepath.Join(t.TempDir(), "concat.mp4")
	out, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, fp := range []string{initFp, segFp} {
		in, err := os.Open(fp)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(out, in)
		in.Close()
	}
	out.Close()
	po, err := runProbe(ctx, LookTool("ffprobe"), tmp, "")
	if err != nil {
		t.Fatalf("ffprobe 拼接分片失败: %v", err)
	}
	hasAudio := false
	for _, st := range po.Streams {
		if st.CodecType == "audio" && st.CodecName == "aac" {
			hasAudio = true
		}
	}
	var dur float64
	fmt.Sscanf(po.Format.Duration, "%f", &dur)
	if !hasAudio || dur < 3 {
		t.Fatalf("首分片应含 aac 音频且时长正常，实际 hasAudio=%v dur=%v", hasAudio, dur)
	}
}

func TestAudioOnlyTranscodeIsEventMode(t *testing.T) {
	svc, u := newSvc(t, samples(t))
	p := "/vid/audiofix.mkv" // h264+ac3 → 视频 copy 只转音频
	waitPlaylist(t, svc, u, p, "#EXTINF", 15*time.Second)
	sess := svc.sessions[p]
	if sess == nil || sess.vod || !sess.dec.VideoCopy || sess.dec.AudioCopy {
		t.Fatalf("期望 event 模式视频 copy+音频转码，实际 vod=%v dec=%+v", sess.vod, sess.dec)
	}
}

func TestVodTranscode(t *testing.T) {
	svc, u := newSvc(t, samples(t))
	ctx := context.Background()
	p := "/vid/transcode.mkv" // mpeg4 → libx264 全转码，vod 模式

	b, err := svc.Playlist(ctx, u, p)
	if err != nil {
		t.Fatalf("Playlist: %v", err)
	}
	s := string(b)
	// vod 列表即刻完整：ENDLIST + 3 个分片（10s / 4s）
	if !strings.Contains(s, "#EXT-X-ENDLIST") || !strings.Contains(s, "#EXT-X-PLAYLIST-TYPE:VOD") {
		t.Fatalf("vod 列表不完整:\n%s", s)
	}
	if n := strings.Count(s, "#EXTINF"); n != 3 {
		t.Fatalf("期望 3 个分片，实际 %d:\n%s", n, s)
	}
	sess := svc.sessions[p]
	if sess == nil || !sess.vod {
		t.Fatal("期望 vod 模式会话")
	}

	initFp, err := svc.Segment(ctx, u, p, "init.mp4")
	if err != nil {
		t.Fatalf("Segment(init.mp4): %v", err)
	}
	segFp, err := svc.Segment(ctx, u, p, "seg_0.m4s")
	if err != nil {
		t.Fatalf("Segment(seg_0): %v", err)
	}
	dur, start := probeConcat(t, initFp, segFp)
	if dur < 3.5 || dur > 4.5 {
		t.Fatalf("首分片时长 %v，期望 ≈4s", dur)
	}
	if start > 0.5 {
		t.Fatalf("首分片 start_time %v，期望 ≈0", start)
	}
	if _, err := svc.Segment(ctx, u, p, "seg_2.m4s"); err != nil {
		t.Fatalf("Segment(seg_2): %v", err)
	}
	if _, err := svc.Segment(ctx, u, p, "seg_3.m4s"); err == nil {
		t.Fatal("越界分片应报错")
	}
}

// 回归：ffmpeg 启动即建 0 字节 init.mp4，内容在首个分片完成后才写入——
// 会话刚创建就请求 init 必须等到非空（曾把空 init 发给 hls.js 导致起播失败）。
func TestInitSegmentNotEmptyOnFreshSession(t *testing.T) {
	svc, u := newSvc(t, samples(t))
	ctx := context.Background()
	p := "/vid/transcode.mkv"
	if _, err := svc.Playlist(ctx, u, p); err != nil {
		t.Fatalf("Playlist: %v", err)
	}
	fp, err := svc.Segment(ctx, u, p, "init.mp4") // 紧随其后，命中占位窗口
	if err != nil {
		t.Fatalf("Segment(init.mp4): %v", err)
	}
	st, err := os.Stat(fp)
	if err != nil || st.Size() == 0 {
		t.Fatalf("init.mp4 为空（size=%v err=%v）", st, err)
	}
}

func TestVodSeekRestart(t *testing.T) {
	svc, u := newSvc(t, samples(t))
	ctx := context.Background()
	p := "/vid/long.mkv" // 60s → 15 分片

	if _, err := svc.Playlist(ctx, u, p); err != nil {
		t.Fatalf("Playlist: %v", err)
	}
	// 立刻请求最后一个分片：远超窗口 → -ss 重启
	fp, err := svc.Segment(ctx, u, p, "seg_14.m4s")
	if err != nil {
		t.Fatalf("Segment(seg_14): %v", err)
	}
	sess := svc.sessions[p]
	sess.mu.Lock()
	runFrom := sess.runFrom
	sess.mu.Unlock()
	if runFrom != 14 {
		t.Fatalf("期望重启于分片 14，实际 runFrom=%d", runFrom)
	}
	// output_ts_offset：seek 后分片时间戳应落在绝对时间轴（≈56s）
	initFp, err := svc.Segment(ctx, u, p, "init.mp4")
	if err != nil {
		t.Fatalf("Segment(init.mp4): %v", err)
	}
	_, start := probeConcat(t, initFp, fp)
	if start < 55 || start > 57 {
		t.Fatalf("seek 分片 start_time=%v，期望 ≈56", start)
	}
	// 回拖到早段：本轮起点之前 → 再次重启
	if _, err := svc.Segment(ctx, u, p, "seg_5.m4s"); err != nil {
		t.Fatalf("Segment(seg_5): %v", err)
	}
	sess.mu.Lock()
	runFrom = sess.runFrom
	sess.mu.Unlock()
	if runFrom != 5 {
		t.Fatalf("期望重启于分片 5，实际 runFrom=%d", runFrom)
	}
}

func TestEvictionAndSweep(t *testing.T) {
	svc, u := newSvc(t, samples(t))
	ctx := context.Background()
	for _, p := range []string{"/vid/remux.mkv", "/vid/audiofix.mkv", "/vid/transcode.mkv"} {
		if _, err := svc.Playlist(ctx, u, p); err != nil {
			t.Fatalf("Playlist(%s): %v", p, err)
		}
		time.Sleep(20 * time.Millisecond) // 拉开 lastUsed
	}
	svc.mu.Lock()
	_, hasFirst := svc.sessions["/vid/remux.mkv"]
	n := len(svc.sessions)
	svc.mu.Unlock()
	if n != maxSessions || hasFirst {
		t.Fatalf("期望驱逐最早会话后保留 %d 个，实际 n=%d hasFirst=%v", maxSessions, n, hasFirst)
	}

	// 空闲回收：把剩余会话的 lastUsed 拨回 6 分钟前
	svc.mu.Lock()
	var dirs []string
	for _, se := range svc.sessions {
		se.mu.Lock()
		se.lastUsed = time.Now().Add(-6 * time.Minute)
		se.mu.Unlock()
		dirs = append(dirs, se.dir)
	}
	svc.mu.Unlock()
	svc.sweep()
	svc.mu.Lock()
	n = len(svc.sessions)
	svc.mu.Unlock()
	if n != 0 {
		t.Fatalf("sweep 后应无会话，实际 %d", n)
	}
	// destroy 异步等进程退出后删目录
	deadline := time.Now().Add(15 * time.Second)
	for {
		gone := true
		for _, dir := range dirs {
			if _, err := os.Stat(dir); err == nil {
				gone = false
			}
		}
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("会话目录未被清理: %v", dirs)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func TestSegmentBadName(t *testing.T) {
	svc, u := newSvc(t, samples(t))
	if _, err := svc.Segment(context.Background(), u, "/vid/remux.mkv", "../evil"); err == nil {
		t.Fatal("非法分片名应报错")
	}
	if _, err := svc.Segment(context.Background(), u, "/vid/remux.mkv", "seg_x.m4s"); err == nil {
		t.Fatal("非法分片名应报错")
	}
}

func TestDriverErrPassthrough(t *testing.T) {
	svc, u := newSvc(t, samples(t))
	if _, err := svc.Playlist(context.Background(), u, "/vid/不存在.mkv"); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际 %v", err)
	}
}
