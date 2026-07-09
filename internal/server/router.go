package server

import (
	"log"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// Router 组装全部路由。
func (s *Server) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// 受信代理：反代后据此从 X-Forwarded-For 取真实客户端 IP（登录限流/日志用）。
	// NL_TRUSTED_PROXIES 逗号分隔 CIDR/IP，缺省信回环 + 内网段（同机/局域网反代）。
	if err := r.SetTrustedProxies(trustedProxies()); err != nil {
		log.Printf("[server] 配置受信代理失败: %v", err)
	}
	r.Use(gin.Recovery())
	r.Use(securityHeaders())

	api := r.Group("/api")
	// 公开
	api.GET("/ping", func(c *gin.Context) { OK(c, "pong") })
	api.POST("/auth/login", s.login)
	api.GET("/public/settings", s.publicSettings)

	// 登录后
	authed := api.Group("", s.Authed())
	authed.GET("/auth/me", s.me)
	authed.GET("/fs/get", s.fsGet)
	authed.GET("/fs/list", s.fsList)
	authed.GET("/fs/search", s.fsSearch)
	authed.GET("/media/list", s.mediaList)
	authed.GET("/media/groups", s.mediaGroups)
	authed.GET("/media/history", s.mediaHistory)
	authed.GET("/media/progress", s.mediaProgress)
	authed.POST("/media/played", s.mediaPlayed)
	authed.GET("/video/info", s.videoInfo)
	authed.GET("/video/hls/*path", s.videoHLS)
	authed.GET("/thumb/*path", s.thumbHandler)
	authed.GET("/raw/*path", s.rawHandler)
	authed.HEAD("/raw/*path", s.rawHandler)
	authed.GET("/tasks", s.taskList)
	authed.POST("/tasks/:id/cancel", s.taskCancel)
	authed.POST("/tasks/:id/retry", s.taskRetry)
	authed.POST("/tasks/:id/remove", s.taskRemove)
	authed.DELETE("/tasks/done", s.taskClearDone)

	// 写权限
	w := authed.Group("", s.CanWrite())
	w.POST("/fs/mkdir", s.fsMkdir)
	w.POST("/fs/rename", s.fsRename)
	w.POST("/fs/remove", s.fsRemove)
	w.POST("/fs/move", s.fsMoveCopy(true))
	w.POST("/fs/copy", s.fsMoveCopy(false))
	w.PUT("/fs/upload", s.fsUpload)
	w.POST("/fs/offline", s.fsOffline)

	// 管理员
	admin := authed.Group("/admin", s.AdminOnly())
	admin.GET("/users", s.userList)
	admin.POST("/users", s.userCreate)
	admin.PUT("/users/:id", s.userUpdate)
	admin.DELETE("/users/:id", s.userDelete)
	admin.GET("/drivers", s.driverList)
	admin.GET("/storages", s.storageList)
	admin.POST("/storages", s.storageCreate)
	admin.PUT("/storages/:id", s.storageUpdate)
	admin.DELETE("/storages/:id", s.storageDelete)
	admin.POST("/storages/:id/reload", s.storageReload)
	admin.POST("/telegram/:id/send_code", s.tgSendCode)
	admin.POST("/telegram/:id/sign_in", s.tgSignIn)
	admin.GET("/settings", s.settingsGet)
	admin.PUT("/settings", s.settingsPut)
	admin.GET("/index/progress", s.indexProgress)
	admin.POST("/index/rebuild", s.indexRebuild)
	admin.GET("/preload/progress", s.preloadProgress)
	admin.POST("/preload/run", s.preloadRun)

	s.registerStatic(r)
	return r
}

// securityHeaders 全站安全响应头：禁 MIME 嗅探、不外泄 Referer（含 URL 里的 ?token=）、
// 禁被第三方 iframe 套（防点击劫持）。
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		c.Next()
	}
}

// trustedProxies 解析 NL_TRUSTED_PROXIES（逗号分隔 CIDR/IP）；缺省信回环 + 内网段。
func trustedProxies() []string {
	if v := strings.TrimSpace(os.Getenv("NL_TRUSTED_PROXIES")); v != "" {
		var out []string
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}
}
