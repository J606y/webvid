package media

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"newlist/internal/auth"
	"newlist/internal/driver"
	"newlist/internal/fs"
	"newlist/internal/model"
	"newlist/internal/user"
	"newlist/internal/util"
)

const (
	segLen      = 4.0             // 秒/分片
	maxSessions = 2               // 转码会话并发上限（PLAN：并发 ≤2，超出驱逐最久未用）
	idleTTL     = 5 * time.Minute // 空闲回收
	waitAhead   = 12              // vod 模式：请求分片超出当前进度 N 个以内等待，否则 -ss 重启

	// maxDuration 播放列表按时长展开的上限。分片数直接由 ffprobe 报的时长算出，
	// 而畸形/损坏文件报出个荒谬的时长是常事——vodPlaylist 会照单全收，一次请求
	// 就拼出一个几 GB 的字符串。24 小时已是 21600 个分片，再长的一律按 24 小时算。
	maxDuration = 24 * 3600.0
)

// Service 是 HLS 转码会话管理器。
//
// 两种会话模式（按探测决策自动选择）：
//   - event（视频流 -c copy：纯 remux 或只转音频）：ffmpeg 全速跑完整个文件并自写
//     EVENT 播放列表，播放器随列表增长解锁进度条，跑完出 ENDLIST 变成完整 VOD——
//     copy 远快于实时，秒级~分钟级即全片可拖；不做 -ss 重启（无需对齐关键帧）。
//   - vod（视频重编码）：服务端按探测时长直接生成完整 VOD 播放列表（4s 等分），
//     ffmpeg 用 force_key_frames 对齐分片边界 + output_ts_offset 让时间戳落在
//     绝对时间轴上；播放器原生全片可拖，拖到未生成处由分片请求触发 -ss 重启。
type Service struct {
	fs      *fs.FS
	root    string // data/transcode
	baseURL string // 本机回环地址；云盘输入经 /api/raw 中转（复用鉴权/直链重取/代理加速）
	secret  []byte
	// internalToken 标识本进程内部回环请求（ffmpeg/ffprobe 拉 /api/raw）；
	// server 下载限速器仅凭此头豁免，不再信来源 IP（反代后来源恒为回环）。
	internalToken string
	db            *sql.DB // media_info 持久探测缓存（nil = 仅内存缓存，测试用）

	ffOnce            sync.Once
	ffmpegP, ffprobeP string

	// jobs 是 ffmpeg/ffprobe 的总闸：抽封面与探源信息共用（转码播放不受此限——
	// 那是用户正在等的前台活）。后台可调，见 conf.MediaJobs。
	jobs *util.Gate

	// stop 关停 janitor；stopOnce 保证 Close 可重复调用。
	stop     chan struct{}
	stopOnce sync.Once

	mu       sync.Mutex
	sessions map[string]*session
	probes   map[string]Decision
	flight   map[string]*probeWait // 在途探测，同一文件只跑一趟 ffprobe
	stats    map[string]statEntry  // 短时文件信息，见 rememberStat
}

// probeWait 是一次在途探测：后到的调用等它的结论，不再各占一个闸位。
type probeWait struct {
	done chan struct{}
	d    Decision
	err  error
}

// statEntry 是刚问过云盘的文件信息。播一部片子要连着三次拿同一份 size/mtime
// （/video/info、建会话、拉流），在云盘上每次都是一发网络请求。
type statEntry struct {
	fi model.FileInfo
	at time.Time
}

// statTTL 文件信息的复用时限：只覆盖「点开视频」这一串连续动作，超时即回落真去问。
const statTTL = 2 * time.Minute

// maxLead vod 模式下转码最多领先播放位置几个分片（150×4s = 10 分钟）。
// var 以便测试调小，见 paceLeadFrom。
var maxLead = 150

// SetGate 换用外部传入的 ffmpeg 总闸，仅供启动接线（此时尚无并发）。
// 与 thumb 各建一把闸的话，conf.MediaJobs 写着 N，实际能同时跑的 ffmpeg 是 2N。
func (s *Service) SetGate(g *util.Gate) { s.jobs = g }

