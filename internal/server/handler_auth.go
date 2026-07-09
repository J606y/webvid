package server

import (
	"github.com/gin-gonic/gin"

	"newlist/internal/auth"
	"newlist/internal/conf"
)

// POST /api/auth/login
func (s *Server) login(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" || req.Password == "" {
		Fail(c, 400, "请输入用户名和密码")
		return
	}
	ip := c.ClientIP()
	if !s.limiter.Allow(ip) {
		Fail(c, 429, "失败次数过多，请 5 分钟后再试")
		return
	}
	u, err := s.users.GetByUsername(req.Username)
	if err != nil || !u.Enabled || !auth.VerifyPassword(u.PasswordHash, req.Password) {
		s.limiter.Fail(ip)
		Fail(c, 401, "用户名或密码错误")
		return
	}
	s.limiter.Success(ip)
	token, exp, err := auth.SignToken(u.ID, s.secret)
	if err != nil {
		Fail(c, 500, "签发令牌失败")
		return
	}
	OK(c, gin.H{"token": token, "expires_at": exp.UTC(), "user": u})
}

// GET /api/auth/me
func (s *Server) me(c *gin.Context) {
	OK(c, getUser(c))
}

// GET /api/public/settings —— 登录页也需要站点标题，不要求认证。
// upload_workers 供网页上传队列决定同传文件数（非敏感）。
func (s *Server) publicSettings(c *gin.Context) {
	OK(c, gin.H{"site_title": s.conf.SiteTitle(), "version": conf.Version,
		"upload_workers": s.conf.UploadWorkers()})
}
