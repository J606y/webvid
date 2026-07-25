package server

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"

	"newlist/internal/driver"
	"newlist/internal/driver/telegram"
	"newlist/internal/util"
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
		Fail(c, 500, "存储配置已损坏，无法读取")
		return 0, nil, false
	}
	return id, cfg, true
}

// GET /api/admin/telegram/:id/status —— 该存储当前有没有未过期的登录会话。
// 登录弹窗打开时先问一次：会话活着的话，「发送验证码」按的其实是换通道重发，
// 按钮就该那么写。只靠前端本地状态判断的话，换个标签页打开就又说谎了。
func (s *Server) tgStatus(c *gin.Context) {
	id, _, ok := s.tgStorageCfg(c)
	if !ok {
		return
	}
	OK(c, telegram.Logins.Pending(id))
}

// POST /api/admin/telegram/:id/send_code —— 向配置的手机号发送登录验证码。
// 返回验证码实际投递通道（App 内消息/短信/电话）；resumed=true 表示本次复用了此前
// 未过期的登录会话（换通道重发），并非一次全新验证——前端据此把按钮文案与真实行为对齐。
func (s *Server) tgSendCode(c *gin.Context) {
	id, cfg, ok := s.tgStorageCfg(c)
	if !ok {
		return
	}
	info, err := telegram.Logins.SendCode(c.Request.Context(), id, cfg)
	if err != nil {
		Fail(c, 502, util.Humanize(err))
		return
	}
	OK(c, info)
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
		Fail(c, 400, "请输入验证码")
		return
	}
	sess, needPwd, err := telegram.Logins.SignIn(
		c.Request.Context(), id, strings.TrimSpace(req.Code), req.Password)
	if err != nil {
		Fail(c, 502, util.Humanize(err))
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
