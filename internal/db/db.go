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
	// cache_size 负数 = 以 KiB 计，-64000 约 64MB 页缓存。默认只有 2MB，媒体库列表
	// 与搜索都是几万行的扫描，页缓存太小就是反复从磁盘读同一批索引页。
	dsn := "file:" + filepath.ToSlash(filepath.Join(dataDir, "newlist.db")) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)" +
		"&_pragma=cache_size(-64000)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite 多连接并发写容易 BUSY，少量连接 + busy_timeout 足够自用规模。
	d.SetMaxOpenConns(4)
	// 空闲上限须跟满打开上限：默认只留 2 条，并发一超过 2 就反复真开真关连接，
	// 而 modernc 每开一条都要重跑 DSN 里那几条 PRAGMA。
	d.SetMaxIdleConns(4)
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
		// idx_files_name_lower 删掉：全仓唯一读 name_lower 的是搜索的
		// LIKE '%…%'，前导通配让它必须整表扫（EXPLAIN 恒为 SCAN files），
		// 这个索引一次都没被用上，只在写入侧收钱——30 万行实测索引重建慢 28%、
		// 库大 17%（25MB）。name_lower 列本身保留，搜索仍读它。
		`DROP INDEX IF EXISTS idx_files_name_lower`,
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
				video_hevc INTEGER NOT NULL DEFAULT 0,
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
	// 旧库补列：video_hevc 上线前的探测缓存不知道一条流是不是 HEVC，旧行一律记 0
	// （=按不可直出处理），HEVC 直出对老片库就永远不生效。加列成功（=首次升级）时
	// 把当时判为要重编码的那批清掉重探——它们正是可能受益的全部，已经在直出的那批
	// 结论不会变，不必跟着一起重探。
	if _, err := d.Exec(`ALTER TABLE media_info ADD COLUMN video_hevc INTEGER NOT NULL DEFAULT 0`); err == nil {
		if _, err := d.Exec(`DELETE FROM media_info WHERE video_copy = 0`); err != nil {
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
