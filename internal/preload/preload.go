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

	"newlist/internal/conf"
	"newlist/internal/fs"
	"newlist/internal/media"
	"newlist/internal/model"
	"newlist/internal/thumb"
	"newlist/internal/user"
	"newlist/internal/util"
)

// workers 预载并发度：每个 worker 一次处理一个文件（探测/下载都是网络阻塞，
// thumb 与 media 内部还各有自己的并发闸，这里保守取 4）。
const workers = 4

// SnoozeFor 是「不是现在」的推迟时长，到点自动继续。
const SnoozeFor = 24 * time.Hour

// snoozeKey 是推迟到点时刻在 settings 里的键，重启后据此恢复计时。
const snoozeKey = "preload_snooze_until"

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
	Snoozed    bool   `json:"snoozed"`   // 已点「不是现在」，推迟中
	ResumeAt   string `json:"resume_at"` // 推迟到点、自动继续的时刻（RFC3339）
	Pending    int64  `json:"pending"`   // 推迟时剩下多少项没预载
}

type Service struct {
	db     *sql.DB
	conf   *conf.Store
	fs     *fs.FS
	thumbs *thumb.Service
	media  *media.Service

	mu          sync.Mutex
	running     bool
	total       int64
	current     string
	errMsg      string
	finishedAt  string
	gen         int // 轮次代际：新一轮取代旧轮，旧 goroutine 靠比对 gen 停手
	cancel      context.CancelFunc
	snoozeUntil time.Time   // 非零 = 推迟中，到点自动继续
	timer       *time.Timer // 到点自动继续的定时器
	pending     []fileRow   // 推迟时未派发的剩余项，「继续」从这里接着跑

	done, covers, probes atomic.Int64
}

func New(db *sql.DB, cf *conf.Store, f *fs.FS, th *thumb.Service, md *media.Service) *Service {
	s := &Service{db: db, conf: cf, fs: f, thumbs: th, media: md}
	s.restoreSnooze()
	return s
}

func (s *Service) Progress() Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := Progress{
		Running: s.running, Total: s.total,
		Done: s.done.Load(), Covers: s.covers.Load(), Probes: s.probes.Load(),
		Current: s.current, Err: s.errMsg, FinishedAt: s.finishedAt,
	}
	if !s.snoozeUntil.IsZero() {
		p.Snoozed = true
		p.ResumeAt = s.snoozeUntil.UTC().Format(time.RFC3339)
		p.Pending = int64(len(s.pending))
	}
	return p
}

// Run 手动跑一轮后台预载：解除推迟，从头收集并取代正在进行的旧轮（幂等：已缓存的
// 封面/探测会快速跳过，只有新增或变更的文件才真正下载/探测）。立即返回。
func (s *Service) Run() {
	s.clearSnooze()
	s.start(nil, false)
}

// AutoRun 是自动预载入口（启动时、索引重建完成后）：推迟期内直接跳过，
// 并作废旧的剩余清单（索引已变），到点自动继续时从头跑一轮。
func (s *Service) AutoRun() {
	s.mu.Lock()
	until := s.snoozeUntil
	if !until.IsZero() {
		s.pending = nil
	}
	s.mu.Unlock()
	if !until.IsZero() {
		log.Printf("[preload] 预载推迟中，跳过本次自动预载（%s 后继续）",
			until.Local().Format("2006-01-02 15:04"))
		return
	}
	s.start(nil, false)
}

// Snooze 推迟预载：不再派发新文件（手头在跑的几项跑完即止），SnoozeFor 后自动继续；
// 期间自动触发的预载一律跳过。返回自动继续的时刻。
func (s *Service) Snooze() time.Time {
	until := time.Now().Add(SnoozeFor)
	s.mu.Lock()
	s.snoozeUntil = until
	s.armLocked(SnoozeFor)
	s.mu.Unlock()
	s.saveSnooze(until)
	log.Printf("[preload] 预载已推迟到 %s", until.Local().Format("2006-01-02 15:04"))
	return until
}