func New(f *fs.FS, dataDir, baseURL string, secret []byte, db *sql.DB) *Service {
	root := filepath.Join(dataDir, "transcode")
	os.RemoveAll(root) // 会话均为进程内状态，启动清理上次残留
	os.MkdirAll(root, 0o755)
	s := &Service{fs: f, root: root, baseURL: baseURL, secret: secret,
		internalToken: auth.RandomPassword(32), db: db,
		sessions: map[string]*session{}, probes: map[string]Decision{},
		flight: map[string]*probeWait{}, stats: map[string]statEntry{},
		stop: make(chan struct{}),
		jobs: util.NewGate(2)} // 保守默认，main 随后按 conf.MediaJobs 调整
	go s.janitor()
	return s
}

// InternalToken 返回本进程内部回环请求（ffmpeg/ffprobe 拉 /api/raw）的鉴权令牌，
// 供 server 下载限速器识别并豁免——反代后来源 IP 均为回环，不能再靠 IP 判定。
func (s *Service) InternalToken() string { return s.internalToken }

func (s *Service) tools() (ffmpeg, ffprobe string) {
	s.ffOnce.Do(func() {
		s.ffmpegP, s.ffprobeP = LookTool("ffmpeg"), LookTool("ffprobe")
		if s.ffmpegP == "" || s.ffprobeP == "" {
			log.Println("[media] 未找到 ffmpeg/ffprobe，转码播放不可用（可设 NL_FFMPEG / NL_FFPROBE 指定路径）")
		}
	})
	return s.ffmpegP, s.ffprobeP
}

// input 给 ffmpeg/ffprobe 的输入：本地盘用宿主机绝对路径；
// 云盘走本地回环 /api/raw（直链过期由 raw 层每次重取解决，代理加速同样生效）。
func (s *Service) input(u *user.User, logical string) (string, error) {
	drv, rel, err := s.fs.Driver(u, logical)
	if err != nil {
		return "", err
	}
	if lp, ok := drv.(driver.LocalPather); ok {
		return lp.AbsPath(rel)
	}
	// 回环 /api/raw 挂在鉴权组下，令牌里的用户 ID 会被中间件回查 users 表，而自增主键
	// 从 1 起——ID 不合法就是一次注定 401 的请求。发出去只会让「身份配错了」以
	// 「云盘连不上」的面目回到调用方，无从查起，所以这里当场说清。
	if u == nil || u.ID <= 0 {
		return "", fmt.Errorf("内部回环缺少有效的用户身份，读不了云盘上的文件")
	}
	tok, _, err := auth.SignToken(u.ID, s.secret)
	if err != nil {
		return "", err
	}
	return s.baseURL + "/api/raw" + encodePath(logical) + "?token=" + url.QueryEscape(tok), nil
}

func encodePath(p string) string {
	segs := strings.Split(p, "/")
	for i, sg := range segs {
		segs[i] = url.PathEscape(sg)
	}
	return strings.Join(segs, "/")
}

// FrameFirst 取输入的第一帧写入 out（缩放到宽 width 的 JPEG），不做任何定位。
// 静态图片（JPEG/PNG 等单帧输入）必须走这个：单帧输入上任何定位参数都会把唯一那一帧
// 丢掉，而 ffmpeg 退出码仍是 0，只是什么也没输出。
func FrameFirst(ctx context.Context, ff, in, out string, width int) error {
	return FrameAt(ctx, ff, in, out, width, "", "")
}

// FrameAt 用 ffmpeg 抽取视频某一帧写入 out（缩放到宽 width 的 JPEG）。依次尝试 offsets 中
// 各秒点（如 "3","0"：先 3s 处、失败回退取首帧），第一个产出非空帧的即成功；全部失败返回最后错误。
// 偏移传空串表示不定位，直接取输入的第一帧——静态图片（单帧输入）必须这样调，见 try。
// in 为本地绝对路径或 http(s) 直链——http 输入自动挂 -reconnect 续传与 X-Internal-Auth 头
// （见 httpInputArgs），本地输入这些旗标为空、无副作用，故一份代码兼容云盘回环抽帧与本地抽帧。
// ff 为 ffmpeg 可执行路径，internalToken 为回环鉴权令牌（本地或无令牌传 ""）。
func FrameAt(ctx context.Context, ff, in, out string, width int, internalToken string, offsets ...string) error {
	tmp := out + ".vf.tmp.jpg"
	defer os.Remove(tmp)
	try := func(ss string) error {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		// -threads 1：抽一帧而已，默认多线程解码会吃满所有核心（并发另有 jobs 闸把关）
		a := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-threads", "1"}
		// 单帧输入（JPEG/PNG 等静态图）上任何定位都会把唯一那一帧丢掉：ffmpeg 退出码仍是 0，
		// 只是什么也没输出——连 -ss 0 都会。所以偏移为空时一个定位参数都不加。
		if ss != "" {
			a = append(a, "-ss", ss)
		}
		a = append(a, httpInputArgs(in, internalToken)...)
		a = append(a, "-i", in, "-frames:v", "1",
			"-vf", fmt.Sprintf("scale=%d:-2", width), "-q:v", "5", "-y", tmp)
		if err := exec.CommandContext(cctx, ff, a...).Run(); err != nil {
			return err
		}
		if st, err := os.Stat(tmp); err != nil || st.Size() == 0 {
			return fmt.Errorf("ffmpeg 未产出帧")
		}
		return nil
	}
	lastErr := fmt.Errorf("未指定抽帧偏移")
	for _, ss := range offsets {
		if lastErr = try(ss); lastErr == nil {
			return os.Rename(tmp, out)
		}
	}
	return lastErr
}

