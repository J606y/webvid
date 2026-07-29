// Package preload 在索引就绪后于后台批量预热媒体：探测视频源信息（写入
// media_info 持久缓存）+ 下载/生成封面缩略图落盘。仅处理挂载已勾选
// 「在视频库/照片墙展示」的可见媒体，让首次浏览即命中缓存、无需现场探测云盘。
package preload

import (
	"context"
	"database/sql"
	"errors"
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

// defaultWorkers 预载并发度的兜底值（conf 缺席时用，如测试）。实际取 conf.PreloadWorkers，
// 每个 worker 一次处理一个文件；真正吃 CPU 的 ffmpeg/ffprobe 另有 media/thumb 的总闸把关。
const defaultWorkers = 2

// workers 本轮派几个 worker。每轮开跑时读一次设置，改完不必重启。
func (s *Service) workers() int {
	if s.conf == nil {
		return defaultWorkers
	}
	return s.conf.PreloadWorkers()
}

// coverWidth 预载封面的请求宽度：远端盘下载的那份与宽度无关、各尺寸共用，
// 本地盘按此宽度生成。判缓存与真下载必须用同一个宽度，否则本地盘会判空。
const coverWidth = 400

// SnoozeFor 是「不是现在」的推迟时长，到点自动继续。
const SnoozeFor = 24 * time.Hour

// AutoEvery 是后台预载的兜底周期。预载本身只在启动与索引全量重建完成后触发，
// 而日常新增（上传、复制、云端那边直接放进去的）走的是索引增量更新，不带这一步——
// 没有这个周期性的一轮，新文件的封面就只能等浏览到它时当场加载，看着像「后台不干活」。
// 远端封面缓存 30 天到期后的刷新同理，也靠这一轮兜。
// 代价很低：全已缓存时 collect 阶段就全部跳过，一次网络请求都不发。
const AutoEvery = 6 * time.Hour

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
	// CoverTotal / ProbeTotal 是「本该有多少」：能出封面的媒体数、需要探测的视频数
	// （mp4 一类浏览器直接能播的不需要探测，不计入）。没有分母的话，光看
	// 「封面 2952」对不上索引的五万条，只能靠猜。
	CoverTotal int64 `json:"cover_total"`
	ProbeTotal int64 `json:"probe_total"`
	// Failed / FailNote 本轮没能取到封面或源信息的项数与头一条原因。这类失败往往秒回
	// （云盘不给缩略图、抽帧崩了、探测连不上），只记日志的话界面上只剩「跑完了」，
	// 用户看到的就是「后台好像没干活」——必须摆到卡片上。
	Failed    int64 `json:"failed"`
	FailNote  string `json:"fail_note"`
	WillRetry bool   `json:"will_retry"` // 已排好下一次重试（失败项会再试一遍）
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
	coverTotal  int64 // 能出封面的媒体数（分母）
	probeTotal  int64 // 需要探测的视频数（分母）
	current     string
	errMsg      string
	finishedAt  string
	gen         int // 轮次代际：新一轮取代旧轮，旧 goroutine 靠比对 gen 停手
	cancel      context.CancelFunc
	snoozeUntil time.Time   // 非零 = 推迟中，到点自动继续
	timer       *time.Timer // 到点自动继续的定时器
	pending     []fileRow   // 推迟时未派发的剩余项，「继续」从这里接着跑
	failNote    string      // 本轮头一条失败原因（人话），后续同类不再覆盖
	// 失败重试：启动那一轮常常撞上云盘驱动还没连上，整轮秒败。不重试的话封面就永远
	// 只能等浏览时当场生成——用户看到的就是「后台不干活，打开网页才加载」。
	retryTimer *time.Timer
	retryIn    time.Duration // 下次重试的间隔，每失败一轮翻倍，见 armRetryLocked
	// 索引变动后的补跑：一次目录复制会连着来上千次通知，攒一攒再跑，且绝不打断在跑的一轮。
	dirty      bool
	schedTimer *time.Timer

	done, covers, probes, failed atomic.Int64
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
		Failed: s.failed.Load(), FailNote: s.failNote,
		CoverTotal: s.coverTotal, ProbeTotal: s.probeTotal,
		WillRetry: s.retryTimer != nil,
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
		go s.countCached() // 不预载，但后台卡片仍要显示真实的缓存量
		return
	}
	s.start(nil, false)
}

// StartAuto 开始周期性兜底预载，每 every 跑一轮，直到 ctx 结束。
// 上一轮还在跑就跳过这次——大库首轮可能跑几个钟头，打断了重来永远跑不完。
// 推迟期内由 AutoRun 自行跳过（只补一次缓存计数）。
func (s *Service) StartAuto(ctx context.Context, every time.Duration) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if s.Progress().Running {
					continue
				}
				s.AutoRun()
			}
		}
	}()
}

