// Package thumb 提供缩略图：云盘驱动自带缩略图下载落盘缓存后本地服务
// （URL 稳定可被浏览器缓存；直链 tempauth 每次都变导致刷新即重载）；
// 本地文件按需生成（图片用 imaging 缩放，视频用 ffmpeg 截帧）并落盘缓存。
package thumb

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp" // 注册 webp 解码

	"newlist/internal/driver"
	"newlist/internal/fs"
	"newlist/internal/media"
	"newlist/internal/model"
	"newlist/internal/user"
	"newlist/internal/util"
)

// remoteTTL 远端缩略图缓存有效期：键只含逻辑路径（远端 Stat 是网络调用，
// 不能拿 mtime 进键），同路径换内容只能靠过期刷新兜底；刷新失败沿用旧文件。
const remoteTTL = 30 * 24 * time.Hour

// vframeWidth 远端视频兜底封面的生成宽度：一次生成、各请求尺寸共用（免每尺寸都网络抽帧）；
// 640 足够卡片/网格显示，hero 大图轻微放大也可接受。
const vframeWidth = 640

type Service struct {
	fs       *fs.FS
	cacheDir string
	jobs     *util.Gate    // 生成并发限制（CPU/ffmpeg），后台可调，见 conf.MediaJobs
	dlSem    chan struct{} // 远端缩略图下载并发限制（网络）

	mu     sync.Mutex
	flight map[string]chan struct{} // 同 key singleflight

	ffOnce sync.Once
	ffPath string

	// videoFrame：远端视频抽帧兜底（由 media.Service 提供，见 SetVideoFramer）。
	// 云盘视频驱动无自带缩略图时，经此走回环 /api/raw 用 ffmpeg 抽一帧。nil = 不兜底。
	videoFrame func(ctx context.Context, u *user.User, logical, out string, width int) error
}

// SetVideoFramer 注入远端视频抽帧函数（main 在 media 建好后接线，解耦免包依赖）。
func (s *Service) SetVideoFramer(fn func(ctx context.Context, u *user.User, logical, out string, width int) error) {
	s.videoFrame = fn
}

// textPreviewExts 是会被云盘（OneDrive）误当源码/文本、把文件字节渲染成文本生成乱码
// 预览缩略图的视频扩展名。目前仅 .ts（与 TypeScript 撞扩展名）——这类跳过自带缩略图。
var textPreviewExts = map[string]bool{"ts": true}

func textPreviewExt(logical string) bool {
	return textPreviewExts[strings.ToLower(strings.TrimPrefix(filepath.Ext(logical), "."))]
}

func New(f *fs.FS, dataDir string) *Service {
	dir := filepath.Join(dataDir, "thumbs")
	os.MkdirAll(dir, 0o755)
	return &Service{fs: f, cacheDir: dir, jobs: util.NewGate(2),
		dlSem: make(chan struct{}, 6), flight: map[string]chan struct{}{}}
}

// SetJobs 热调封面生成（ffmpeg 抽帧 / 图片缩放）的并发上限，与 media 的探测闸同一个设置项。
func (s *Service) SetJobs(n int) { s.jobs.SetLimit(n) }

// FFmpeg 返回探测到的 ffmpeg 路径（可能为空 = 不可用）。
// 探测逻辑（NL_FFMPEG / PATH / winget 兜底）与 media 共用一份，见 media.LookTool。
func (s *Service) FFmpeg() string {
	s.ffOnce.Do(func() {
		s.ffPath = media.LookTool("ffmpeg")
		if s.ffPath == "" {
			log.Println("[thumb] 未找到 ffmpeg，视频缩略图不可用（可设 NL_FFMPEG 指定路径）")
		}
	})
	return s.ffPath
}

