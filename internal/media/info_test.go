package media

// media_info 持久探测缓存的读写与失效（不依赖 ffmpeg）。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"newlist/internal/db"
	"newlist/internal/fs"
	"newlist/internal/model"
	"newlist/internal/user"
)

func TestMediaInfoPersistence(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	svc := New(fs.New(d), t.TempDir(), "http://127.0.0.1:0", []byte("s"), d)

	mod := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fi := model.FileInfo{Name: "a.mkv", Size: 12345, Modified: mod}
	dec := Decision{VideoCopy: true, AudioCopy: false, AudioAAC: true, HasVideo: true, HasAudio: true, Duration: 42.5}
	svc.saveInfo("/vid/a.mkv", fi, dec)

	got, ok := svc.loadInfo("/vid/a.mkv", fi)
	if !ok {
		t.Fatal("期望命中持久缓存")
	}
	if got != dec {
		t.Fatalf("决策不一致: %+v vs %+v", got, dec)
	}

	// size 变化 → 缓存失效
	if _, ok := svc.loadInfo("/vid/a.mkv", model.FileInfo{Name: "a.mkv", Size: 999, Modified: mod}); ok {
		t.Fatal("size 变化应视为缓存失效")
	}
	// modified 变化 → 缓存失效
	if _, ok := svc.loadInfo("/vid/a.mkv", model.FileInfo{Name: "a.mkv", Size: 12345, Modified: mod.Add(time.Hour)}); ok {
		t.Fatal("modified 变化应视为缓存失效")
	}
	// 未知路径 → 未命中
	if _, ok := svc.loadInfo("/vid/none.mkv", fi); ok {
		t.Fatal("未知路径不应命中")
	}

	// 覆盖写：同路径新决策替换旧记录
	dec2 := Decision{VideoCopy: false, AudioCopy: true, HasVideo: true, HasAudio: true, Duration: 7}
	svc.saveInfo("/vid/a.mkv", fi, dec2)
	if got, ok := svc.loadInfo("/vid/a.mkv", fi); !ok || got != dec2 {
		t.Fatalf("覆盖写后应读到新决策: ok=%v got=%+v", ok, got)
	}

	// db=nil 的 Service：持久层为空操作，永不命中
	nilSvc := New(fs.New(d), t.TempDir(), "http://127.0.0.1:0", []byte("s"), nil)
	nilSvc.saveInfo("/vid/a.mkv", fi, dec)
	if _, ok := nilSvc.loadInfo("/vid/a.mkv", fi); ok {
		t.Fatal("db=nil 时不应有持久缓存")
	}
}

// TestDecidePersistsAndReuses 探测成功后写入 media_info，新会话（内存缓存为空）
// 再次 Decide 时命中持久层。需要 ffmpeg 生成样片。
func TestDecidePersistsAndReuses(t *testing.T) {
	root := samples(t) // 无 ffmpeg 自动 Skip
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
	t.Cleanup(func() {
		d.Exec(`DELETE FROM storages`)
		f.Reload(context.Background())
		d.Close()
	})
	u := &user.User{ID: 1, Role: "admin", BasePath: "/", Enabled: true}

	svc1 := New(f, t.TempDir(), "http://127.0.0.1:0", []byte("s"), d)
	fi, err := f.Get(context.Background(), u, "/vid/remux.mkv")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	dec1, err := svc1.Decide(context.Background(), u, "/vid/remux.mkv", fi)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !dec1.HasVideo || dec1.Duration <= 0 {
		t.Fatalf("探测结果异常: %+v", dec1)
	}
	// 落库校验
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM media_info WHERE path='/vid/remux.mkv'`).Scan(&n)
	if n != 1 {
		t.Fatalf("期望 media_info 写入 1 行, got %d", n)
	}

	// 新会话：内存缓存为空，应从持久层命中（值一致）
	svc2 := New(f, t.TempDir(), "http://127.0.0.1:0", []byte("s"), d)
	dec2, ok := svc2.loadInfo("/vid/remux.mkv", fi)
	if !ok || dec2 != dec1 {
		t.Fatalf("持久层未命中或值不一致: ok=%v %+v vs %+v", ok, dec2, dec1)
	}
}
