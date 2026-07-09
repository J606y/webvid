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

type Service struct {
	fs       *fs.FS
	cacheDir string
	sem      chan struct{} // 生成并发限制（CPU/ffmpeg）
	dlSem    chan struct{} // 远端缩略图下载并发限制（网络）

	mu     sync.Mutex
	flight map[string]chan struct{} // 同 key singleflight

	ffOnce sync.Once
	ffPath string
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

// Get 返回 (302 重定向 URL, 本地缓存文件路径, error)，二者最多一个非空。
func (s *Service) Get(ctx context.Context, u *user.User, logical string, width int) (string, string, error) {
	if width <= 0 || width > 1600 {
		width = 400
	}
	drv, rel, err := s.fs.Driver(u, logical)
	if err != nil {
		return "", "", err
	}
	if t, ok := drv.(driver.Thumber); ok {
		if url, file := s.remote(ctx, t, rel, logical); url != "" || file != "" {
			return url, file, nil
		}
		// 驱动无该文件缩略图 → 继续走本地生成分支（远端盘不满足 LocalPather → 404）
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
	s.mu.Lock()
	if ch, busy := s.flight[key]; busy {
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
		if _, err := os.Stat(out); err == nil {
			return "", out, nil
		}
		return "", "", driver.ErrNotSupported
	}
	ch := make(chan struct{})
	s.flight[key] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.flight, key)
		s.mu.Unlock()
		close(ch)
	}()

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
	s.mu.Lock()
	if ch, busy := s.flight[key]; busy {
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return "", ""
		}
		if _, err := os.Stat(out); err == nil {
			return "", out
		}
		return "", ""
	}
	ch := make(chan struct{})
	s.flight[key] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.flight, key)
		s.mu.Unlock()
		close(ch)
	}()

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