// once 是 Get/remote/remoteVideoFrame 三处共用的 singleflight 前置：同一 key 已有在途
// 计算时，本调用挂起等待其完成（ctx 取消则 err 非空，调用方应立即返回）；
// 抢到 leader 身份（lead=true）的调用方负责实际计算，算完必须 defer finish() 唤醒等待者
// 并从 flight 表摘除 key——无论成功与否，「是否有旧缓存可沿用」由各自读盘判断，
// finish 本身不携带结果。lead=false 时 finish 为 nil，调用方转去检查磁盘产物是否已就绪。
func (s *Service) once(ctx context.Context, key string) (lead bool, finish func(), err error) {
	s.mu.Lock()
	if ch, busy := s.flight[key]; busy {
		s.mu.Unlock()
		select {
		case <-ch:
			return false, nil, nil
		case <-ctx.Done():
			return false, nil, ctx.Err()
		}
	}
	ch := make(chan struct{})
	s.flight[key] = ch
	s.mu.Unlock()
	return true, func() {
		s.mu.Lock()
		delete(s.flight, key)
		s.mu.Unlock()
		close(ch)
	}, nil
}

// Get 返回 (302 重定向 URL, 本地缓存文件路径, error)，二者最多一个非空。
func (s *Service) Get(ctx context.Context, u *user.User, logical string, width int) (string, string, error) {
	if width <= 0 || width > 1600 {
		width = 400
	}
	drv, rel, err := s.fs.Driver(u, logical)
	if err != nil {
		return "", "", err
	}
	isVideo := model.ExtType(logical) == "video"
	_, isLocal := drv.(driver.LocalPather)
	// .ts 等与源码撞扩展名的视频会被 OneDrive 误当文本、生成「把文件字节渲染成文本」的
	// 乱码预览缩略图，必须跳过自带缩略图改用 ffmpeg 抽帧（见 textPreviewExt）。
	if t, ok := drv.(driver.Thumber); ok && !(isVideo && textPreviewExt(logical)) {
		if url, file := s.remote(ctx, t, rel, logical); url != "" || file != "" {
			return url, file, nil
		}
	}
	// 远端盘视频无可信自带缩略图（OneDrive 对 flv/部分 mp4/ts 不生成或生成乱码）：
	// 用 ffmpeg 走回环 /api/raw 抽帧兜底。本地盘视频走下方本地生成分支（能读绝对路径）。
	if isVideo && !isLocal {
		file, err := s.remoteVideoFrame(ctx, u, logical)
		if file != "" {
			return "", file, nil
		}
		// 抽帧真的失败了就照实说：再往下走只会撞上「该存储不支持此操作」，
		// 把一次可修的故障（缺 ffmpeg、读不到文件）谎报成「这盘本来就没封面」。
		if err != nil {
			return "", "", err
		}
	}
	lp, ok := drv.(driver.LocalPather)
	if !ok {
		return "", "", driver.ErrNotSupported
	}
	abs, err := lp.AbsPath(rel)
	if err != nil {
		return "", "", err
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		return "", "", driver.ErrNotFound
	}

	key := cacheKey(logical, st.ModTime(), st.Size(), width)
	out := filepath.Join(s.cacheDir, key+".jpg")
	if _, err := os.Stat(out); err == nil {
		return "", out, nil
	}

	// singleflight：同 key 只生成一次
	lead, finish, err := s.once(ctx, key)
	if err != nil {
		return "", "", err
	}
	if !lead {
		if _, err := os.Stat(out); err == nil {
			return "", out, nil
		}
		return "", "", driver.ErrNotSupported
	}
	defer finish()

	release, err := s.jobs.Acquire(ctx)
	if err != nil {
		return "", "", err
	}
	defer release()

	switch model.ExtType(logical) {
	case "image":
		err = s.genImage(abs, out, width)
	case "video":
		err = s.genVideo(ctx, abs, out, width)
	default:
		return "", "", driver.ErrNotSupported
	}
	if err != nil {
		return "", "", err
	}
	return "", out, nil
}

// CoverState 是某条路径的封面此刻在缓存里的处境。
type CoverState int

const (
	CoverPending CoverState = iota // 缓存里没有或已过期，要真去下载/生成
	CoverReady                     // 已缓存且未过期，无需再做
	CoverNone                      // 这条路径出不了封面（驱动不支持、缺 ffmpeg 等），做也白做
)