// Decide 探测文件并给出播放决策（结果按 路径+size+mtime 内存缓存）。
// 后台优先级，供预载调用；用户正在等的那次探测走 decideNow(…, true)。
func (s *Service) Decide(ctx context.Context, u *user.User, logical string, fi model.FileInfo) (Decision, error) {
	return s.decideNow(ctx, u, logical, fi, false)
}

// decideNow 探测并给出决策。foreground=true 表示有人正盯着屏幕等，闸上插队（见 util.Gate）。
//
// 同一文件的并发探测只跑一趟：详情卡与播放页会同时来问、用户连点两下也会，
// 各探各的就是两个 ffprobe 各占一个闸位，而闸总共才两个。
func (s *Service) decideNow(ctx context.Context, u *user.User, logical string, fi model.FileInfo, foreground bool) (Decision, error) {
	_, ffprobe := s.tools()
	if ffprobe == "" {
		return Decision{}, ErrNoFFmpeg
	}
	key := logical + "|" + strconv.FormatInt(fi.Size, 10) + "|" + strconv.FormatInt(fi.Modified.UnixNano(), 10)
	for {
		s.mu.Lock()
		d, hit := s.probes[key]
		s.mu.Unlock()
		if hit {
			return d, nil
		}

		// 持久层（预载已探测 / 上次会话回写）：size+modified 未变即复用，免云盘现场探测。
		if d, ok := s.loadInfo(logical, fi); ok {
			s.cacheMem(key, d)
			return d, nil
		}

		w, lead := s.beginProbe(key)
		if lead {
			d, err := s.runDecide(ctx, u, logical, fi, key, foreground)
			s.endProbe(key, w, d, err)
			return d, err
		}
		select {
		case <-w.done:
		case <-ctx.Done():
			return Decision{}, ctx.Err()
		}
		if !errors.Is(w.err, context.Canceled) {
			return w.d, w.err
		}
		// 领跑的那次是被它自己的 ctx 掐掉的（本次的还活着，上面的 select 已验过），
		// 重来一趟：要么这次命中缓存，要么自己领跑。
	}
}

// runDecide 真跑一次 ffprobe 并落两级缓存。
func (s *Service) runDecide(ctx context.Context, u *user.User, logical string, fi model.FileInfo, key string, foreground bool) (Decision, error) {
	input, err := s.input(u, logical)
	if err != nil {
		return Decision{}, err
	}
	// 排队等 ffprobe 名额：缓存全落空时（如刚建完索引）这里会同时涌进成百上千个探测请求。
	// 前台插队，否则点开一个视频要等后台预载手头那两个跨国大文件探完，最坏 45 秒。
	acquire := s.jobs.Acquire
	if foreground {
		acquire = s.jobs.AcquirePriority
	}
	release, err := acquire(ctx)
	if err != nil {
		return Decision{}, err
	}
	defer release()
	_, ffprobe := s.tools()
	po, err := runProbe(ctx, ffprobe, input, s.internalToken)
	if err != nil {
		return Decision{}, err
	}
	d := decide(po)
	s.cacheMem(key, d)
	s.saveInfo(logical, fi, d)
	return d, nil
}

// beginProbe 登记一次探测，lead=true 表示由本次调用负责真去跑。
func (s *Service) beginProbe(key string) (*probeWait, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flight == nil { // 测试可能不经 New 直接构造
		s.flight = map[string]*probeWait{}
	}
	if w, ok := s.flight[key]; ok {
		return w, false
	}
	w := &probeWait{done: make(chan struct{})}
	s.flight[key] = w
	return w, true
}