// retryFirst / scheduleDelay 见 armRetryLocked / Schedule。
const retryFirst = 2 * time.Minute

// scheduleDelay 索引变动后的防抖窗口（var 而非 const：测试要调短它）。
var scheduleDelay = 30 * time.Second

// Schedule 索引有变动时调（上传、复制、新挂载扫完）：攒一会儿再补跑一轮。
// 一次目录复制会连着通知上千次，所以要防抖；手头正跑着就等它跑完，绝不打断——
// 打断了大库那一轮永远跑不到头。
func (s *Service) Schedule() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirty = true
	s.armScheduleLocked()
}

func (s *Service) armScheduleLocked() {
	if s.schedTimer != nil {
		s.schedTimer.Stop()
	}
	s.schedTimer = time.AfterFunc(scheduleDelay, s.fireSchedule)
}

func (s *Service) fireSchedule() {
	s.mu.Lock()
	s.schedTimer = nil
	switch {
	case !s.dirty:
		s.mu.Unlock()
		return
	case !s.snoozeUntil.IsZero():
		s.mu.Unlock() // 推迟中：dirty 留着，到点 Resume 会整轮重收
		return
	case s.running:
		s.armScheduleLocked() // 手头正忙，等会儿再看
		s.mu.Unlock()
		return
	}
	s.dirty = false
	s.mu.Unlock()
	s.AutoRun()
}

// armRetryLocked 排下一次重试：2 分钟起，每失败一轮翻倍，最长到 AutoEvery 封顶。
// 调用方须持 s.mu。
func (s *Service) armRetryLocked() {
	s.disarmRetryLocked()
	if s.retryIn == 0 {
		s.retryIn = retryFirst
	} else if s.retryIn < AutoEvery {
		s.retryIn *= 2
	}
	if s.retryIn > AutoEvery {
		s.retryIn = AutoEvery
	}
	d := s.retryIn
	s.retryTimer = time.AfterFunc(d, func() {
		s.mu.Lock()
		s.retryTimer = nil
		busy := s.running || !s.snoozeUntil.IsZero()
		s.mu.Unlock()
		if busy {
			return
		}
		s.AutoRun()
	})
	log.Printf("[preload] 本轮有没做成的，%s 后自动重试", d)
}

func (s *Service) disarmRetryLocked() {
	if s.retryTimer != nil {
		s.retryTimer.Stop()
		s.retryTimer = nil
	}
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
	s.disarmRetryLocked()
	s.retryIn = 0
	s.running = false
	s.current = ""
	s.errMsg = ""
	s.finishedAt = ""
	s.pending = nil
	s.failNote = ""
	s.total, s.coverTotal, s.probeTotal = 0, 0, 0
	s.done.Store(0)
	s.covers.Store(0)
	s.probes.Store(0)
	s.failed.Store(0)
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
	s.disarmRetryLocked() // 这一轮就是重试，旧的定时器作废
	if !resume {
		s.failNote = ""
		s.total, s.coverTotal, s.probeTotal = 0, 0, 0
		s.done.Store(0)
		s.covers.Store(0)
		s.probes.Store(0)
		s.failed.Store(0)
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
		// 清点要挨个查缓存（几万条媒体要几秒），先把话说在前头，别让卡片空着一个 0%
		s.setCurrent(gen, "清点已缓存的部分…")
		t := s.collect()
		files = t.todo
		s.mu.Lock()
		if s.gen != gen {
			s.mu.Unlock()
			return
		}
		s.total = int64(len(files))
		s.coverTotal, s.probeTotal = t.coverTotal, t.probeTotal
		s.mu.Unlock()
		// 计数基线：已缓存的那部分先记上，之后每做成一件再加一。卡片显示的始终是
		// 「现在缓存了多少」，不是「这一轮跑了多少」。
		s.covers.Store(t.covers)
		s.probes.Store(t.probes)
	}

	sem := make(chan struct{}, s.workers())
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
	// needCover / needProbe：这一项各自还有没有真活要干，由 collect 就地判定。
	// 派活时照此只做缺的那份，计数也只在真做了的那份上加，不会把已缓存的重复计进去。
	needCover, needProbe bool
}

