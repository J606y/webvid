package conf

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"strconv"
	"sync"
)

const Version = "1.8.1"

// Store 是 settings 表的带缓存读写封装。
type Store struct {
	db    *sql.DB
	mu    sync.RWMutex
	cache map[string]string
}

func New(db *sql.DB) (*Store, error) {
	s := &Store{db: db, cache: map[string]string{}}
	rows, err := db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		s.cache[k] = v
	}
	return s, rows.Err()
}

func (s *Store) Get(key, def string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.cache[key]; ok {
		return v
	}
	return def
}

func (s *Store) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(
		`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value); err != nil {
		return err
	}
	s.cache[key] = value
	return nil
}

// JWTSecret 返回签名密钥；首次调用时生成随机 32 字节并持久化。
func (s *Store) JWTSecret() ([]byte, error) {
	if v := s.Get("jwt_secret", ""); v != "" {
		return base64.StdEncoding.DecodeString(v)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	if err := s.Set("jwt_secret", base64.StdEncoding.EncodeToString(buf)); err != nil {
		return nil, err
	}
	return buf, nil
}

func (s *Store) SiteTitle() string { return s.Get("site_title", "WebVid") }

// intIn 读整数设置并钳到 [lo,hi]；未设置或非法值返回 def。
func (s *Store) intIn(key string, def, lo, hi int) int {
	v, err := strconv.Atoi(s.Get(key, ""))
	if err != nil {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// SetInt 写整数设置（先钳到 [lo,hi]），返回实际写入值。
func (s *Store) SetInt(key string, v, lo, hi int) (int, error) {
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	return v, s.Set(key, strconv.Itoa(v))
}

// 任务线程数与全站限速设置（AList 风格）。限速单位 KB/s，0 = 不限速。
// 线程数上限与 task.maxWorkers 一致；上传并发是浏览器端同传文件数，不宜过大。
func (s *Store) CopyWorkers() int     { return s.intIn("copy_workers", 2, 1, 32) }
func (s *Store) OfflineWorkers() int  { return s.intIn("offline_workers", 2, 1, 32) }
func (s *Store) UploadWorkers() int   { return s.intIn("upload_workers", 2, 1, 8) }
func (s *Store) CopySpeedKB() int     { return s.intIn("copy_speed_kb", 0, 0, 1<<20) }
func (s *Store) UploadSpeedKB() int   { return s.intIn("upload_speed_kb", 0, 0, 1<<20) }
func (s *Store) DownloadSpeedKB() int { return s.intIn("download_speed_kb", 0, 0, 1<<20) }