// Cover 只读磁盘判断封面处境，绝不发网络请求、不生成任何文件。供后台预载在派活前
// 把「没活可干」的项排除在进度总数之外——它们会被瞬间跳过，算进总数只会让进度条
// 一开局就停在已缓存占比上，剩下的真活全挤在最后一小截里。
//
// 分支顺序与缓存键取法必须与 Get 一致（Get 改了这里要同步改），否则判断会与实际
// 走的路径对不上：判成 Ready 却其实要下载 → 进度条少算活；反之则多算。
func (s *Service) Cover(u *user.User, logical string, width int) CoverState {
	if width <= 0 || width > 1600 {
		width = 400
	}
	drv, rel, err := s.fs.Driver(u, logical)
	if err != nil {
		return CoverNone
	}
	isVideo := model.ExtType(logical) == "video"
	_, isLocal := drv.(driver.LocalPather)

	// ① 驱动自带缩略图：缓存在 TTL 内即完事；过期或没有都得走一趟网络（算活）
	if _, ok := drv.(driver.Thumber); ok && !(isVideo && textPreviewExt(logical)) {
		if s.freshWithin(cacheKey(logical+"|remote", time.Time{}, 0, 0), remoteTTL) {
			return CoverReady
		}
		return CoverPending
	}
	// ② 远端盘视频：ffmpeg 经回环抽帧兜底；抽帧能力缺席就是做也白做
	if isVideo && !isLocal {
		if s.freshWithin(cacheKey(logical+"|vframe", time.Time{}, 0, 0), remoteTTL) {
			return CoverReady
		}
		if s.videoFrame == nil || s.FFmpeg() == "" {
			return CoverNone
		}
		return CoverPending
	}
	// ③ 本地盘：键含 mtime/size/宽度，源文件变了旧缓存自然不命中
	lp, ok := drv.(driver.LocalPather)
	if !ok {
		return CoverNone
	}
	abs, err := lp.AbsPath(rel)
	if err != nil {
		return CoverNone
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		return CoverNone
	}
	if s.freshWithin(cacheKey(logical, st.ModTime(), st.Size(), width), 0) {
		return CoverReady
	}
	switch model.ExtType(logical) {
	case "image":
		return CoverPending
	case "video":
		if s.FFmpeg() == "" {
			return CoverNone // 截不了帧
		}
		return CoverPending
	}
	return CoverNone
}

// freshWithin 报告该 key 的缓存文件是否存在；ttl>0 时还要求未超龄（远端缩略图用）。
func (s *Service) freshWithin(key string, ttl time.Duration) bool {
	st, err := os.Stat(filepath.Join(s.cacheDir, key+".jpg"))
	if err != nil {
		return false
	}
	return ttl <= 0 || time.Since(st.ModTime()) < ttl
}

// remote 取云盘缩略图：磁盘缓存 TTL 内直接用；未缓存/过期则取直链下载落盘，
// 刷新失败沿用旧文件，首次下载失败回退直链 302（至少能显示一次）。
// 返回 (302 URL, 本地文件)，全空 = 该文件无缩略图。
func (s *Service) remote(ctx context.Context, t driver.Thumber, rel, logical string) (string, string) {
	key := cacheKey(logical+"|remote", time.Time{}, 0, 0)
	out := filepath.Join(s.cacheDir, key+".jpg")
	if st, err := os.Stat(out); err == nil && time.Since(st.ModTime()) < remoteTTL {
		return "", out
	}

	// singleflight：同 key 只取直链+下载一次
	lead, finish, err := s.once(ctx, key)
	if err != nil {
		return "", ""
	}
	if !lead {
		if _, e := os.Stat(out); e == nil {
			return "", out
		}
		return "", ""
	}
	defer finish()

	url, err := t.Thumb(ctx, rel)
	if err != nil || url == "" {
		if _, e := os.Stat(out); e == nil {
			return "", out // 过期刷新失败：沿用旧缓存
		}
		return "", ""
	}
	select {
	case s.dlSem <- struct{}{}:
		defer func() { <-s.dlSem }()
	case <-ctx.Done():
		return url, ""
	}
	if err := download(ctx, url, out); err != nil {
		log.Printf("[thumb] 远端缩略图下载失败 %s: %v", logical, err)
		if _, e := os.Stat(out); e == nil {
			return "", out
		}
		return url, ""
	}
	return "", out
}

