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

// hevcCap 读客户端自报的 HEVC 解码能力：8=Main（8bit）、10=含 Main 10bit、其余=不支持。
//
// 由前端 isTypeSupported 实测后带上来，服务端不按 UA 猜：Safari、装了 HEVC 扩展的
// Edge、有硬解的 Chrome 都能直出，而按 UA 判必然既有漏判也有误判——漏判只是多烧 CPU，
// 误判就是黑屏。缺参数一律按不支持处理，行为与本功能上线前一致。
func hevcCap(c *gin.Context) int {
	switch c.Query("hevc") {
	case "10":
		return 10
	case "8":
		return 8
	}
	return 0
}

// GET /api/video/info?path=&hevc= —— 三档策略（direct/hls/unsupported，见 media.Service.Info）。
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
	OK(c, s.media.Info(ctx, getUser(c), p, fi, hevcCap(c)))
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
	u, cap := getUser(c), hevcCap(c)
	switch {
	case res == "index.m3u8":
		b, err := s.media.Playlist(c.Request.Context(), u, logical, cap)
		if err != nil {
			mediaError(c, err)
			return
		}
		// 播放列表可能在增长（event 模式），禁止缓存；
		// 分片 URI 是相对地址、不带鉴权也不带能力标记，两者一并注回去
		// （Safari 原生 HLS 也能用）——分片必须落到与列表同一个会话上。
		c.Header("Cache-Control", "no-store")
		c.Data(200, "application/vnd.apple.mpegurl", injectQuery(b, c.Query("token"), cap))
	case res == "init.mp4" || segNameRe.MatchString(res):
		fp, err := s.media.Segment(c.Request.Context(), u, logical, res, cap)
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

// injectQuery 给播放列表内的相对 URI（分片行与 EXT-X-MAP）追加 ?token= 与 &hevc=。
func injectQuery(b []byte, tok string, cap int) []byte {
	q := ""
	if tok != "" {
		q = "?token=" + url.QueryEscape(tok)
	}
	if cap > 0 {
		sep := "?"
		if q != "" {
			sep = "&"
		}
		q += sep + "hevc=" + strconv.Itoa(cap)
	}
	if q == "" {
		return b
	}
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

// coverWait 是封面接口愿意当场等多久，coverBuild 是一张封面最多做多久。
//
// 生成过程必须脱离浏览器请求的生死：滚动、切页、懒加载都会取消图片请求，若把请求的
// ctx 传下去，正在抽的那一帧当场被杀、半成品丢弃、什么都没缓存——下次进主页又得
// 从头烧一遍 CPU 和带宽，永远收敛不了。
//
// 但 handler 不能跟着一起等到底。浏览器早就放弃的请求，服务端仍占着一条 TCP，而抽帧
// 闸只有几个名额，其余全在排队——主页一次滚动几百张卡片，连接就是这么攒到几千条
// 把服务压垮的。所以这里只当场等一小会儿：等到了直接回图，等不到就 404，生成照样在
// 后台跑完落盘，下次刷新即命中（前端封面加载失败本就回落占位图标）。
const (
	coverWait  = 8 * time.Second
	coverBuild = 3 * time.Minute
)

// GET /api/thumb/*path?size=
func (s *Server) thumbHandler(c *gin.Context) {
	size, _ := strconv.Atoi(c.DefaultQuery("size", "400"))
	u, logical := getUser(c), c.Param("path")
	// 摘掉取消信号，交给后台跑完。gin 的 Context 在 handler 返回后会被复用，
	// 派进 goroutine 的东西必须在这里就取出来。
	base := context.WithoutCancel(c.Request.Context())

	type cover struct {
		url, file string
		err       error
	}
	done := make(chan cover, 1) // 带缓冲：handler 已经走了也不挡住后台那条
	go func() {
		ctx, cancel := context.WithTimeout(base, coverBuild)
		defer cancel()
		url, file, err := s.thumbs.Get(ctx, u, logical, size)
		done <- cover{url, file, err}
	}()

	wait := time.NewTimer(coverWait)
	defer wait.Stop()
	var r cover
	select {
	case r = <-done:
	case <-wait.C:
		// 还在做：先放浏览器走，别占着连接。这张封面会在后台做完并落盘。
		Fail(c, 404, "封面正在生成")
		return
	}
	url, file, err := r.url, r.file, r.err
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
