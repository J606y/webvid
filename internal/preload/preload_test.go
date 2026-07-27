package preload

import (
	"context"
	"database/sql"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newlist/internal/conf"
	"newlist/internal/db"
	_ "newlist/internal/driver/local" // 注册 local 驱动
	"newlist/internal/fs"
	"newlist/internal/index"
	"newlist/internal/media"
	"newlist/internal/thumb"
)

// mount 建库并挂载 /m -> root（cfg 需含 root_path）；返回 db 与 fs。
func mount(t *testing.T, root string, cfg map[string]string) (*sql.DB, *fs.FS) {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	cfg["root_path"] = root
	cfgJSON, _ := json.Marshal(cfg)
	if _, err := d.Exec(
		`INSERT INTO storages(mount_path, driver, config, ord, enabled, status, created_at)
		 VALUES('/m', 'local', ?, 0, 1, '', '2026-07-07T00:00:00Z')`, string(cfgJSON)); err != nil {
		t.Fatalf("插入存储: %v", err)
	}
	f := fs.New(d)
	if err := f.Reload(context.Background()); err != nil {
		t.Fatalf("fs.Reload: %v", err)
	}
	t.Cleanup(func() {
		// local 驱动持有根目录句柄（os.OpenRoot），Windows 下不 Drop 则 TempDir 删不掉；
		// sqlite 连接也要关，否则 newlist.db 被占用删不掉
		d.Exec(`DELETE FROM storages`)
		f.Reload(context.Background())
		d.Close()
	})
	return d, f
}

// store 打开 settings 表读写封装（预载用它持久化「不是现在」的推迟到点）。
func store(t *testing.T, d *sql.DB) *conf.Store {
	t.Helper()
	cf, err := conf.New(d)
	if err != nil {
		t.Fatalf("conf.New: %v", err)
	}
	return cf
}

func setCfg(t *testing.T, d *sql.DB, f *fs.FS, root string, cfg map[string]string) {
	t.Helper()
	cfg["root_path"] = root
	cfgJSON, _ := json.Marshal(cfg)
	if _, err := d.Exec(`UPDATE storages SET config=? WHERE mount_path='/m'`, string(cfgJSON)); err != nil {
		t.Fatalf("更新配置: %v", err)
	}
	if err := f.Reload(context.Background()); err != nil {
		t.Fatalf("fs.Reload: %v", err)
	}
}

func rebuild(t *testing.T, d *sql.DB, f *fs.FS) {
	t.Helper()
	idx := index.New(d, f)
	if !idx.Rebuild() {
		t.Fatal("触发索引重建失败")
	}
	deadline := time.Now().Add(10 * time.Second)
	for idx.Progress().Running {
		if time.Now().After(deadline) {
			t.Fatal("索引重建超时")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e := idx.Progress().Err; e != "" {
		t.Fatalf("索引重建出错: %s", e)
	}
}

func waitPreload(t *testing.T, s *Service) Progress {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for s.Progress().Running {
		if time.Now().After(deadline) {
			t.Fatal("预载超时")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return s.Progress()
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("写文件 %s: %v", p, err)
	}
}

func writeImage(t *testing.T, p string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 16), uint8(y * 16), 128, 255})
		}
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("创建 %s: %v", p, err)
	}
	defer f.Close()
	if strings.HasSuffix(p, ".jpg") {
		err = jpeg.Encode(f, img, nil)
	} else {
		err = png.Encode(f, img)
	}
	if err != nil {
		t.Fatalf("编码 %s: %v", p, err)
	}
}

// TestCollectVisibility：collect 只收可见媒体——挂载勾掉「在照片墙/视频库展示」后排除对应类型。
func TestCollectVisibility(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "a.png")) // 图片
	touch(t, filepath.Join(root, "c.mkv")) // 视频
	d, f := mount(t, root, map[string]string{})
	rebuild(t, d, f)

	pl := New(d, store(t, d), f, nil, nil) // collect 不用 thumb/media
	if got := len(pl.collect()); got != 2 {
		t.Fatalf("默认全展示应收 2 项, got %d", got)
	}

	setCfg(t, d, f, root, map[string]string{"show_photo": "false"})
	got := pl.collect()
	if len(got) != 1 || got[0].extType != "video" {
		t.Fatalf("关照片墙后应只剩视频, got %+v", got)
	}

	setCfg(t, d, f, root, map[string]string{"show_photo": "false", "show_video": "false"})
	if got := len(pl.collect()); got != 0 {
		t.Fatalf("两界面都关应收 0 项, got %d", got)
	}
}