// endProbe 交出结论并唤醒等待者。等待者只在 done 关闭之后读 d/err，故此处赋值安全。
func (s *Service) endProbe(key string, w *probeWait, d Decision, err error) {
	s.mu.Lock()
	delete(s.flight, key)
	s.mu.Unlock()
	w.d, w.err = d, err
	close(w.done)
}

// rememberStat 记下刚问到的文件信息，供紧接着的建会话复用（见 statEntry）。
func (s *Service) rememberStat(logical string, fi model.FileInfo) {
	s.mu.Lock()
	if len(s.stats) > 512 {
		s.stats = map[string]statEntry{}
	}
	s.stats[logical] = statEntry{fi: fi, at: time.Now()}
	s.mu.Unlock()
}

// recallStat 取回 statTTL 内记下的文件信息。
func (s *Service) recallStat(logical string) (model.FileInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.stats[logical]
	if !ok || time.Since(e.at) > statTTL {
		return model.FileInfo{}, false
	}
	return e.fi, true
}

func (s *Service) cacheMem(key string, d Decision) {
	s.mu.Lock()
	if len(s.probes) > 512 {
		s.probes = map[string]Decision{}
	}
	s.probes[key] = d
	s.mu.Unlock()
}

// loadInfo 从 media_info 读探测缓存；size/modified 必须与当前文件一致才算命中。
func (s *Service) loadInfo(logical string, fi model.FileInfo) (Decision, bool) {
	if s.db == nil {
		return Decision{}, false
	}
	var (
		d                            Decision
		vc, vh, ac, aa, hv, ha, size int64
		w, h                         int64
	)
	mod := modKey(fi.Modified)
	err := s.db.QueryRow(
		`SELECT size, video_copy, video_hevc, audio_copy, audio_aac, has_video, has_audio, duration,
		        video_codec, width, height, fps, bitrate FROM media_info
		 WHERE path=? AND modified=?`, logical, mod).
		Scan(&size, &vc, &vh, &ac, &aa, &hv, &ha, &d.Duration,
			&d.VideoCodec, &w, &h, &d.FPS, &d.BitRate)
	if err != nil || size != fi.Size {
		return Decision{}, false
	}
	d.VideoCopy, d.AudioCopy, d.AudioAAC, d.HasVideo, d.HasAudio = vc == 1, ac == 1, aa == 1, hv == 1, ha == 1
	d.VideoHEVC = int(vh)
	d.Width, d.Height = int(w), int(h)
	return d, true
}

