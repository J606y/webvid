package server

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"newlist/internal/fs"
	"newlist/internal/media"
)

// GET /api/video/info?path= —— 三档策略（direct/hls/unsupported，见 media.Service.Info）。
// 后台预载已探测的视频经 media_info 持久缓存秒回，未探测的现场 ffprobe 并回写缓存。
func (s *Server) videoInfo(c *gin.Context) {
	p, err := fs.NormPath(c.Query("path"))
	if err != nil {
		fsError(c, err)
		return
	}
	fi, err := s.fs.Get(c.Request.Context(), getUser(c), p)
	if err != nil {
		fsError(c, err)
		return
	}
	if fi.IsDir {
		Fail(c, 400, "该路径不是文件")
		return
	}
	// 同 thumbHandler：探测一旦开跑就让它跑完并入库。用户等不及切走了，这次探测
	// 也不该白费——否则下次点开同一个视频还得从头探一遍。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 90*time.Second)
	defer cancel()
	OK(c, s.media.Info(ctx, getUser(c), p, fi))
}

var segNameRe = regexp.MustCompile(`^seg_\d+\.m4s$`)

// GET /api/video/hls/*path —— *path = <逻辑路径>/<资源名>，
// 资源名 ∈ index.m3u8 | init.mp4 | seg_N.m4s。
// index.m3u8 创建/复用转码会话；分片等待生成后以文件形式返回。
func (s *Server) videoHLS(c *gin.Context) {
	raw := c.Param("path")
	i := strings.LastIndex(raw, "/")
	if i < 0 {
		Fail(c, 400, "路径非法")
		return
	}
	res := raw[i+1:]
	logical, err := fs.NormPath(raw[:i])
	if err != nil {
		fsError(c, err)
		return
	}
	u := getUser(c)
	switch {
	case res == "index.m3u8":
		b, err := s.media.Playlist(c.Request.Context(), u, logical)
		if err != nil {
			mediaError(c, err)
			return
		}
		// 播放列表可能在增长（event 模式），禁止缓存；
		// 分片 URI 是相对地址不带鉴权，把 token 注入回去（Safari 原生 HLS 也能用）
		c.Header("Cache-Control", "no-store")
		c.Data(200, "application/vnd.apple.mpegurl", injectToken(b, c.Query("token")))
	case res == "init.mp4" || segNameRe.MatchString(res):
		fp, err := s.media.Segment(c.Request.Context(), u, logical, res)
		if err != nil {
			mediaError(c, err)
			return
		}
		ct := "video/mp4"
		if strings.HasSuffix(res, ".m4s") {
			ct = "video/iso.segment"
		}
		c.Header("Content-Type", ct)
		c.Header("Cache-Control", "private, max-age=3600")
		c.File(fp)
	default:
		Fail(c, 404, "资源不存在")
	}
}

// injectToken 给播放列表内的相对 URI（分片行与 EXT-X-MAP）追加 ?token=。
func injectToken(b []byte, tok string) []byte {
	if tok == "" {
		return b
	}
	q := "?token=" + url.QueryEscape(tok)
	lines := strings.Split(string(b), "\n")
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		switch {
		case t == "" || (strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "#EXT-X-MAP:")):
			// 注释/空行原样
		case strings.HasPrefix(t, "#EXT-X-MAP:"):
			lines[i] = strings.Replace(ln, `init.mp4"`, `init.mp4`+q+`"`, 1)
		default:
			lines[i] = t + q
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// mediaError 转码层错误 → HTTP：ffmpeg 缺失 501，其余复用 fs 映射。
func mediaError(c *gin.Context, err error) {
	if errors.Is(err, media.ErrNoFFmpeg) {
		Fail(c, 501, err.Error())
		return
	}
	fsError(c, err)
}

// GET /api/thumb/*path?size=
func (s *Server) thumbHandler(c *gin.Context) {
	size, _ := strconv.Atoi(c.DefaultQuery("size", "400"))
	// 生成过程脱离浏览器请求的生死：滚动、切页、懒加载都会取消图片请求，若把请求的
	// ctx 传下去，正在抽的那一帧当场被杀、半成品丢弃、什么都没缓存——下次进主页
	// 又得从头烧一遍 CPU 和带宽，永远收敛不了。摘掉取消信号后这张封面一定做完并落盘。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Minute)
	defer cancel()
	url, file, err := s.thumbs.Get(ctx, getUser(c), c.Param("path"), size)
	if err != nil {
		// 缩略图不可用一律 404（前端回落占位图标），不暴露细节。抽帧失败、云盘报错
		// 也走这里：一张封面出不来不该让前端收到 500，真正的原因写在服务端日志与
		// 后台预载卡片上（见 preload 的失败计数）。
		Fail(c, 404, "无缩略图")
		return
	}
	if url != "" {
		// 兜底 302（下载失败才走到）：短缓存，别让浏览器每次刷新都重取
		c.Header("Cache-Control", "private, max-age=600")
		c.Redirect(302, url)
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.File(file)
}
