package conf

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"strconv"
	"sync"
)

// Version 由发布流水线在编译期注入，取自 git 标签（见 .github/workflows/release.yml）。
//
// 必须是 var 而不是 const —— `-ldflags -X` 注不进 const，这正是过去每次发版都得手改
// 这一行、还得记着与标签保持一致的原因。现在标签是唯一的事实来源。
//
// 未注入时保持 "dev"：本地 go build 出来的东西本就不是发布产物，界面上如实标出来比
// 冒充某个版本号安全——发错版本号会让人拿着 dev 构建去对照发布说明排查。
var Version = "dev"

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
func (s *Store) CopyFileWorkers() int { return s.intIn("copy_file_workers", 4, 1, 32) }
func (s *Store) OfflineWorkers() int  { return s.intIn("offline_workers", 2, 1, 32) }
func (s *Store) UploadWorkers() int   { return s.intIn("upload_workers", 2, 1, 8) }

// 预载的两个并发不开放给用户调，直接在这里定好。合理区间很窄：调大换不来速度
// （云盘会限流、CPU 互相争抢），调小只是白等，而选错的代价全落在"机器卡住了"上。
//
// MediaJobs 是 ffmpeg/ffprobe 的总闸，本地盘封面生成与视频源信息探测共用，播放转码不受此限。
// 每个进程按 -threads 1 跑，闸值约等于占用的核数，取 2 给最小的双核机器也留一个核。
//
// PreloadWorkers 是预载同时处理几个文件。封面已改为下载云盘自带缩略图（网络侧另有
// thumb.dlLimit 兜着），探测那半仍排在 MediaJobs 后面，所以 4 只是调度宽度，压不满机器。
const (
	MediaJobs      = 2
	PreloadWorkers = 4
)

func (s *Store) CopySpeedKB() int     { return s.intIn("copy_speed_kb", 0, 0, 1<<20) }
func (s *Store) UploadSpeedKB() int   { return s.intIn("upload_speed_kb", 0, 0, 1<<20) }
func (s *Store) DownloadSpeedKB() int { return s.intIn("download_speed_kb", 0, 0, 1<<20) }

// MediaHomeSort 媒体库首页「所有视频 / 所有照片」那一屏的取法：
// random = 每次进入随机抽一批（默认，偏发现）；modified = 最新在前（偏找东西）。
// 完整列表始终在「查看全部」里，不受此项影响。取值非法时回落 random。
func (s *Store) MediaHomeSort() string {
	if s.Get("media_home_sort", "random") == "modified" {
		return "modified"
	}
	return "random"
}
