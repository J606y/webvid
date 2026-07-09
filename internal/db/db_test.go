package db

// media_info 旧库升级路径：加 audio_aac 列时必须清空旧探测缓存（旧行缺 aac 标记，
// 命中会让 ts/m2ts 的 aac copy 缺 bsf 复发起播失败）；新库重开不得误清。

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrateMediaInfoAudioAAC(t *testing.T) {
	dir := t.TempDir()

	// 造一个"上线前"的旧库：media_info 无 audio_aac 列，且已有一行缓存
	old, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "newlist.db")))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE media_info (
			path TEXT PRIMARY KEY, size INTEGER NOT NULL, modified TEXT NOT NULL,
			video_copy INTEGER NOT NULL DEFAULT 0, audio_copy INTEGER NOT NULL DEFAULT 0,
			has_video INTEGER NOT NULL DEFAULT 0, has_audio INTEGER NOT NULL DEFAULT 0,
			duration REAL NOT NULL DEFAULT 0, probed_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("建旧表: %v", err)
	}
	if _, err := old.Exec(`INSERT INTO media_info(path,size,modified,audio_copy,probed_at)
			VALUES('/vid/a.ts',1,'m',1,'t')`); err != nil {
		t.Fatalf("插旧行: %v", err)
	}
	old.Close()

	// 升级：加列成功 → 旧缓存整表清空
	d, err := Open(dir)
	if err != nil {
		t.Fatalf("Open 升级: %v", err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM media_info WHERE audio_aac=0`).Scan(&n); err != nil {
		t.Fatalf("audio_aac 列不存在: %v", err)
	}
	if n != 0 {
		t.Fatalf("升级加列后旧缓存应清空，剩 %d 行", n)
	}

	// 新 schema 库重开：ALTER 重复列失败 → 不得误清已有缓存
	if _, err := d.Exec(`INSERT INTO media_info(path,size,modified,audio_aac,probed_at)
			VALUES('/vid/b.ts',1,'m',1,'t')`); err != nil {
		t.Fatalf("插新行: %v", err)
	}
	d.Close()
	d, err = Open(dir)
	if err != nil {
		t.Fatalf("Open 重开: %v", err)
	}
	defer d.Close()
	if err := d.QueryRow(`SELECT COUNT(*) FROM media_info`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("重开不应清缓存: n=%d err=%v", n, err)
	}
}