// saveInfo 把探测决策写入 media_info（覆盖旧记录）。失败仅记日志，不影响播放。
func (s *Service) saveInfo(logical string, fi model.FileInfo, d Decision) {
	if s.db == nil {
		return
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO media_info
		 (path,size,modified,video_copy,video_hevc,audio_copy,audio_aac,has_video,has_audio,duration,
		  video_codec,width,height,fps,bitrate,probed_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		logical, fi.Size, modKey(fi.Modified),
		util.BoolInt(d.VideoCopy), d.VideoHEVC,
		util.BoolInt(d.AudioCopy), util.BoolInt(d.AudioAAC), util.BoolInt(d.HasVideo), util.BoolInt(d.HasAudio),
		d.Duration, d.VideoCodec, d.Width, d.Height, d.FPS, d.BitRate,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		log.Printf("[media] media_info 写入失败 %s: %v", logical, err)
	}
}

// PurgeInfo 清空视频源信息缓存：内存决策缓存 + media_info 全表。
// 在播的转码会话不受影响——会话建立时决策已取到手，运行期不再回查缓存。
func (s *Service) PurgeInfo() error {
	s.mu.Lock()
	s.probes = map[string]Decision{}
	s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	if _, err := s.db.Exec(`DELETE FROM media_info`); err != nil {
		return err
	}
	log.Println("[media] 视频源信息缓存已删除")
	return nil
}

// modKey 把修改时间归一为 RFC3339（秒级，与 index 写入 files.modified 同格式）。
func modKey(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Playlist 返回 index.m3u8 内容（不存在则创建会话并启动 ffmpeg）。
// hevcCap 是客户端的 HEVC 解码能力，见 Decision.CopyWith。
func (s *Service) Playlist(ctx context.Context, u *user.User, logical string, hevcCap int) ([]byte, error) {
	sess, err := s.ensure(ctx, u, logical, hevcCap)
	if err != nil {
		return nil, err
	}
	return sess.playlist(ctx)
}

// Segment 等待并返回分片文件的本地路径（name ∈ init.mp4 | seg_N.m4s）。
func (s *Service) Segment(ctx context.Context, u *user.User, logical, name string, hevcCap int) (string, error) {
	sess, err := s.ensure(ctx, u, logical, hevcCap)
	if err != nil {
		return "", err
	}
	return sess.segment(ctx, name)
}

// sessionKey 会话表的键。带上客户端能力：同一部 HEVC 片子，能直出的设备拿到的是
// 原样封装的分片，不能直出的拿到的是 x264 重编码的，两者的分片内容与目录都不能混用。
func sessionKey(logical string, hevcCap int) string {
	return logical + "\x00" + strconv.Itoa(hevcCap)
}

// ensure 取现有会话或新建（Stat+probe 不持全局锁；同 key 并发创建以先入表者为准）。
func (s *Service) ensure(ctx context.Context, u *user.User, logical string, hevcCap int) (*session, error) {
	ffmpeg, _ := s.tools()
	if ffmpeg == "" {
		return nil, ErrNoFFmpeg
	}
	key := sessionKey(logical, hevcCap)
	s.mu.Lock()
	if sess := s.sessions[key]; sess != nil {
		s.mu.Unlock()
		sess.touch()
		return sess, nil
	}
	s.mu.Unlock()

	drv, rel, err := s.fs.Driver(u, logical) // 含视野/存在性校验（不发网络）
	if err != nil {
		return nil, err
	}
	// /video/info 刚问过同一个文件，几秒内不必再问云盘一次（见 statEntry）
	fi, ok := s.recallStat(logical)
	if !ok {
		if fi, err = drv.Stat(ctx, rel); err != nil {
			return nil, err
		}
		if fi.IsDir {
			return nil, driver.ErrNotFound
		}
		s.rememberStat(logical, fi)
	}
	raw, err := s.decideNow(ctx, u, logical, fi, true) // 用户正等着起播
	if err != nil {
		return nil, err
	}
	dec := raw.CopyWith(hevcCap)
	input, err := s.input(u, logical)
	if err != nil {
		return nil, err
	}

	h := sha1.Sum([]byte(key))
	id := hex.EncodeToString(h[:])[:16]
	sess := &session{
		svc:   s,
		key:   logical,
		dir:   filepath.Join(s.root, id),
		input: input,
		dec:   dec,
		vod:   dec.HasVideo && !dec.VideoCopy && dec.Duration > 0,
		nSegs: int(math.Ceil(math.Min(math.Max(dec.Duration, segLen), maxDuration) / segLen)),
	}

	s.mu.Lock()
	if exist := s.sessions[key]; exist != nil {
		s.mu.Unlock()
		exist.touch()
		return exist, nil
	}
	for len(s.sessions) >= maxSessions {
		var oldest *session
		var oldestKey string
		var oldestT time.Time
		for k, se := range s.sessions {
			se.mu.Lock()
			t := se.lastUsed
			se.mu.Unlock()
			if oldest == nil || t.Before(oldestT) {
				oldest, oldestKey, oldestT = se, k, t
			}
		}
		delete(s.sessions, oldestKey)
		log.Printf("[media] 会话数达上限，驱逐最久未用 %s", oldest.key)
		go oldest.destroy()
	}
	s.sessions[key] = sess
	s.mu.Unlock()

	if err := os.MkdirAll(sess.dir, 0o755); err != nil {
		s.mu.Lock()
		delete(s.sessions, key)
		s.mu.Unlock()
		return nil, err
	}
	sess.mu.Lock()
	sess.lastUsed = time.Now()
	sess.startLocked(0)
	err = sess.runErr
	sess.mu.Unlock()
	if err != nil {
		// 启动即失败：从会话表移除并清理目录，否则坏会话被缓存 idleTTL(5min)，
		// 期间该文件持续命中并返回旧错误，无法重试播放。
		s.mu.Lock()
		if s.sessions[key] == sess {
			delete(s.sessions, key)
		}
		s.mu.Unlock()
		go sess.destroy()
		return nil, err
	}
	return sess, nil
}

// janitor 定期回收空闲超时的会话，Close 时随之退出。
// 用 NewTicker 而非 time.Tick：后者的 Ticker 谁都拿不到、停不掉也回收不了。
func (s *Service) janitor() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.sweep()
		}
	}
}

