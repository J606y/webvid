package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"newlist/internal/driver"
	"newlist/internal/driver/googledrive"
	"newlist/internal/util"
)

// gdStorageCfg 读取并校验 googledrive 存储行，返回其配置。
func (s *Server) gdStorageCfg(c *gin.Context) (int64, driver.Config, bool) {
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
	if drv != "googledrive" {
		Fail(c, 400, "该存储不是 Google Drive 驱动")
		return 0, nil, false
	}
	cfg := driver.Config{}
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		Fail(c, 500, "存储配置已损坏，无法读取")
		return 0, nil, false
	}
	return id, cfg, true
}

// POST /api/admin/googledrive/:id/auth_url {origin} —— 生成 Google OAuth 同意页 URL。
// redirect_uri 取自前端传来的浏览器 origin（回调页必须与 Google 控制台登记的一致）。
func (s *Server) gdAuthURL(c *gin.Context) {
	id, cfg, ok := s.gdStorageCfg(c)
	if !ok {
		return
	}
	clientID := strings.TrimSpace(cfg["client_id"])
	if clientID == "" || strings.TrimSpace(cfg["client_secret"]) == "" {
		Fail(c, 400, "请先填写并保存 client_id 与 client_secret，再点授权")
		return
	}
	var req struct {
		Origin string `json:"origin"`
	}
	_ = c.ShouldBindJSON(&req)
	origin := strings.TrimRight(strings.TrimSpace(req.Origin), "/")
	if origin == "" { // 兜底：从请求推断（反代后据 X-Forwarded-Proto）
		scheme := "https"
		if p := c.GetHeader("X-Forwarded-Proto"); p != "" {
			scheme = p
		} else if c.Request.TLS == nil {
			scheme = "http"
		}
		origin = scheme + "://" + c.Request.Host
	}
	redirectURI := origin + "/api/googledrive/callback"

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		Fail500(c, err)
		return
	}
	state := hex.EncodeToString(b)
	googledrive.OAuth.Put(state, id, redirectURI)
	OK(c, gin.H{"auth_url": googledrive.AuthURL(clientID, redirectURI, state)})
}

// GET /api/googledrive/callback?code&state —— Google 授权回调（非鉴权路由，靠单次有效的 state 保护）。
// Google 302 浏览器过来时不带我们的 JWT，故不能挂在 admin 组下。
//
// state 在这里统一先取（即便是取消/拒绝分支也一样）：Google 的错误重定向同样会带回
// 我们下发的 state，借它拿到 storageID 才能让下面的结果页把"这是哪条存储的授权"
// 广播回后台标签页；state 无效/过期时 id 为 0，广播里带不出具体归属，由前端兜底处理。
func (s *Server) gdCallback(c *gin.Context) {
	id, redirectURI, stateOK := googledrive.OAuth.Take(c.Query("state"))
	if errStr := c.Query("error"); errStr != "" {
		gdCallbackHTML(c, id, false, "授权未完成：你在 Google 页面取消或拒绝了授权，请回后台重新点「授权」")
		return
	}
	if !stateOK {
		gdCallbackHTML(c, id, false, "授权会话已过期或无效，请回后台重新点「授权」")
		return
	}
	code := c.Query("code")
	if code == "" {
		gdCallbackHTML(c, id, false, "未收到授权码")
		return
	}
	var cfgJSON string
	if err := s.db.QueryRow(`SELECT config FROM storages WHERE id=? AND driver='googledrive'`, id).
		Scan(&cfgJSON); err != nil {
		gdCallbackHTML(c, id, false, "存储不存在")
		return
	}
	cfg := driver.Config{}
	json.Unmarshal([]byte(cfgJSON), &cfg)
	refresh, err := googledrive.Exchange(c.Request.Context(),
		cfg["client_id"], cfg["client_secret"], code, redirectURI)
	if err != nil {
		gdCallbackHTML(c, id, false, util.Humanize(err))
		return
	}
	cfg["refresh_token"] = refresh
	b, _ := json.Marshal(cfg)
	if _, err := s.db.Exec(`UPDATE storages SET config=? WHERE id=?`, string(b), id); err != nil {
		gdCallbackHTML(c, id, false, "授权成功，但保存失败，请回后台重试")
		return
	}
	// 重载挂载让新 token 生效（失败不阻断，用户可回后台手动重载）。
	if err := s.fs.Reload(c.Request.Context()); err == nil {
		s.index.Rebuild()
	}
	gdCallbackHTML(c, id, true, "授权成功！可以关闭此页面，回后台刷新即可看到该存储已就绪。")
}

// gdCallbackHTML 回一个自包含的结果页（回调发生在新标签，用户看完关闭即可）。
//
// 授权是否真的完成不该由用户点了后台弹窗里的哪个按钮决定，而应由这里——服务端已经
// 落盘的结果——说了算。于是顺带用同源 BroadcastChannel 把 {id, ok, message} 广播出去：
// BroadcastChannel 只认同源，不依赖 window.opener，因此后台那边 window.open 加的
// 'noopener'（防止这个新标签反向操纵后台页面）不会挡路。后台页面监听到后据此立即
// 给出准确的成功/失败提示；万一用户中途直接关掉这个标签、广播根本不会发生，后台页面
// 点「已完成/关闭」时也会回查该存储的真实配置与挂载状态兜底，不会卡在"看着像成功"。
func gdCallbackHTML(c *gin.Context, id int64, ok bool, msg string) {
	title, color := "授权失败", "#f56c6c"
	if ok {
		title, color = "授权成功", "#22c55e"
	}
	// json.Marshal 默认会把 <、>、& 转义为 < 等，可安全嵌进 <script> 而不必担心
	// msg 里出现 </script> 之类内容把标签提前截断。
	payload, err := json.Marshal(gin.H{"id": id, "ok": ok, "message": msg})
	if err != nil {
		payload = []byte(`{"id":0,"ok":false,"message":"内部错误"}`)
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, `<!doctype html><html lang="zh"><head><meta charset="utf-8">`+
		`<meta name="viewport" content="width=device-width,initial-scale=1"><title>`+title+`</title></head>`+
		`<body style="font-family:system-ui,-apple-system,sans-serif;background:#0f131c;color:#e5e7eb;`+
		`display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0">`+
		`<div style="text-align:center;padding:24px;max-width:460px">`+
		`<div style="font-size:20px;font-weight:600;color:`+color+`;margin-bottom:12px">`+title+`</div>`+
		`<div style="line-height:1.7">`+html.EscapeString(msg)+`</div></div>`+
		`<script>try{var bc=new BroadcastChannel('webvid-googledrive-auth');bc.postMessage(`+
		string(payload)+`);bc.close()}catch(e){}</script>`+
		`</body></html>`)
}
