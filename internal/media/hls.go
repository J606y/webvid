package media

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

	mu       sync.Mutex
	sessions map[string]*session
	probes   map[string]Decision
}

func New(f *fs.FS, dataDir, baseURL string, secret []byte, db *sql.DB) *Service {
	root := filepath.Join(dataDir, "transcode")
	os.RemoveAll(root) // 会话均为进程内状态，启动清理上次残留
	os.MkdirAll(root, 0o755)
	s := &Service{fs: f, root: root, baseURL: baseURL, secret: secret,
		internalToken: auth.RandomPassword(32), db: db,
		sessions: map[string]*session{}, probes: map[string]Decision{}}
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

// FrameJPEG 抽取视频一帧写入 out（缩放到宽 width 的 JPEG），供缩略图兜底：
// 云盘视频驱动无自带缩略图时（如 OneDrive 对 mkv/ts 等不生成预览），thumb 经此
// 走本地回环 /api/raw 抽帧（-ss 输入端 seek，只 Range 拉取起始附近，不整片下载）。
func (s *Service) FrameJPEG(ctx context.Context, u *user.User, logical, out string, width int) error {
	ff, _ := s.tools()
	if ff == "" {
		return ErrNoFFmpeg
	}
	in, err := s.input(u, logical)
	if err != nil {
		return err
	}
	tmp := out + ".vf.tmp.jpg"
	defer os.Remove(tmp)
	try := func(ss string) error {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		a := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-ss", ss}
		a = append(a, httpInputArgs(in, s.internalToken)...)
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
	if err := try("3"); err != nil {
		if err := try("0"); err != nil { // 短视频/seek 失败回退取首帧
			return err
		}
	}
	return os.Rename(tmp, out)
}

// Decide 探测文件并给出播放决策（结果按 路径+size+mtime 内存缓存）。
func (s *Service) Decide(ctx context.Context, u *user.User, logical string, fi model.FileInfo) (Decision, error) {
	_, ffprobe := s.tools()
	if ffprobe == "" {
		return Decision{}, ErrNoFFmpeg
	}
	key := logical + "|" + strconv.FormatInt(fi.Size, 10) + "|" + strconv.FormatInt(fi.Modified.UnixNano(), 10)
	s.mu.Lock()
	if d, ok := s.probes[key]; ok {
		s.mu.Unlock()
		return d, nil
	}
	s.mu.Unlock()

	// 持久层（预载已探测 / 上次会话回写）：size+modified 未变即复用，免云盘现场探测。
	if d, ok := s.loadInfo(logical, fi); ok {
		s.cacheMem(key, d)
		return d, nil
	}

	input, err := s.input(u, logical)
	if err != nil {
		return Decision{}, err
	}
	po, err := runProbe(ctx, ffprobe, input, s.internalToken)
	if err != nil {
		return Decision{}, err
	}
	d := decide(po)
	s.cacheMem(key, d)
	s.saveInfo(logical, fi, d)
	return d, nil
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
		d                        Decision
		vc, ac, aa, hv, ha, size int64
	)
	mod := modKey(fi.Modified)
	err := s.db.QueryRow(
		`SELECT size, video_copy, audio_copy, audio_aac, has_video, has_audio, duration FROM media_info
		 WHERE path=? AND modified=?`, logical, mod).
		Scan(&size, &vc, &ac, &aa, &hv, &ha, &d.Duration)
	if err != nil || size != fi.Size {
		return Decision{}, false
	}
	d.VideoCopy, d.AudioCopy, d.AudioAAC, d.HasVideo, d.HasAudio = vc == 1, ac == 1, aa == 1, hv == 1, ha == 1
	return d, true
}

// saveInfo 把探测决策写入 media_info（覆盖旧记录）。失败仅记日志，不影响播放。
func (s *Service) saveInfo(logical string, fi model.FileInfo, d Decision) {
	if s.db == nil {
		return
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO media_info
		 (path,size,modified,video_copy,audio_copy,audio_aac,has_video,has_audio,duration,probed_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		logical, fi.Size, modKey(fi.Modified),
		util.BoolInt(d.VideoCopy), util.BoolInt(d.AudioCopy), util.BoolInt(d.AudioAAC), util.BoolInt(d.HasVideo), util.BoolInt(d.HasAudio),
		d.Duration, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		log.Printf("[media] media_info 写入失败 %s: %v", logical, err)
	}
}

// modKey 把修改时间归一为 RFC3339（秒级，与 index 写入 files.modified 同格式）。
func modKey(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Playlist 返回 index.m3u8 内容（不存在则创建会话并启动 ffmpeg）。
func (s *Service) Playlist(ctx context.Context, u *user.User, logical string) ([]byte, error) {
	sess, err := s.ensure(ctx, u, logical)
	if err != nil {
		return nil, err
	}
	return sess.playlist(ctx)
}

// Segment 等待并返回分片文件的本地路径（name ∈ init.mp4 | seg_N.m4s）。
func (s *Service) Segment(ctx context.Context, u *user.User, logical, name string) (string, error) {
	sess, err := s.ensure(ctx, u, logical)
	if err != nil {
		return "", err
	}
	return sess.segment(ctx, name)
}

// ensure 取现有会话或新建（Stat+probe 不持全局锁；同 key 并发创建以先入表者为准）。
func (s *Service) ensure(ctx context.Context, u *user.User, logical string) (*session, error) {
	ffmpeg, _ := s.tools()
	if ffmpeg == "" {
		return nil, ErrNoFFmpeg
	}
	s.mu.Lock()
	if sess := s.sessions[logical]; sess != nil {
		s.mu.Unlock()
		sess.touch()
		return sess, nil
	}
	s.mu.Unlock()

	drv, rel, err := s.fs.Driver(u, logical) // 含视野/存在性校验（不发网络）
	if err != nil {
		return nil, err
	}
	fi, err := drv.Stat(ctx, rel)
	if err != nil {
		return nil, err
	}
	if fi.IsDir {
		return nil, driver.ErrNotFound
	}
	dec, err := s.Decide(ctx, u, logical, fi)
	if err != nil {
		return nil, err
	}
	input, err := s.input(u, logical)
	if err != nil {
		return nil, err
	}

	h := sha1.Sum([]byte(logical))
	id := hex.EncodeToString(h[:])[:16]
	sess := &session{
		svc:   s,
		key:   logical,
		dir:   filepath.Join(s.root, id),
		input: input,
		dec:   dec,
		vod:   dec.HasVideo && !dec.VideoCopy && dec.Duration > 0,
		nSegs: int(math.Ceil(math.Max(dec.Duration, segLen) / segLen)),
	}

	s.mu.Lock()
	if exist := s.sessions[logical]; exist != nil {
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
		log.Printf("[media] 会话数达上限，驱逐最久未用 %s", oldestKey)
		go oldest.destroy()
	}
	s.sessions[logical] = sess
	s.mu.Unlock()

	if err := os.MkdirAll(sess.dir, 0o755); err != nil {
		s.mu.Lock()
		delete(s.sessions, logical)
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
		if s.sessions[logical] == sess {
			delete(s.sessions, logical)
		}
		s.mu.Unlock()
		go sess.destroy()
		return nil, err
	}
	return sess, nil
}

// janitor 定期回收空闲超时的会话。
func (s *Service) janitor() {
	for range time.Tick(time.Minute) {
		s.sweep()
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
			log.Printf("[media] 回收空闲转码会话 %s", k)
			go sess.destroy()
		}
	}
}

// Close 关停服务：终止所有在跑的 ffmpeg 并清理会话目录。由 main 在 HTTP 优雅关闭后调用，
// 避免转码进程变孤儿继续跑完、临时目录残留。
func (s *Service) Close() {
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
			sess.runErr = fmt.Errorf("ffmpeg 退出: %v（%s）", err, scrubToken(sess.stderr.String()))
			log.Printf("[media] %s %v", sess.key, sess.runErr)
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
		} else {
			a = append(a, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
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
		return fp, nil
	}
	if !sess.vod {
		// event 模式列表里出现的分片必已落盘（temp_file），走到这里是竞态边界，短等
		return sess.waitFile(ctx, fp, 30*time.Second)
	}

	// vod 模式：进度窗口内等待，窗口外（含回拖到已驱逐区/进程已停）-ss 重启
	sess.mu.Lock()
	sess.advanceCursorLocked()
	inWindow := sess.cmd != nil && n >= sess.runFrom && n <= sess.cursor+waitAhead
	if !inWindow {
		sess.killLocked()
		sess.startLocked(n)
	}
	rerr := sess.runErr
	sess.mu.Unlock()
	if rerr != nil {
		return "", rerr
	}
	return sess.waitFile(ctx, fp, 90*time.Second)
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
