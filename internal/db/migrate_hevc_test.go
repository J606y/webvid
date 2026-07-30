package db

// media_info 旧库升级路径：加 video_hevc 列时只清「当时判为要重编码」的那批（video_copy=0），
// 它们正是 HEVC 直出可能受益的全部；已经在直出的那批结论不会变，不该跟着重探。
// 新库重开不得误清。

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrateMediaInfoVideoHEVC(t *testing.T) {
	dir := t.TempDir()

	// 造一个「上线前」的库：有 audio_aac（免得撞上更早那条整表清空的迁移）、无 video_hevc
	old, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "newlist.db")))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE media_info (
			path TEXT PRIMARY KEY, size INTEGER NOT NULL, modified TEXT NOT NULL,
			video_copy INTEGER NOT NULL DEFAULT 0, audio_copy INTEGER NOT NULL DEFAULT 0,
			audio_aac INTEGER NOT NULL DEFAULT 0,
			has_video INTEGER NOT NULL DEFAULT 0, has_audio INTEGER NOT NULL DEFAULT 0,
			duration REAL NOT NULL DEFAULT 0, probed_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("建旧表: %v", err)
	}
	for _, row := range []struct {
		path string
		copy int
	}{
		{"/vid/remux.mkv", 1},     // 原本就直出：结论不变，应保留
		{"/vid/transcode.mkv", 0}, // 原本要重编码：可能是 HEVC，应清掉重探
	} {
		if _, err := old.Exec(`INSERT INTO media_info(path,size,modified,video_copy,probed_at)
				VALUES(?,1,'m',?,'t')`, row.path, row.copy); err != nil {
			t.Fatalf("插旧行 %s: %v", row.path, err)
		}
	}
	old.Close()

	d, err := Open(dir)
	if err != nil {
		t.Fatalf("Open 升级: %v", err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM media_info WHERE video_hevc=0`).Scan(&n); err != nil {
		t.Fatalf("video_hevc 列不存在: %v", err)
	}
	var path string
	if err := d.QueryRow(`SELECT path FROM media_info`).Scan(&path); err != nil {
		t.Fatalf("升级后应恰好留下直出那一行: %v", err)
	}
	if path != "/vid/remux.mkv" {
		t.Fatalf("留下的应是原本直出的那行，实际 %q", path)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM media_info`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("只该清掉 video_copy=0 的那批: n=%d err=%v", n, err)
	}

	// 新 schema 库重开：ALTER 重复列失败 → 不得误清
	if _, err := d.Exec(`INSERT INTO media_info(path,size,modified,video_copy,probed_at)
			VALUES('/vid/c.mkv',1,'m',0,'t')`); err != nil {
		t.Fatalf("插新行: %v", err)
	}
	d.Close()
	d, err = Open(dir)
	if err != nil {
		t.Fatalf("Open 重开: %v", err)
	}
	defer d.Close()
	if err := d.QueryRow(`SELECT COUNT(*) FROM media_info`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("重开不应清缓存: n=%d err=%v", n, err)
	}
}