func (s *Service) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, sess := range s.sessions {
		sess.mu.Lock()
		idle := time.Since(sess.lastUsed) > idleTTL
		sess.mu.Unlock()
		if idle {
			delete(s.sessions, k)
			log.Printf("[media] 回收空闲转码会话 %s", sess.key)
			go sess.destroy()
		}
	}
}

// Close 关停服务：终止所有在跑的 ffmpeg 并清理会话目录。由 main 在 HTTP 优雅关闭后调用，
// 避免转码进程变孤儿继续跑完、临时目录残留。
func (s *Service) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.mu.Lock()
	sessions := s.sessions
	s.sessions = map[string]*session{}
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.destroy()
	}
}

// ---- 会话 ----

type session struct {
	svc   *Service
	key   string // 逻辑路径（会话表键）
	dir   string
	input string
	dec   Decision
	vod   bool // true=服务端 VOD 列表（视频重编码分片对齐）；false=ffmpeg 自写 event 列表
	nSegs int  // vod 模式分片总数

	mu       sync.Mutex
	lastUsed time.Time
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	runDone  chan struct{} // 本轮进程 Wait 完成（destroy 等它退出后才删目录）
	runFrom  int           // 本轮 ffmpeg 起始分片
	cursor   int           // 从 runFrom 起已确认连续存在的最大分片号
	runErr   error         // ffmpeg 异常退出原因（人为 kill 不算）
	gen      int           // 换代计数：kill 时 +1，旧 Wait 回调不覆盖新态
	stderr   *tailBuf
}

func (sess *session) touch() {
	sess.mu.Lock()
	sess.lastUsed = time.Now()
	sess.mu.Unlock()
}

// startLocked 启动一轮 ffmpeg（须持 sess.mu；from 仅 vod 模式非 0）。
func (sess *session) startLocked(from int) {
	ctx, cancel := context.WithCancel(context.Background())
	sess.cancel = cancel
	sess.runFrom = from
	sess.cursor = from - 1
	sess.runErr = nil
	sess.gen++
	sess.stderr = &tailBuf{}
	gen := sess.gen
	done := make(chan struct{})
	sess.runDone = done

	cmd := exec.CommandContext(ctx, sess.svc.ffmpegP, sess.ffmpegArgs(from)...)
	cmd.Dir = sess.dir
	cmd.Stderr = sess.stderr
	if err := cmd.Start(); err != nil {
		sess.runErr = fmt.Errorf("ffmpeg 启动失败: %w", err)
		sess.cmd = nil
		cancel()
		close(done)
		return
	}
	sess.cmd = cmd
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[media] %s wait goroutine panic: %v", sess.key, r)
			}
		}()
		err := cmd.Wait()
		close(done)
		sess.mu.Lock()
		defer sess.mu.Unlock()
		if sess.gen != gen { // 已被 kill/新一轮替换
			return
		}
		sess.cmd = nil
		if err != nil && ctx.Err() == nil {
			log.Printf("[media] %s ffmpeg 退出: %v（%s）", sess.key, err, scrubToken(sess.stderr.String()))
			sess.runErr = fmt.Errorf("转码失败：该视频可能已损坏或格式不受支持")
		}
		cancel()
	}()
}

// killLocked 终止当前进程（须持 sess.mu）。
func (sess *session) killLocked() {
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.cmd = nil
	sess.gen++
}

// transcodeThreads 一路 x264 允许用几个核：核数的一半，至少 1。
// maxSessions=2，两路同时跑正好用满，机器不会因为看片而没法响应别的请求。
func transcodeThreads() int {
	return max(1, runtime.NumCPU()/2)
}

// destroy 终止进程并删除会话目录（等进程退出，Windows 下句柄未释放删不掉）。
func (sess *session) destroy() {
	sess.mu.Lock()
	done := sess.runDone
	sess.killLocked()
	sess.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
	if err := os.RemoveAll(sess.dir); err != nil {
		time.Sleep(2 * time.Second)
		os.RemoveAll(sess.dir) // 再试一次，仍失败留给下次启动清理
	}
}