// Resume 结束推迟并接着跑：留有剩余清单就从那里继续，否则重跑整轮。
// 到点由定时器调用，用户点「继续」也走这里；未在推迟中则为空操作。
func (s *Service) Resume() {
	s.mu.Lock()
	if s.snoozeUntil.IsZero() {
		s.mu.Unlock()
		return // 已经继续过了（用户抢在定时器前点了继续，或反之）
	}
	s.snoozeUntil = time.Time{}
	s.disarmLocked()
	left := s.pending
	s.pending = nil
	draining := s.running // 上一轮还在排空，剩余清单尚未落定 → 重跑整轮
	s.mu.Unlock()
	s.saveSnooze(time.Time{})
	if draining || len(left) == 0 {
		s.start(nil, false)
		return
	}
	log.Printf("[preload] 继续预载：剩余 %d 项", len(left))
	s.start(left, true)
}

// Cleared 是一次缓存删除的结果（供后台反馈）。
type Cleared struct {
	Covers int64 `json:"covers"` // 删除的封面缓存文件数
	Bytes  int64 `json:"bytes"`  // 释放的磁盘空间
}

// Clear 停下预载并删除其全部产物：封面缓存文件 + 视频源信息缓存，进度归零。
// 推迟状态原样保留——删的是数据，不是「稍后再预载」的意愿。
func (s *Service) Clear() (Cleared, error) {
	s.stop()
	covers, bytes, err := s.thumbs.Purge()
	if err != nil {
		return Cleared{}, err
	}
	if err := s.media.PurgeInfo(); err != nil {
		return Cleared{}, err
	}
	return Cleared{Covers: covers, Bytes: bytes}, nil
}

// stop 停下当前轮并把进度复位到「从未预载」。gen++ 让旧轮的 finish/计数自行失效
// （见 finish 与 process 的 gen 判定），在途的几项跑完即止，不会写回状态。
// 剩余清单一并作废：计数已归零，接着跑会让进度对不上。
func (s *Service) stop() {
	s.mu.Lock()
	s.gen++
	if s.cancel != nil {
		s.cancel() // 取消在途的下载/探测
		s.cancel = nil
	}
	s.running = false
	s.current = ""
	s.errMsg = ""
	s.finishedAt = ""
	s.pending = nil
	s.total = 0
	s.done.Store(0)
	s.covers.Store(0)
	s.probes.Store(0)
	s.mu.Unlock()
}

// start 启动一轮预载并取代旧轮。resume=true 表示接着推迟时的剩余清单跑（沿用已有
// 计数），false 则全量重新收集并清零计数。立即返回，进度经 Progress 观察。
func (s *Service) start(files []fileRow, resume bool) {
	s.mu.Lock()
	s.gen++
	gen := s.gen
	if s.cancel != nil {
		s.cancel() // 取消旧轮的在途网络操作
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.running = true
	s.current = ""
	s.errMsg = ""
	s.pending = nil
	if !resume {
		s.total = 0
		s.done.Store(0)
		s.covers.Store(0)
		s.probes.Store(0)
	}
	s.mu.Unlock()
	go s.run(ctx, gen, files, resume)
}

func (s *Service) run(ctx context.Context, gen int, files []fileRow, resume bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[preload] 预载 panic: %v", r)
			s.finish(gen, nil, nil)
		}
	}()
	if !resume {
		files = s.collect()
		s.mu.Lock()
		if s.gen != gen {
			s.mu.Unlock()
			return
		}
		s.total = int64(len(files))
		s.mu.Unlock()
	}

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	i := 0
	for ; i < len(files); i++ {
		// 推迟不打断在途下载：只停止派发，手头几项跑完，files[i:] 留给「继续」
		if ctx.Err() != nil || !s.isCurrent(gen) || s.snoozed() {
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
			s.process(ctx, gen, fr)
			if s.isCurrent(gen) {
				s.done.Add(1)
			}
		}(files[i])
	}
	wg.Wait()
	s.finish(gen, ctx.Err(), files[i:])
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
// 计数只在本轮仍是当前轮时累加：Clear/新一轮已把计数归零，在途的这几项跑完再加
// 会让界面显示出刚被删掉的缓存（gen 判定同 setCurrent）。
func (s *Service) process(ctx context.Context, gen int, fr fileRow) {
	// 封面：远端盘下载落盘一份（宽度无关，各尺寸共用）；本地盘生成默认宽度。
	if _, file, err := s.thumbs.Get(ctx, admin, fr.path, 400); err == nil && file != "" && s.isCurrent(gen) {
		s.covers.Add(1)
	}
	// 视频源信息：direct 扩展名由 handler 按扩展名秒判，无需 ffprobe；其余探测并回写 media_info。
	if fr.extType == "video" && !media.IsDirectExt(fr.name) {
		fi := model.FileInfo{Name: fr.name, Size: fr.size, Modified: parseMod(fr.modified)}
		if _, err := s.media.Decide(ctx, admin, fr.path, fi); err == nil && s.isCurrent(gen) {
			s.probes.Add(1)
		}
	}
}