// visible 取全部可见的视频/图片文件：按最长前缀归属挂载，挂载对应界面开关
// 关闭则跳过（视频→show_video、图片→show_photo）。只做可见性过滤，不碰缓存。
func (s *Service) visible() []fileRow {
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

// tally 是一次收集的结果：待办清单 + 卡片要显示的四个数。
type tally struct {
	todo       []fileRow
	covers     int64 // 已缓存封面数
	coverTotal int64 // 能出封面的媒体数（分母）
	probes     int64 // 已探测视频数
	probeTotal int64 // 需要探测的视频数（分母；mp4 一类不需要，不计入）
}

// collect 在可见媒体里分出「还有活要干的」与「已经缓存好的」。
//
// todo 只含确实还要下载/探测的项：已缓存的、以及做也白做的（驱动出不了封面、
// 服务器没有 ffmpeg/ffprobe）一律不进——它们会被瞬间跳过，算进总数只会让进度条
// 一开局就停在「已缓存占比」上，剩下的真活全挤在最后一小截里。
// covers/probes 是此刻真实的缓存量，用作计数基线，卡片据此显示「已缓存多少」。
func (s *Service) collect() tally {
	var t tally
	for _, fr := range s.visible() {
		switch s.thumbs.Cover(admin, fr.path, coverWidth) {
		case thumb.CoverReady:
			t.covers++
			t.coverTotal++
		case thumb.CoverPending:
			fr.needCover = true
			t.coverTotal++
		}
		switch s.media.ProbeStatus(fr.path, model.FileInfo{
			Name: fr.name, Size: fr.size, Modified: parseMod(fr.modified)}) {
		case media.ProbeReady:
			t.probes++
			t.probeTotal++
		case media.ProbePending:
			fr.needProbe = true
			t.probeTotal++
		}
		if fr.needCover || fr.needProbe {
			t.todo = append(t.todo, fr)
		}
	}
	return t
}

// countCached 只数「现在缓存了多少」，一件活也不派。推迟期间不预载，但卡片仍要显示
// 真实的封面/源信息缓存量——计数是内存态，不数一遍的话重启后会一直显示 0。
func (s *Service) countCached() {
	s.mu.Lock()
	gen, running := s.gen, s.running
	s.mu.Unlock()
	if running {
		return
	}
	t := s.collect()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen || s.running {
		return // 期间开了新一轮，它自己会写计数，别覆盖
	}
	s.coverTotal, s.probeTotal = t.coverTotal, t.probeTotal
	s.covers.Store(t.covers)
	s.probes.Store(t.probes)
}

// process 预热单个文件：只做 collect 判定还缺的那份（封面 / 源信息），已缓存的不再碰——
// 重复调一遍虽是缓存命中，但计数会把已经计进基线的那份再加一次。
// 计数只在本轮仍是当前轮时累加：Clear/新一轮已把计数归零，在途的这几项跑完再加
// 会让界面显示出刚被删掉的缓存（gen 判定同 setCurrent）。
func (s *Service) process(ctx context.Context, gen int, fr fileRow) {
	// 封面：远端盘下载落盘一份（宽度无关，各尺寸共用）；本地盘生成默认宽度。
	// file 为空 = 只拿到一次性直链、没能落盘，对预载而言等同没做成。
	if fr.needCover {
		_, file, err := s.thumbs.Get(ctx, admin, fr.path, coverWidth)
		switch {
		case err == nil && file != "":
			if s.isCurrent(gen) {
				s.covers.Add(1)
			}
		default:
			s.noteFail(gen, ctx, "取不到封面", err)
		}
	}
	// 视频源信息：direct 扩展名由 handler 按扩展名秒判，无需 ffprobe；其余探测并回写 media_info。
	if fr.needProbe {
		fi := model.FileInfo{Name: fr.name, Size: fr.size, Modified: parseMod(fr.modified)}
		if _, err := s.media.Decide(ctx, admin, fr.path, fi); err == nil {
			if s.isCurrent(gen) {
				s.probes.Add(1)
			}
		} else {
			s.noteFail(gen, ctx, "探不到视频源信息", err)
		}
	}
}

// noteFail 记一笔「这一项没做成」：计数 + 留下头一条原因（人话，卡片直接显示，
// 不再加前缀——底层给的已经是一句完整的话）。取消导致的失败不算：那是用户点了
// 推迟/删除缓存，不是这一项本身有问题。
func (s *Service) noteFail(gen int, ctx context.Context, fallback string, err error) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		return
	}
	s.failed.Add(1)
	if s.failNote == "" {
		s.failNote = fallback
		if err != nil {
			s.failNote = util.Humanize(err)
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
	log.Printf("[preload] 预载完成：本轮做了 %d 项，已缓存封面 %d / 源信息 %d",
		s.total, s.covers.Load(), s.probes.Load())
	// 有没做成的就排一次重试。最常见的一幕：进程刚起、云盘驱动还没连上，整轮秒败——
	// 不重试的话这些封面就永远只能等浏览时当场生成。全做成了则把退避清零。
	if s.failed.Load() > 0 {
		s.armRetryLocked()
	} else {
		s.retryIn = 0
	}
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

// scheduleDelayForTest 把防抖窗口调短并返回原值，仅供测试使用。
func scheduleDelayForTest(d time.Duration) time.Duration {
	old := scheduleDelay
	scheduleDelay = d
	return old
}
