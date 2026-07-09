package server

import (
	"strings"

	"github.com/gin-gonic/gin"

	"newlist/internal/auth"
	"newlist/internal/user"
)

const userKey = "user"

// getUser 取出 Authed 中间件放入的用户（仅在 Authed 之后的 handler 使用）。
func getUser(c *gin.Context) *user.User {
	v, _ := c.Get(userKey)
	return v.(*user.User)
}

// Authed 认证：Authorization: Bearer <jwt> 或 ?token=<jwt>
// （img/video 标签带不了 Header，媒体类 URL 走 query 形式）。
func (s *Server) Authed() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := ""
		if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok = strings.TrimPrefix(h, "Bearer ")
		} else if q := c.Query("token"); q != "" {
			tok = q
		}
		if tok == "" {
			Fail(c, 401, "未登录")
			c.Abort()
			return
		}
		id, err := auth.ParseToken(tok, s.secret)
		if err != nil {
			Fail(c, 401, "登录已过期，请重新登录")
			c.Abort()
			return
		}
		u, err := s.users.GetByID(id)
		if err != nil || !u.Enabled {
			Fail(c, 401, "用户不存在或已停用")
			c.Abort()
			return
		}
		c.Set(userKey, u)
		c.Next()
	}
}

// CanWrite 写权限：管理员或 can_write 用户。
func (s *Server) CanWrite() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !getUser(c).AllowWrite() {
			Fail(c, 403, "无写入权限")
			c.Abort()
			return
		}
		c.Next()
	}
}

// AdminOnly 管理员专属。
func (s *Server) AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !getUser(c).IsAdmin() {
			Fail(c, 403, "需要管理员权限")
			c.Abort()
			return
		}
		c.Next()
	}
}
