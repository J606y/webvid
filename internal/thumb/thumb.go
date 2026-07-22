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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp" // 注册 webp 解码

	"newlist/internal/driver"
	"newlist/internal/fs"
	"newlist/internal/model"
	"newlist/internal/user"
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
	sem      chan struct{} // 生成并发限制（CPU/ffmpeg）
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
	return &Service{fs: f, cacheDir: dir, sem: make(chan struct{}, 2),
		dlSem: make(chan struct{}, 6), flight: map[string]chan struct{}{}}
}

// FFmpeg 返回探测到的 ffmpeg 路径（可能为空 = 不可用）。
func (s *Service) FFmpeg() string {
	s.ffOnce.Do(func() {
		if p := os.Getenv("NL_FFMPEG"); p != "" {
			s.ffPath = p
			return
		}
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			s.ffPath = p
			return
		}
		// Windows: winget 安装但当前进程 PATH 未刷新的兜底
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			matches, _ := filepath.Glob(filepath.Join(la,
				"Microsoft", "WinGet", "Packages", "Gyan.FFmpeg*", "ffmpeg-*", "bin", "ffmpeg.exe"))
			if len(matches) > 0 {
				s.ffPath = matches[len(matches)-1]
				return
			}
		}
		log.Println("[thumb] 未找到 ffmpeg，视频缩略图不可用（可设 NL_FFMPEG 指定路径）")
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
		if file := s.remoteVideoFrame(ctx, u, logical); file != "" {
			return "", file, nil
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

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return "", "", ctx.Err()
	}

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
func (s *Service) remoteVideoFrame(ctx context.Context, u *user.User, logical string) string {
	if s.videoFrame == nil {
		return ""
	}
	key := cacheKey(logical+"|vframe", time.Time{}, 0, 0)
	out := filepath.Join(s.cacheDir, key+".jpg")
	if st, err := os.Stat(out); err == nil && time.Since(st.ModTime()) < remoteTTL {
		return out
	}

	// singleflight：同 key 只抽一次
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

	// 抽帧走 ffmpeg（CPU）+ 网络拉流，占 sem 生成闸
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return ""
	}
	if err := s.videoFrame(ctx, u, logical, out, vframeWidth); err != nil {
		log.Printf("[thumb] 远端视频抽帧失败 %s: %v", logical, err)
		if _, e := os.Stat(out); e == nil {
			return out // 刷新失败沿用旧缓存
		}
		return ""
	}
	return out
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
	tmp := out + ".tmp.jpg"
	defer os.Remove(tmp)
	try := func(ss string) error {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, ff,
			"-hide_banner", "-loglevel", "error",
			"-ss", ss, "-i", abs,
			"-frames:v", "1",
			"-vf", fmt.Sprintf("scale=%d:-2", width),
			"-q:v", "5", "-y", tmp)
		if err := cmd.Run(); err != nil {
			return err
		}
		if st, err := os.Stat(tmp); err != nil || st.Size() == 0 {
			return fmt.Errorf("ffmpeg 未产出帧")
		}
		return nil
	}
	if err := try("3"); err != nil {
		if err := try("0"); err != nil {
			return err
		}
	}
	return os.Rename(tmp, out)
}
