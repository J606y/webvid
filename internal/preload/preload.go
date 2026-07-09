// Package preload 在索引就绪后于后台批量预热媒体：探测视频源信息（写入
// media_info 持久缓存）+ 下载/生成封面缩略图落盘。仅处理挂载已勾选
// 「在视频库/照片墙展示」的可见媒体，让首次浏览即命中缓存、无需现场探测云盘。
package preload

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"newlist/internal/fs"
	"newlist/internal/media"
	"newlist/internal/model"
	"newlist/internal/thumb"
	"newlist/internal/user"
)

// workers 预载并发度：每个 worker 一次处理一个文件（探测/下载都是网络阻塞，
// thumb 与 media 内部还各有自己的并发闸，这里保守取 4）。
const workers = 4

// admin 全视野身份（预载扫全部可见媒体，权限过滤由挂载可见性开关承担）。
var admin = &user.User{Role: "admin", BasePath: "/"}

// Progress 是预载进度快照（供后台展示）。
type Progress struct {
	Running    bool   `json:"running"`
	Total      int64  `json:"total"`   // 待处理媒体总数
	Done       int64  `json:"done"`    // 已处理
	Covers     int64  `json:"covers"`  // 已就绪封面数
	Probes     int64  `json:"probes"`  // 已探测视频数
	Current    string `json:"current"` // 当前处理路径
	Err        string `json:"err"`
	FinishedAt string `json:"finished_at"`
}

type Service struct {
	db     *sql.DB
	fs     *fs.FS
	thumbs *thumb.Service
	media  *media.Service

	mu         sync.Mutex
	running    bool
	total      int64
	current    string
	errMsg     string
	finishedAt string
	gen        int // 轮次代际：新一轮取代旧轮，旧 goroutine 靠比对 gen 停手
	cancel     context.CancelFunc

	done, covers, probes atomic.Int64
}

func New(db *sql.DB, f *fs.FS, th *thumb.Service, md *media.Service) *Service {
	return &Service{db: db, fs: f, thumbs: th, media: md}
}

func (s *Service) Progress() Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Progress{
		Running: s.running, Total: s.total,
		Done: s.done.Load(), Covers: s.covers.Load(), Probes: s.probes.Load(),
		Current: s.current, Err: s.errMsg, FinishedAt: s.finishedAt,
	}
}

// Run 启动一轮后台预载，取代正在进行的旧轮（幂等：已缓存的封面/探测会快速跳过，
// 只有新增或变更的文件才真正下载/探测）。立即返回，进度经 Progress 观察。
func (s *Service) Run() {
	s.mu.Lock()
	s.gen++
	gen := s.gen
	if s.cancel != nil {
		s.cancel() // 取消旧轮的在途网络操作
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.running = true
	s.total = 0
	s.current = ""
	s.errMsg = ""
	s.done.Store(0)
	s.covers.Store(0)
	s.probes.Store(0)
	s.mu.Unlock()
	go s.run(ctx, gen)
}

func (s *Service) run(ctx context.Context, gen int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[preload] 预载 panic: %v", r)
			s.finish(gen, nil)
		}
	}()
	files := s.collect()
	s.mu.Lock()
	if s.gen != gen {
		s.mu.Unlock()
		return
	}
	s.total = int64(len(files))
	s.mu.Unlock()

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, fr := range files {
		if ctx.Err() != nil || !s.isCurrent(gen) {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(fr fileRow) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[preload] 处理 %s panic: %v", fr.path, r)
				}
			}()
			s.setCurrent(gen, fr.path)
			s.process(ctx, fr)
			s.done.Add(1)
		}(fr)
	}
	wg.Wait()
	s.finish(gen, ctx.Err())
}

type fileRow struct {
	path, name, extType, modified string
	size                          int64
}

// collect 取全部可见的视频/图片文件：按最长前缀归属挂载，挂载对应界面开关
// 关闭则跳过（视频→show_video、图片→show_photo）。
func (s *Service) collect() []fileRow {
	mounts := s.fs.Mounts() // 已按挂载路径长度降序
	rows, err := s.db.Query(
		`SELECT path, name, size, modified, ext_type FROM files
		 WHERE is_dir=0 AND ext_type IN ('video','image')`)
	if err != nil {
		log.Printf("[preload] 查询媒体文件失败: %v", err)
		return nil
	}
	defer rows.Close()
	var out []fileRow
	for rows.Next() {
		var fr fileRow
		if err := rows.Scan(&fr.path, &fr.name, &fr.size, &fr.modified, &fr.extType); err != nil {
			continue
		}
		m := owner(mounts, fr.path)
		if m == nil {
			continue
		}
		kind := "video"
		if fr.extType == "image" {
			kind = "image"
		}
		if !m.MediaVisible(kind) {
			continue
		}
		out = append(out, fr)
	}
	return out
}

// process 预热单个文件：下载/生成封面 + （非 direct 视频）探测源信息入库。
func (s *Service) process(ctx context.Context, fr fileRow) {
	// 封面：远端盘下载落盘一份（宽度无关，各尺寸共用）；本地盘生成默认宽度。
	if _, file, err := s.thumbs.Get(ctx, admin, fr.path, 400); err == nil && file != "" {
		s.covers.Add(1)
	}
	// 视频源信息：direct 扩展名由 handler 按扩展名秒判，无需 ffprobe；其余探测并回写 media_info。
	if fr.extType == "video" && !media.IsDirectExt(fr.name) {
		fi := model.FileInfo{Name: fr.name, Size: fr.size, Modified: parseMod(fr.modified)}
		if _, err := s.media.Decide(ctx, admin, fr.path, fi); err == nil {
			s.probes.Add(1)
		}
	}
}

func (s *Service) finish(gen int, cerr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		return // 已被新一轮取代，不覆盖其状态
	}
	s.running = false
	s.current = ""
	if cerr != nil && cerr != context.Canceled {
		s.errMsg = cerr.Error()
	}
	s.finishedAt = time.Now().UTC().Format(time.RFC3339)
	s.cancel = nil
	log.Printf("[preload] 预载完成：封面 %d / 探测 %d / 共 %d 项",
		s.covers.Load(), s.probes.Load(), s.total)
}

func (s *Service) isCurrent(gen int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen == gen
}

func (s *Service) setCurrent(gen int, p string) {
	s.mu.Lock()
	if s.gen == gen {
		s.current = p
	}
	s.mu.Unlock()
}

// owner 返回文件所属挂载（最长前缀；mounts 已按路径长度降序，命中即最长）。
func owner(mounts []*fs.Mount, p string) *fs.Mount {
	for _, m := range mounts {
		if p == m.Path {
			return m
		}
		prefix := m.Path
		if prefix != "/" {
			prefix += "/"
		}
		if strings.HasPrefix(p, prefix) {
			return m
		}
	}
	return nil
}

func parseMod(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