// TestPreloadWarmsCovers：对本地图片跑一轮预载后封面计数就绪，且缩略图落盘可复用。
func TestPreloadWarmsCovers(t *testing.T) {
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "a.png"))
	writeImage(t, filepath.Join(root, "b.jpg"))
	d, f := mount(t, root, map[string]string{})
	rebuild(t, d, f)

	th := thumb.New(f, t.TempDir())
	md := media.New(f, t.TempDir(), "http://127.0.0.1:0", []byte("s"), d)
	pl := New(d, store(t, d), f, th, md)
	pl.Run()
	prog := waitPreload(t, pl)

	if prog.Covers < 2 {
		t.Fatalf("期望预热 ≥2 张封面, got %d (done=%d total=%d)", prog.Covers, prog.Done, prog.Total)
	}
	if prog.Err != "" {
		t.Fatalf("预载报错: %s", prog.Err)
	}
	// 缩略图已落盘：再次 Get 直接命中缓存文件
	if _, file, err := th.Get(context.Background(), admin, "/m/a.png", 400); err != nil || file == "" {
		t.Fatalf("预热后缩略图应已缓存: file=%q err=%v", file, err)
	}
}

// TestPreloadProbesVideos：非 direct 视频探测源信息写入 media_info；direct(mp4) 不探测。需要 ffmpeg。
func TestPreloadProbesVideos(t *testing.T) {
	ffmpeg := media.LookTool("ffmpeg")
	if ffmpeg == "" || media.LookTool("ffprobe") == "" {
		t.Skip("本机无 ffmpeg/ffprobe，跳过视频探测")
	}
	root := t.TempDir()
	genVideo(t, ffmpeg, filepath.Join(root, "v.mkv"))
	genVideo(t, ffmpeg, filepath.Join(root, "d.mp4"))
	d, f := mount(t, root, map[string]string{})
	rebuild(t, d, f)

	th := thumb.New(f, t.TempDir())
	md := media.New(f, t.TempDir(), "http://127.0.0.1:0", []byte("s"), d)
	pl := New(d, store(t, d), f, th, md)
	pl.Run()
	prog := waitPreload(t, pl)

	if prog.Probes < 1 {
		t.Fatalf("期望探测 ≥1 个视频, got %d", prog.Probes)
	}
	var mkv, mp4 int
	d.QueryRow(`SELECT COUNT(*) FROM media_info WHERE path='/m/v.mkv'`).Scan(&mkv)
	d.QueryRow(`SELECT COUNT(*) FROM media_info WHERE path='/m/d.mp4'`).Scan(&mp4)
	if mkv != 1 {
		t.Fatalf("非 direct 视频应写入 media_info, got %d", mkv)
	}
	if mp4 != 0 {
		t.Fatalf("direct 视频(mp4)不应探测入库, got %d", mp4)
	}
}

// TestSnoozeSkipsAutoRun：点「不是现在」后自动预载跳过、状态落库，「继续」立刻接着跑。
func TestSnoozeSkipsAutoRun(t *testing.T) {
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "a.png"))
	writeImage(t, filepath.Join(root, "b.jpg"))
	d, f := mount(t, root, map[string]string{})
	rebuild(t, d, f)

	cf := store(t, d)
	th := thumb.New(f, t.TempDir())
	md := media.New(f, t.TempDir(), "http://127.0.0.1:0", []byte("s"), d)
	pl := New(d, cf, f, th, md)

	until := pl.Snooze()
	if dur := time.Until(until); dur < 23*time.Hour || dur > SnoozeFor {
		t.Fatalf("应推迟约一天, got %v", dur)
	}
	pl.AutoRun()
	prog := pl.Progress()
	if prog.Running || prog.Done != 0 {
		t.Fatalf("推迟中不应自动预载: %+v", prog)
	}
	if !prog.Snoozed || prog.ResumeAt == "" {
		t.Fatalf("应处于推迟中: %+v", prog)
	}
	if cf.Get(snoozeKey, "") == "" {
		t.Fatal("推迟到点应落库")
	}

	pl.Resume()
	prog = waitPreload(t, pl)
	if prog.Snoozed {
		t.Fatalf("继续后不应仍在推迟: %+v", prog)
	}
	if prog.Covers < 2 {
		t.Fatalf("继续后应预热 ≥2 张封面, got %d", prog.Covers)
	}
	if v := cf.Get(snoozeKey, ""); v != "" {
		t.Fatalf("继续后应清除推迟状态, got %q", v)
	}
}