func (sess *session) ffmpegArgs(from int) []string {
	off := float64(from) * segLen
	a := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if off > 0 {
		a = append(a, "-ss", fmt.Sprintf("%.3f", off))
	}
	// 云盘输入经本机 /api/raw 回环拉取：内部鉴权头（豁免下载限速+单流透传）
	// 及断线续传旗标，见 httpInputArgs。
	a = append(a, httpInputArgs(sess.input, sess.svc.internalToken)...)
	a = append(a, "-i", sess.input)
	if sess.dec.HasVideo {
		a = append(a, "-map", "0:v:0")
	}
	if sess.dec.HasAudio {
		a = append(a, "-map", "0:a:0")
	}
	if sess.dec.HasVideo {
		if sess.dec.VideoCopy {
			a = append(a, "-c:v", "copy")
			if sess.dec.videoTag != "" {
				a = append(a, "-tag:v", sess.dec.videoTag) // HEVC 直出需 hvc1，见 Decision.CopyWith
			}
		} else {
			// -threads：不写的话 libx264 按核数开满，一路转码就吃掉整机——转码不受
			// jobs 闸约束（那是用户正在等的前台活），唯一的约束就是这里。取核数一半，
			// 留一半给另一路会话、ffprobe/抽帧和 HTTP 本身。
			a = append(a, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
				"-threads", strconv.Itoa(transcodeThreads()),
				"-pix_fmt", "yuv420p",
				"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", int(segLen)))
		}
	}
	if sess.dec.HasAudio {
		// vod 模式恒转 aac：-ss 精确 seek 需解码丢帧，copy 的压缩包无法齐点裁切
		if sess.dec.AudioCopy && !sess.vod {
			a = append(a, "-c:a", "copy")
			if sess.dec.AudioAAC {
				// ts/m2ts 里的 AAC 是 ADTS 帧，直接 copy 进 fMP4 会 Malformed AAC
				// 起播即死；ASC 源（mkv/mp4）过此 bsf 直通无害，故 aac 恒挂
				a = append(a, "-bsf:a", "aac_adtstoasc")
			}
		} else {
			a = append(a, "-c:a", "aac", "-b:a", "192k", "-ac", "2")
		}
	}
	a = append(a,
		"-f", "hls",
		"-hls_time", strconv.Itoa(int(segLen)),
		"-hls_segment_type", "fmp4",
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", "seg_%d.m4s",
		"-hls_list_size", "0",
		"-hls_flags", "temp_file", // 分片写完才改名 → 文件存在即完整
	)
	if sess.vod {
		a = append(a, "-start_number", strconv.Itoa(from),
			"-output_ts_offset", fmt.Sprintf("%.3f", off))
	} else {
		a = append(a, "-hls_playlist_type", "event")
	}
	return append(a, "live.m3u8")
}

