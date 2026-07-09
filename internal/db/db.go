package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open 打开（必要时创建）SQLite 数据库并确保表结构存在。
func Open(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dataDir, "newlist.db")) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite 多连接并发写容易 BUSY，少量连接 + busy_timeout 足够自用规模。
	d.SetMaxOpenConns(4)
	if err := migrate(d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func migrate(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			username      TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			role          TEXT NOT NULL DEFAULT 'user',
			base_path     TEXT NOT NULL DEFAULT '/',
			can_write     INTEGER NOT NULL DEFAULT 0,
			enabled       INTEGER NOT NULL DEFAULT 1,
			created_at    TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS storages (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			mount_path TEXT UNIQUE NOT NULL,
			driver     TEXT NOT NULL,
			config     TEXT NOT NULL DEFAULT '{}',
			ord        INTEGER NOT NULL DEFAULT 0,
			enabled    INTEGER NOT NULL DEFAULT 1,
			status     TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS files (
			path       TEXT PRIMARY KEY,
			parent     TEXT NOT NULL,
			name       TEXT NOT NULL,
			name_lower TEXT NOT NULL,
			is_dir     INTEGER NOT NULL,
			size       INTEGER NOT NULL DEFAULT 0,
			modified   TEXT NOT NULL DEFAULT '',
			ext_type   TEXT NOT NULL DEFAULT 'other'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_files_parent ON files(parent)`,
		`CREATE INDEX IF NOT EXISTS idx_files_name_lower ON files(name_lower)`,
		`CREATE INDEX IF NOT EXISTS idx_files_ext_type ON files(ext_type, is_dir, modified)`,
		`CREATE TABLE IF NOT EXISTS play_history (
			user_id   INTEGER NOT NULL,
			path      TEXT NOT NULL,
			played_at TEXT NOT NULL,
			position  REAL NOT NULL DEFAULT 0,
			duration  REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, path)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_history_user_time ON play_history(user_id, played_at)`,
		// media_info：视频源信息（ffprobe 决策）持久缓存，由后台预载填充、播放探测回写。
		// 键=path，size/modified 变化即失效重探；供 /video/info 秒回免去云盘现场探测。
		`CREATE TABLE IF NOT EXISTS media_info (
				path       TEXT PRIMARY KEY,
				size       INTEGER NOT NULL,
				modified   TEXT NOT NULL,
				video_copy INTEGER NOT NULL DEFAULT 0,
				audio_copy INTEGER NOT NULL DEFAULT 0,
				audio_aac  INTEGER NOT NULL DEFAULT 0,
				has_video  INTEGER NOT NULL DEFAULT 0,
				has_audio  INTEGER NOT NULL DEFAULT 0,
				duration   REAL NOT NULL DEFAULT 0,
				probed_at  TEXT NOT NULL
			)`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// 旧库补列：audio_aac 上线前的探测缓存没有该标记，加列成功（=首次升级）时
	// 顺带清空缓存表强制重探——否则命中旧行的 aac copy 仍会缺 bsf 起播失败。
	if _, err := d.Exec(`ALTER TABLE media_info ADD COLUMN audio_aac INTEGER NOT NULL DEFAULT 0`); err == nil {
		if _, err := d.Exec(`DELETE FROM media_info`); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// 旧库补列：断点续播（position/duration）上线前的 play_history 无这两列，
	// 加列即可（默认 0 = 从头播，旧历史行不受影响，续播位置随下次播放自然填充）。
	// ALTER 重复列在新库会失败，忽略即视为已迁移。
	d.Exec(`ALTER TABLE play_history ADD COLUMN position REAL NOT NULL DEFAULT 0`)
	d.Exec(`ALTER TABLE play_history ADD COLUMN duration REAL NOT NULL DEFAULT 0`)
	return nil
}
