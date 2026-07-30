// Package thumb 提供缩略图：云盘驱动自带缩略图下载落盘缓存后本地服务
// （URL 稳定可被浏览器缓存；直链 tempauth 每次都变导致刷新即重载）；
// 本地文件按需生成（图片用 imaging 缩放，视频用 ffmpeg 截帧）并落盘缓存。
package thumb

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
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

// 本地盘封面的宽度档位：卡片/网格一档，hero 大图一档。
const (
	widthCard = 640
	widthHero = 1280
)

// normWidth 把请求宽度归到固定档位。
//
// 本地盘的缓存键含宽度（见 cacheKey），调用方各写一个数字的话，同一张图会按 320、
// 400、480、1200 各生成一份，谁也命中不了谁。后台预载踩的就是这个坑：它按一个自己
// 挑的宽度生成、落盘、报完成，而前端请求的宽度没有一个对得上——预载说做完了，
// 封面却一张也看不见。归档之后前端怎么写都只落在这两个键上。
func normWidth(w int) int {
	if w <= widthCard {
		return widthCard
	}
	return widthHero
}

// dlLimit 是云盘自带缩略图这条路的并发上限（取直链 + 下载图片）。
//
// 全程是网络等待、不吃 CPU，所以单独一把闸，不与 ffmpeg 的总闸混用——混在一起的话
// 一屏封面会排在抽帧和探测后面，首屏得干等，两边还互相饿死。
// 6 条足够跑满家用带宽，也不至于一屏两百张卡片就朝云盘打出两百条 TLS 连接。
const dlLimit = 6

type Service struct {
	fs       *fs.FS
	cacheDir string
	jobs     *util.Gate // 生成并发限制（CPU/ffmpeg），见 conf.MediaJobs
	dl       *util.Gate // 云盘自带缩略图并发限制（网络），见 dlLimit

	mu     sync.Mutex
	flight map[string]chan struct{} // 同 key singleflight

	ffOnce sync.Once
	ffPath string
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
		dl: util.NewGate(dlLimit), flight: map[string]chan struct{}{}}
}

// SetGate 换用外部传入的 ffmpeg 总闸，仅供启动接线（此时尚无并发）。
// thumb 与 media 各建一把闸的话，conf.MediaJobs 写着 N，实际能同时跑的 ffmpeg 是 2N。
func (s *Service) SetGate(g *util.Gate) { s.jobs = g }

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

