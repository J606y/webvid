package server

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"

	"newlist/internal/driver"
	"newlist/internal/driver/telegram"
)

// tgStorageCfg 读取并校验 telegram 存储行，返回其配置。
func (s *Server) tgStorageCfg(c *gin.Context) (int64, driver.Config, bool) {
	id, ok := paramID(c)
	if !ok {
		return 0, nil, false
	}
	var drv, cfgJSON string
	if err := s.db.QueryRow(`SELECT driver, config FROM storages WHERE id=?`, id).
		Scan(&drv, &cfgJSON); err != nil {
		Fail(c, 404, "存储不存在")
		return 0, nil, false
	}
	if drv != "telegram" {
		Fail(c, 400, "该存储不是 Telegram 驱动")
		return 0, nil, false
	}
	cfg := driver.Config{}
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		Fail(c, 500, "存储配置损坏: "+err.Error())
		return 0, nil, false
	}
	return id, cfg, true
}

// POST /api/admin/telegram/:id/send_code —— 向配置的手机号发送登录验证码。
func (s *Server) tgSendCode(c *gin.Context) {
	id, cfg, ok := s.tgStorageCfg(c)
	if !ok {
		return
	}
	if err := telegram.Logins.SendCode(c.Request.Context(), id, cfg); err != nil {
		Fail(c, 502, err.Error())
		return
	}
	OK(c, nil)
}

// POST /api/admin/telegram/:id/sign_in {code, password?}
// 需要两步密码时返回 {need_password:true}；成功则把会话写回配置并重载挂载。
func (s *Server) tgSignIn(c *gin.Context) {
	id, cfg, ok := s.tgStorageCfg(c)
	if !ok {
		return
	}
	var req struct {
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		Fail(c, 400, "code 不能为空")
		return
	}
	sess, needPwd, err := telegram.Logins.SignIn(
		c.Request.Context(), id, strings.TrimSpace(req.Code), req.Password)
	if err != nil {
		Fail(c, 502, err.Error())
		return
	}
	if needPwd {
		OK(c, gin.H{"need_password": true})
		return
	}
	cfg["session"] = sess
	b, _ := json.Marshal(cfg)
	if _, err := s.db.Exec(`UPDATE storages SET config=? WHERE id=?`, string(b), id); err != nil {
		Fail500(c, err)
		return
	}
	s.afterStorageChange(c)
}
