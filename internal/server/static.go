package server

import (
	iofs "io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"newlist/public"
)

// registerStatic 服务嵌入的前端产物：
// 静态文件按需缓存；未命中的非 /api 路径回落 index.html（SPA 深链刷新关键）。
func (s *Server) registerStatic(r *gin.Engine) {
	sub, err := iofs.Sub(public.Dist, "dist")
	if err != nil {
		panic("public/dist 未嵌入: " + err.Error())
	}
	fileServer := http.FileServerFS(sub)

	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if p == "/api" || strings.HasPrefix(p, "/api/") {
			Fail(c, 404, "接口不存在")
			return
		}
		rel := strings.TrimPrefix(p, "/")
		if rel != "" && rel != "index.html" {
			if st, err := iofs.Stat(sub, rel); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "/assets/") {
					// 带内容哈希的构建产物，永久缓存
					c.Header("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}
		data, err := iofs.ReadFile(sub, "index.html")
		if err != nil {
			Fail(c, 500, "前端资源缺失，请先构建 frontend")
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.Data(200, "text/html; charset=utf-8", data)
	})
}