// finish 收尾本轮。left 非空 = 因推迟提前收手，把剩余清单交给「继续」，不记完成时间。
func (s *Service) finish(gen int, cerr error, left []fileRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		return // 已被新一轮取代，不覆盖其状态
	}
	s.running = false
	s.current = ""
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if cerr != nil && cerr != context.Canceled {
		s.errMsg = util.Humanize(cerr)
		log.Printf("[preload] 预载失败: %v", cerr)
	}
	if len(left) > 0 {
		s.pending = left
		log.Printf("[preload] 预载已停下：剩余 %d 项待继续", len(left))
		return
	}
	s.finishedAt = time.Now().UTC().Format(time.RFC3339)
	log.Printf("[preload] 预载完成：封面 %d / 探测 %d / 共 %d 项",
		s.covers.Load(), s.probes.Load(), s.total)
}

func (s *Service) isCurrent(gen int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen == gen
}

func (s *Service) snoozed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.snoozeUntil.IsZero()
}

// clearSnooze 解除推迟且不接着跑（手动「重新预载」走这条：整轮重来，剩余清单作废）。
func (s *Service) clearSnooze() {
	s.mu.Lock()
	if s.snoozeUntil.IsZero() {
		s.mu.Unlock()
		return
	}
	s.snoozeUntil = time.Time{}
	s.disarmLocked()
	s.pending = nil
	s.mu.Unlock()
	s.saveSnooze(time.Time{})
}

// armLocked / disarmLocked 管到点自动继续的定时器，调用方须持 s.mu。
func (s *Service) armLocked(d time.Duration) {
	s.disarmLocked()
	s.timer = time.AfterFunc(d, s.Resume)
}

func (s *Service) disarmLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// saveSnooze 持久化推迟到点（零值 = 清除），让推迟跨重启仍然有效。
func (s *Service) saveSnooze(t time.Time) {
	if s.conf == nil {
		return
	}
	v := ""
	if !t.IsZero() {
		v = t.UTC().Format(time.RFC3339)
	}
	if err := s.conf.Set(snoozeKey, v); err != nil {
		log.Printf("[preload] 保存推迟状态失败: %v", err)
	}
}

// restoreSnooze 恢复重启前的推迟：未到点则接着计时，已到点（含关机期间到点）则清除，
// 启动时的自动预载照常跑。仅 New 内调用，此时 Service 尚未共享，无需加锁。
func (s *Service) restoreSnooze() {
	if s.conf == nil {
		return
	}
	v := s.conf.Get(snoozeKey, "")
	if v == "" {
		return
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil || !t.After(time.Now()) {
		s.saveSnooze(time.Time{})
		return
	}
	s.snoozeUntil = t
	s.armLocked(time.Until(t))
	log.Printf("[preload] 预载推迟中，%s 后继续", t.Local().Format("2006-01-02 15:04"))
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