// once 是 Get/remote 两处共用的 singleflight 前置：同一 key 已有在途
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
	width = normWidth(width)
	drv, rel, err := s.fs.Driver(u, logical)
	if err != nil {
		return "", "", err
	}
	isVideo := model.ExtType(logical) == "video"
	// .ts 等与源码撞扩展名的视频会被 OneDrive 误当文本、生成「把文件字节渲染成文本」的
	// 乱码预览缩略图，必须跳过自带缩略图（本地盘的 .ts 仍能靠下方抽帧出封面，
	// 远端盘的 .ts 则没有封面 —— 远端抽帧兜底已砍）。
	if t, ok := drv.(driver.Thumber); ok && !(isVideo && textPreviewExt(logical)) {
		if url, file := s.remote(ctx, t, rel, logical); url != "" || file != "" {
			return url, file, nil
		}
	}
	// 给不出直链但能交出字节的存储（Telegram 走 MTProto）：落盘后与上面那条同样本地服务。
	if t, ok := drv.(driver.ThumbFetcher); ok && !(isVideo && textPreviewExt(logical)) {
		if file := s.remoteBytes(ctx, t, rel, logical); file != "" {
			return "", file, nil
		}
	}
	// 远端盘视频到此为止：没有自带缩略图就是没有封面。曾经这里用 ffmpeg 走回环 /api/raw
	// 抽帧兜底，实测在 Google Drive 上一小时跑 355 项、产出 0 张——每张都要把视频经服务器
	// 中转拉回来再解一帧，代价与收益完全不成比例，已整条砍掉。本地盘视频走下方本地生成
	// 分支（读绝对路径，几十毫秒，仍然抽帧）。
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
		err = s.genImage(ctx, abs, out, width)
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
	width = normWidth(width)
	drv, rel, err := s.fs.Driver(u, logical)
	if err != nil {
		return CoverNone
	}
	isVideo := model.ExtType(logical) == "video"
	_, isLocal := drv.(driver.LocalPather)

	// ① 驱动自带缩略图：缓存在 TTL 内即完事；过期或没有都得走一趟网络（算活）。
	// 两条取法（URL 下载 / 问驱动要字节）落同一个 |remote 键，这里一并判。
	_, byURL := drv.(driver.Thumber)
	_, byBytes := drv.(driver.ThumbFetcher)
	if (byURL || byBytes) && !(isVideo && textPreviewExt(logical)) {
		if s.freshWithin(cacheKey(logical+"|remote", time.Time{}, 0, 0), remoteTTL) {
			return CoverReady
		}
		return CoverPending
	}
	// ② 远端盘且驱动不给缩略图：做也白做。远端抽帧兜底已砍（见 Get），没有第二条路。
	if !isLocal {
		return CoverNone
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

	// 取直链和下载都要过网络闸。此前只有下载那一步过闸，向云盘要直链的 t.Thumb 一点闸
	// 不过——并发数等于同时到达的 HTTP 请求数，主页一屏卡片就能朝云盘打出几百条连接。
	release, err := s.dl.Acquire(ctx)
	if err != nil {
		return "", ""
	}
	defer release()

	url, err := t.Thumb(ctx, rel)
	if err != nil || url == "" {
		if _, e := os.Stat(out); e == nil {
			return "", out // 过期刷新失败：沿用旧缓存
		}
		return "", ""
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

// remoteBytes 取存储交出的缩略图字节并落盘（driver.ThumbFetcher，如 Telegram）。
// 缓存键、TTL、网络闸与 singleflight 全部与 remote() 共用同一套，只是取法从「下载一个
// URL」换成「问驱动要字节」。返回本地文件路径，空 = 该文件没有缩略图。
// 没有 302 兜底那条路：字节取不到就是真取不到，没有可交给浏览器的地址。
func (s *Service) remoteBytes(ctx context.Context, t driver.ThumbFetcher, rel, logical string) string {
	key := cacheKey(logical+"|remote", time.Time{}, 0, 0)
	out := filepath.Join(s.cacheDir, key+".jpg")
	if st, err := os.Stat(out); err == nil && time.Since(st.ModTime()) < remoteTTL {
		return out
	}

	lead, finish, err := s.once(ctx, key)
	if err != nil {
		return ""
	}
	if !lead {
		if _, e := os.Stat(out); e == nil {
			return out
		}
		return ""
	}
	defer finish()

	release, err := s.dl.Acquire(ctx)
	if err != nil {
		return ""
	}
	defer release()

	b, err := t.ThumbBytes(ctx, rel)
	if err != nil || len(b) == 0 {
		if _, e := os.Stat(out); e == nil {
			return out // 过期刷新失败：沿用旧缓存
		}
		if err != nil && !errors.Is(err, driver.ErrNotFound) {
			log.Printf("[thumb] 取存储缩略图失败 %s: %v", logical, err)
		}
		return ""
	}
	// 先写临时文件再改名：半截文件被当成有效缓存的话，那张破图会一直挂到 TTL 到期
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		log.Printf("[thumb] 写缩略图缓存失败 %s: %v", logical, err)
		return ""
	}
	if err := os.Rename(tmp, out); err != nil {
		os.Remove(tmp)
		log.Printf("[thumb] 缩略图缓存改名失败 %s: %v", logical, err)
		return ""
	}
	return out
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

// dlClient 下载云盘缩略图专用：http.DefaultClient 每 host 只留 2 条空闲连接，
// 一屏封面下来大半是新建连接又立刻丢弃，全堆在 TIME_WAIT 里。
var dlClient = &http.Client{Transport: util.NewHTTPTransport()}

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

// maxDecodePixels 进程内解码的像素上限。imaging 解出来是 NRGBA、4 字节一个像素，
// 16MP 就是 64MB，方向校正还会再复制一份，而生成闸放行 2 个并发——一张全景图或
// 扫描件就能让常驻内存翻几倍。超过上限的交给 ffmpeg：解码在子进程里，用的是
// 1.5 字节一个像素的 YUV，真撑爆了也只死那一个进程。
const maxDecodePixels = 16 << 20 // ≈ 1677 万像素，比 5000 万像素的手机全景小一档

func (s *Service) genImage(ctx context.Context, abs, out string, width int) error {
	if w, h, big := hugeImage(abs); big {
		ff := s.FFmpeg()
		if ff == "" {
			return fmt.Errorf("图片 %d×%d 太大，本机没有 ffmpeg，无法生成缩略图", w, h)
		}
		// 与视频抽帧共用一份实现（本地输入无 http 旗标）。静态图只有一帧，不能定位，
		// 见 media.FrameFirst。
		return media.FrameFirst(ctx, ff, abs, out, width)
	}
	src, err := imaging.Open(abs, imaging.AutoOrientation(true))
	if err != nil {
		return err
	}
	if src.Bounds().Dx() > width {
		src = imaging.Resize(src, width, 0, imaging.Lanczos)
	}
	return saveJPEG(src, out)
}

// hugeImage 只读图片头拿尺寸，判断整张解码会不会超出 maxDecodePixels。
// 读不出头（格式不认识、文件损坏）一律当作不超——让后面的正常解码去报真正的错，
// 这里不替它下结论。
func hugeImage(abs string) (w, h int, big bool) {
	f, err := os.Open(abs)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, int64(cfg.Width)*int64(cfg.Height) > maxDecodePixels
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
