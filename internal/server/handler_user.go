package server

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"newlist/internal/auth"
	"newlist/internal/user"
)

func userError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, user.ErrNotFound):
		Fail(c, 404, "用户不存在")
	case errors.Is(err, user.ErrExists):
		Fail(c, 409, "用户名已存在")
	case errors.Is(err, user.ErrLastAdmin):
		Fail(c, 400, err.Error())
	default:
		Fail500(c, err)
	}
}

func paramID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		Fail(c, 400, "无效的 ID")
		return 0, false
	}
	return id, true
}

// GET /api/admin/users
func (s *Server) userList(c *gin.Context) {
	list, err := s.users.List()
	if err != nil {
		Fail500(c, err)
		return
	}
	OK(c, list)
}

// POST /api/admin/users {username,password,role,base_path,can_write}
// password 为空时生成随机密码并随响应返回一次。
func (s *Server) userCreate(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		BasePath string `json:"base_path"`
		CanWrite bool   `json:"can_write"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
		Fail(c, 400, "用户名不能为空")
		return
	}
	pw := req.Password
	generated := false
	if pw == "" {
		pw = auth.RandomPassword(12)
		generated = true
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		Fail500(c, err)
		return
	}
	u, err := s.users.Create(req.Username, hash, req.Role, req.BasePath, req.CanWrite)
	if err != nil {
		userError(c, err)
		return
	}
	data := gin.H{"user": u}
	if generated {
		data["password"] = pw // 仅此一次返回，前端提示保存
	}
	OK(c, data)
}

// PUT /api/admin/users/:id {username,role,base_path,can_write,enabled,password?}
// password 非空时同时重置密码。
func (s *Server) userUpdate(c *gin.Context) {
	id, ok := paramID(c)
	if !ok {
		return
	}
	var req struct {
		Username string `json:"username"`
		Role     string `json:"role"`
		BasePath string `json:"base_path"`
		CanWrite bool   `json:"can_write"`
		Enabled  bool   `json:"enabled"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
		Fail(c, 400, "参数不完整")
		return
	}
	if req.Role != "admin" {
		req.Role = "user"
	}
	u := &user.User{ID: id, Username: req.Username, Role: req.Role,
		BasePath: req.BasePath, CanWrite: req.CanWrite, Enabled: req.Enabled}
	if err := s.users.Update(u); err != nil {
		userError(c, err)
		return
	}
	if req.Password != "" {
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			Fail500(c, err)
			return
		}
		if err := s.users.UpdatePassword(id, hash); err != nil {
			Fail500(c, err)
			return
		}
	}
	out, err := s.users.GetByID(id)
	if err != nil {
		userError(c, err)
		return
	}
	OK(c, out)
}

// DELETE /api/admin/users/:id
func (s *Server) userDelete(c *gin.Context) {
	id, ok := paramID(c)
	if !ok {
		return
	}
	if id == getUser(c).ID {
		Fail(c, 400, "不能删除当前登录的账号")
		return
	}
	if err := s.users.Delete(id); err != nil {
		userError(c, err)
		return
	}
	OK(c, nil)
}