func (sess *session) playlist(ctx context.Context) ([]byte, error) {
	sess.touch()
	if sess.vod {
		return sess.vodPlaylist(), nil
	}
	// event 模式：轮询 ffmpeg 自写的列表就绪（≥1 分片）
	live := filepath.Join(sess.dir, "live.m3u8")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if b, err := os.ReadFile(live); err == nil && bytes.Contains(b, []byte("#EXTINF")) {
			return b, nil
		}
		sess.mu.Lock()
		rerr := sess.runErr
		sess.mu.Unlock()
		if rerr != nil {
			return nil, rerr
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("转码启动超时")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// vodPlaylist 由探测时长直接生成完整 VOD 列表（分片尚未生成也先列出，
// 请求到再等/重启 ffmpeg），播放器一开始就拿到完整时间轴。
func (sess *session) vodPlaylist() []byte {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", int(segLen))
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
	rem := math.Max(sess.dec.Duration, segLen)
	for i := 0; i < sess.nSegs; i++ {
		d := math.Min(segLen, rem)
		fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg_%d.m4s\n", d, i)
		rem -= d
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return []byte(b.String())
}

var segRe = regexp.MustCompile(`^seg_(\d+)\.m4s$`)

// tokenRe 匹配 URL 查询串里的 token 值，用于日志脱敏（回环 /api/raw 输入 URL 带 JWT）。
var tokenRe = regexp.MustCompile(`(?i)token=[^&\s"']+`)

// scrubToken 把 ffmpeg stderr 中的 ?token=<JWT> 替换为占位，防有效令牌进日志。
func scrubToken(s string) string {
	return tokenRe.ReplaceAllString(s, "token=REDACTED")
}

func (sess *session) segment(ctx context.Context, name string) (string, error) {
	sess.touch()
	fp := filepath.Join(sess.dir, name)
	if name == "init.mp4" {
		return sess.waitFile(ctx, fp, 20*time.Second)
	}
	m := segRe.FindStringSubmatch(name)
	if m == nil {
		return "", driver.ErrNotFound
	}
	n, _ := strconv.Atoi(m[1])
	if sess.vod && n >= sess.nSegs {
		return "", driver.ErrNotFound
	}
	if ready(fp) {
		if sess.vod {
			sess.paceLeadFrom(n)
		}
		return fp, nil
	}
	if !sess.vod {
		// event 模式列表里出现的分片必已落盘（temp_file），走到这里是竞态边界，短等
		return sess.waitFile(ctx, fp, 30*time.Second)
	}

	// vod 模式：进度窗口内等待，窗口外（含回拖到已驱逐区/进程已停）-ss 重启
	sess.mu.Lock()
	sess.advanceCursorLocked()
	if !sess.inWindowLocked(n) {
		done := sess.runDone
		sess.killLocked()
		sess.mu.Unlock()
		// kill 只是发信号，进程要一会儿才真退。等它退了再起新的：否则两路 x264 同时
		// 按 -threads 跑，瞬时把 CPU 翻倍，还会一起往同一批分片文件里写。
		waitClosed(done, 3*time.Second)
		sess.mu.Lock()
		if !sess.inWindowLocked(n) { // 期间可能已被并发的分片请求重新拉起
			sess.killLocked()
			sess.startLocked(n)
		}
	}
	rerr := sess.runErr
	sess.mu.Unlock()
	if rerr != nil {
		return "", rerr
	}
	return sess.waitFile(ctx, fp, 90*time.Second)
}

// inWindowLocked 第 n 个分片是否落在本轮 ffmpeg 够得着的进度窗口内（须持 sess.mu）。
func (sess *session) inWindowLocked(n int) bool {
	return sess.cmd != nil && n >= sess.runFrom && n <= sess.cursor+waitAhead
}

// paceLeadFrom 领先太多就把 ffmpeg 停下来。
//
// 不设上限时它会全速把整部片子转完：用户看了五分钟就退出，CPU 和云盘流量照样按整片
// 付账（event 模式的 -c copy 更是把整个文件拉了一遍），之后还要占着磁盘等 idleTTL 回收。
// 停下来不影响观看——已生成的分片就在盘上，播放位置追到边界时由上面的窗口判定原地
// -ss 续转。只对 vod 模式生效：event 模式的列表由 ffmpeg 自己写，中途停掉就再也接不上。
func (sess *session) paceLeadFrom(n int) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.cmd == nil {
		return
	}
	sess.advanceCursorLocked()
	if sess.cursor-n > maxLead {
		log.Printf("[media] %s 转码已领先播放位置 %d 个分片，先停下（追上来再续）", sess.key, sess.cursor-n)
		sess.killLocked()
	}
}

// waitClosed 等 ch 关闭，最多等 d。ch 为 nil 直接返回。
func waitClosed(ch chan struct{}, d time.Duration) {
	if ch == nil {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	}
}

// advanceCursorLocked 推进"已连续生成"游标（本轮从 runFrom 起顺序产出）。
func (sess *session) advanceCursorLocked() {
	for {
		if _, err := os.Stat(filepath.Join(sess.dir, fmt.Sprintf("seg_%d.m4s", sess.cursor+1))); err != nil {
			return
		}
		sess.cursor++
	}
}

// ready 文件存在且非空才算就绪：分片有 temp_file 改名保证完整，
// 但 init.mp4 是 ffmpeg 启动即建的 0 字节占位、首个分片完成后才一次性写入
// （实测 hls muxer 行为），只查存在会把空 init 发给播放器导致起播失败。
func ready(fp string) bool {
	st, err := os.Stat(fp)
	return err == nil && st.Size() > 0
}

func (sess *session) waitFile(ctx context.Context, fp string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if ready(fp) {
			return fp, nil
		}
		sess.mu.Lock()
		rerr := sess.runErr
		idle := sess.cmd == nil
		sess.mu.Unlock()
		if rerr != nil {
			return "", rerr
		}
		if idle { // 进程已跑完仍无此文件（如 event 模式列表未含/时长边界）
			if ready(fp) {
				return fp, nil
			}
			return "", driver.ErrNotFound
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("等待分片生成超时")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// tailBuf 保留 stderr 末尾 2KB 供错误诊断。
type tailBuf struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2048 {
		t.buf = t.buf[len(t.buf)-2048:]
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}