// TestResumeContinuesPending：「继续」接着推迟时剩下的清单跑，已有计数不清零。
func TestResumeContinuesPending(t *testing.T) {
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "a.png"))
	writeImage(t, filepath.Join(root, "b.jpg"))
	d, f := mount(t, root, map[string]string{})
	rebuild(t, d, f)

	th := thumb.New(f, t.TempDir())
	md := media.New(f, t.TempDir(), "http://127.0.0.1:0", []byte("s"), d)
	pl := New(d, store(t, d), f, th, md)

	// 造出「推迟时手头 1 项已跑完、还剩 1 项」的现场
	files := pl.collect()
	if len(files) != 2 {
		t.Fatalf("应收 2 项, got %d", len(files))
	}
	pl.Snooze()
	pl.mu.Lock()
	pl.total = 2
	pl.pending = files[1:]
	pl.mu.Unlock()
	pl.done.Store(1)
	pl.covers.Store(1)
	if got := pl.Progress().Pending; got != 1 {
		t.Fatalf("应剩 1 项待继续, got %d", got)
	}

	pl.Resume()
	prog := waitPreload(t, pl)
	if prog.Total != 2 || prog.Done != 2 {
		t.Fatalf("继续应接着已有计数跑完: %+v", prog)
	}
	if prog.Covers != 2 {
		t.Fatalf("封面计数应累加到 2, got %d", prog.Covers)
	}
}

// TestSnoozeSurvivesRestart：推迟跨重启仍生效；到点（含关机期间到点）则自动作废。
func TestSnoozeSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "a.png"))
	d, f := mount(t, root, map[string]string{})
	cf := store(t, d)

	if err := cf.Set(snoozeKey, time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("写推迟状态: %v", err)
	}
	if !New(d, cf, f, nil, nil).Progress().Snoozed {
		t.Fatal("未到点的推迟应在重启后恢复")
	}

	if err := cf.Set(snoozeKey, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("写推迟状态: %v", err)
	}
	if New(d, cf, f, nil, nil).Progress().Snoozed {
		t.Fatal("已到点的推迟应在重启后清除")
	}
	if v := cf.Get(snoozeKey, ""); v != "" {
		t.Fatalf("已到点应清库, got %q", v)
	}
}

// TestClearRemovesCache：删除缓存清掉封面文件与 media_info、进度归零，推迟状态保留。
func TestClearRemovesCache(t *testing.T) {
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "a.png"))
	writeImage(t, filepath.Join(root, "b.jpg"))
	d, f := mount(t, root, map[string]string{})
	rebuild(t, d, f)

	dataDir := t.TempDir()
	th := thumb.New(f, dataDir)
	md := media.New(f, t.TempDir(), "http://127.0.0.1:0", []byte("s"), d)
	pl := New(d, store(t, d), f, th, md)
	pl.Run()
	if prog := waitPreload(t, pl); prog.Covers < 2 {
		t.Fatalf("期望先预热 ≥2 张封面, got %d", prog.Covers)
	}
	// 源信息缓存直接造一行，免去本用例对 ffmpeg 的依赖
	if _, err := d.Exec(
		`INSERT INTO media_info(path,size,modified,probed_at) VALUES('/m/x.mkv',1,'','2026-07-28T00:00:00Z')`); err != nil {
		t.Fatalf("造 media_info: %v", err)
	}
	thumbDir := filepath.Join(dataDir, "thumbs")
	if n := len(dirFiles(t, thumbDir)); n < 2 {
		t.Fatalf("预热后封面目录应有 ≥2 个文件, got %d", n)
	}
	pl.Snooze() // 推迟意愿与缓存无关，删完应原样保留

	res, err := pl.Clear()
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if res.Covers < 2 || res.Bytes <= 0 {
		t.Fatalf("应报告删除的封面数与释放空间, got %+v", res)
	}
	if n := len(dirFiles(t, thumbDir)); n != 0 {
		t.Fatalf("Clear 后封面目录应为空, got %d 个文件", n)
	}
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM media_info`).Scan(&n)
	if n != 0 {
		t.Fatalf("Clear 后 media_info 应为空, got %d 行", n)
	}
	p := pl.Progress()
	if p.Running || p.Total != 0 || p.Done != 0 || p.Covers != 0 || p.Probes != 0 || p.Pending != 0 {
		t.Fatalf("Clear 后进度应归零, got %+v", p)
	}
	if !p.Snoozed {
		t.Fatal("Clear 不应解除推迟")
	}
}

func dirFiles(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录 %s: %v", dir, err)
	}
	return ents
}

func genVideo(t *testing.T, ffmpeg, out string) {
	t.Helper()
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=duration=2:size=160x120:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-shortest",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", out}
	if o, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("生成 %s: %v %s", out, err, o)
	}
}
