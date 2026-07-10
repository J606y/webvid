package server

import (
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"newlist/internal/fs"
	"newlist/internal/model"
	"newlist/internal/stream"
)

// 常见类型的显式映射：Windows 上 mime.TypeByExtension 读注册表，结果不可靠。
var ctypeOverride = map[string]string{
	"mkv":  "video/x-matroska",
	"mp4":  "video/mp4",
	"webm": "video/webm",
	"mov":  "video/quicktime",
	"m4v":  "video/mp4",
	"ts":   "video/mp2t",
	"m2ts": "video/mp2t",
	"flac": "audio/flac",
	"mp3":  "audio/mpeg",
	"m4a":  "audio/mp4",
	"ogg":  "audio/ogg",
	"wav":  "audio/wav",
	"md":   "text/plain; charset=utf-8",
	"txt":  "text/plain; charset=utf-8",
	"log":  "text/plain; charset=utf-8",
	"json": "application/json; charset=utf-8",
	"pdf":  "application/pdf",
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"gif":  "image/gif",
	"webp": "image/webp",
	"svg":  "image/svg+xml",
}

func contentTypeFor(name string) string {
	ext := model.Ext(name)
	if ct, ok := ctypeOverride[ext]; ok {
		return ct
	}
	if ct := mime.TypeByExtension("." + ext); ct != "" {
		if strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "charset") {
			ct += "; charset=utf-8"
		}
		return ct
	}
	return "application/octet-stream"
}

// GET|HEAD /api/raw/*path?dl=1
// 本地文件走 http.ServeContent（自动处理 Range/206/If-Modified-Since/HEAD）；
// 云盘直链：默认 302，挂载开了代理模式则服务器中转（多线程 Range 加速，见 internal/stream）。
func (s *Server) rawHandler(c *gin.Context) {
	res, err := s.fs.LinkEx(c.Request.Context(), getUser(c), c.Param("path"))
	if err != nil {
		fsError(c, err)
		return
	}
	lk, fi := res.Link, res.Info
	if lk.URL != "" {
		if !res.Accel.Proxy {
			// 云盘直链每次都变，灯箱前后翻页会反复请求同一图；短缓存 302 本身
			if isImage(fi.Name) {
				c.Header("Cache-Control", "private, max-age=600")
			}
			c.Redirect(http.StatusFound, lk.URL)
			return
		}
		s.rawProxy(c, res)
		return
	}
	if lk.Local == nil {
		Fail(c, 500, "驱动未返回文件内容")
		return
	}
	defer lk.Local.Close()
	c.Header("Content-Type", contentTypeFor(fi.Name))
	setRawSecurity(c, fi.Name)
	if isImage(fi.Name) {
		c.Header("Cache-Control", "private, max-age=86400")
	}
	if c.Query("dl") == "1" {
		c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(fi.Name))
	}
	http.ServeContent(s.downloadWriter(c), c.Request, fi.Name, fi.Modified, lk.Local)
}

func isImage(name string) bool {
	return strings.HasPrefix(contentTypeFor(name), "image/")
}

// dangerousInline 这些类型内联渲染会执行脚本（SVG/HTML/XML），/raw 一律强制下载。
var dangerousInline = map[string]bool{
	"html": true, "htm": true, "xhtml": true, "xht": true, "svg": true, "xml": true,
}

// setRawSecurity 给 /raw 响应加安全头：CSP sandbox 中和任何内联脚本执行；
// 对 html/svg/xml 等危险类型即使未显式 ?dl=1 也强制 attachment，杜绝存储型 XSS。
func setRawSecurity(c *gin.Context, name string) {
	c.Header("Content-Security-Policy", "sandbox")
	if dangerousInline[model.Ext(name)] {
		c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	}
}

// rawProxy 代理模式：客户端 Range 透传、服务器↔云盘侧并发分块拉取。
// 内部读取方（ffmpeg/ffprobe）改走单连接透传：顺序整读大文件时分块模式的
// "每块一请求"会触发云盘请求频率限流，断流即播放器"一直重连"（详见 stream.ServeSingle）。
func (s *Server) rawProxy(c *gin.Context, res *fs.LinkResult) {
	setRawSecurity(c, res.Info.Name)
	if c.Query("dl") == "1" {
		c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(res.Info.Name))
	}
	if isImage(res.Info.Name) {
		c.Header("Cache-Control", "private, max-age=86400")
	}
	if s.isInternal(c) {
		stream.ServeSingle(c.Writer, c.Request, res.Info.Name, res.Info.Modified, res.Info.Size,
			contentTypeFor(res.Info.Name), res.Provider())
		return
	}
	stream.Serve(s.downloadWriter(c), c.Request, res.Info.Name, res.Info.Modified, res.Info.Size,
		contentTypeFor(res.Info.Name), res.Provider(), res.Accel.Threads, res.Accel.ChunkBytes)
}