// remoteVideoFrame 远端视频兜底封面：驱动无自带缩略图时，用 videoFrame（media 抽帧，
// 经回环 /api/raw）生成一帧落盘。缓存/TTL/并发与 remote() 同构：键只含逻辑路径，
// 靠 remoteTTL 过期兜底刷新；生成失败沿用旧缓存（若有）。返回本地文件路径或 ""。
// 返回 (本地文件, error)：文件非空即成功；两者皆空 = 本就没有抽帧能力，交由调用方继续兜底。
func (s *Service) remoteVideoFrame(ctx context.Context, u *user.User, logical string) (string, error) {
	if s.videoFrame == nil {
		return "", nil
	}
	key := cacheKey(logical+"|vframe", time.Time{}, 0, 0)
	out := filepath.Join(s.cacheDir, key+".jpg")
	if st, err := os.Stat(out); err == nil && time.Since(st.ModTime()) < remoteTTL {
		return out, nil
	}

	// singleflight：同 key 只抽一次
	lead, finish, err := s.once(ctx, key)
	if err != nil {
		return "", err
	}
	if !lead {
		if _, e := os.Stat(out); e == nil {
			return out, nil
		}
		return "", nil
	}
	defer finish()

	// 抽帧走 ffmpeg（CPU）+ 网络拉流，占生成闸
	release, err := s.jobs.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	if err := s.videoFrame(ctx, u, logical, out, vframeWidth); err != nil {
		log.Printf("[thumb] 远端视频抽帧失败 %s: %v", logical, err)
		if _, e := os.Stat(out); e == nil {
			return out, nil // 刷新失败沿用旧缓存
		}
		return "", util.Messagef(err, "无法为云盘视频生成封面。请确认服务器装了 ffmpeg 且能读取该文件。")
	}
	return out, nil
}

// Purge 删除全部封面缓存文件，返回删除的文件数与释放的字节数。
// 逐个删而不是整目录 RemoveAll：Windows 上正被写入的文件（在途下载、ffmpeg 抽帧）
// 删不掉，整目录删会中途失败留下半清状态，重建的目录还会让在途写入落进孤儿目录。
// 逐个删则跳过这类文件（记日志），其余照清；漏下的几个会被后续覆盖或过期刷新。
func (s *Service) Purge() (files int64, bytes int64, err error) {
	ents, err := os.ReadDir(s.cacheDir)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		size := int64(0)
		if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		if err := os.Remove(filepath.Join(s.cacheDir, e.Name())); err != nil {
			log.Printf("[thumb] 删除缓存 %s 失败: %v", e.Name(), err)
			continue
		}
		files++
		bytes += size
	}
	log.Printf("[thumb] 封面缓存已删除：%d 个文件", files)
	return files, bytes, nil
}

func download(ctx context.Context, url, out string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", url, nil)
	if err != nil {
		return err
	}
	// 部分网盘的媒体直链校验 UA，用浏览器 UA 兜底；并补齐 Accept 等头——
	// 只带浏览器 UA 却缺 Accept 头会被 CDN/WAF 判为爬虫返回 406 Not Acceptable。
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("状态码 %d", resp.StatusCode)
	}
	tmp := out + ".dl.tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 20<<20)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, out)
}

func cacheKey(p string, mod time.Time, size int64, w int) string {
	h := sha1.Sum([]byte(p + "|" + strconv.FormatInt(mod.UnixNano(), 10) + "|" +
		strconv.FormatInt(size, 10) + "|" + strconv.Itoa(w)))
	return hex.EncodeToString(h[:])
}

func (s *Service) genImage(abs, out string, width int) error {
	src, err := imaging.Open(abs, imaging.AutoOrientation(true))
	if err != nil {
		return err
	}
	if src.Bounds().Dx() > width {
		src = imaging.Resize(src, width, 0, imaging.Lanczos)
	}
	return saveJPEG(src, out)
}

func saveJPEG(img image.Image, out string) error {
	tmp := out + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 80}); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, out)
}

func (s *Service) genVideo(ctx context.Context, abs, out string, width int) error {
	ff := s.FFmpeg()
	if ff == "" {
		return driver.ErrNotSupported
	}
	// 本地绝对路径抽帧：与 media 的云盘回环抽帧共用一份实现（本地输入无 http 旗标，internalToken 传空）。
	return media.FrameAt(ctx, ff, abs, out, width, "", "3", "0")
}
