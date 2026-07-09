package pikpak

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"newlist/internal/driver"
)

// 长耗时请求共用；不设全局 Timeout，超时全靠 ctx。
var httpClient = &http.Client{}

// client 封装 PikPak 访问：token/captcha_token 维护、统一请求、错误映射。
type client struct {
	authBase  string
	driveBase string
	pf        platformConsts

	username string
	password string
	deviceID string

	mu             sync.Mutex
	accessToken    string
	refreshToken   string
	expiresAt      time.Time
	userID         string
	captchaToken   string // 运行期 captcha_token（按请求 action 自取/刷新）
	initialCaptcha string // 用户贴入的、已过人机验证的 captcha_token，仅供首次账密登录，一次性

	persist func(driver.Config) error // 配置回写（refresh_token 轮换 / device_id 生成）
	cfg     driver.Config
	now     func() time.Time // 可注入，captcha 签名时间戳用
}

// apiError 是 PikPak 的业务错误体（HTTP 可能 200 也可能 4xx，一律看 error_code）。
type apiError struct {
	Code int64  `json:"error_code"`
	Err  string `json:"error"`
	Desc string `json:"error_description"`
}

func (e *apiError) message() string {
	if e.Desc != "" {
		return e.Desc
	}
	return e.Err
}

// tokenResp 是 /v1/auth/token 与 /v1/auth/signin 的公共响应投影。
type tokenResp struct {
	apiError
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Sub          string `json:"sub"`
}

// token 返回有效 access_token；提前 5 分钟过期。
func (c *client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" && c.now().Before(c.expiresAt.Add(-5*time.Minute)) {
		return c.accessToken, nil
	}
	if err := c.refreshLocked(ctx); err != nil {
		return "", err
	}
	return c.accessToken, nil
}

// forceExpire 丢弃缓存的 access_token（收到 token 过期错误码时用）。
func (c *client) forceExpire() {
	c.mu.Lock()
	c.accessToken = ""
	c.mu.Unlock()
}

// refreshLocked 用 refresh_token 换新 token；refresh_token 缺失或失效(4126)时转账密登录。
func (c *client) refreshLocked(ctx context.Context) error {
	if c.refreshToken == "" {
		return c.loginLocked(ctx)
	}
	var tr tokenResp
	err := c.authPost(ctx, "/v1/auth/token", map[string]string{
		"client_id":     c.pf.clientID,
		"client_secret": c.pf.clientSecret,
		"grant_type":    "refresh_token",
		"refresh_token": c.refreshToken,
	}, &tr)
	if err != nil {
		return err
	}
	if tr.Code == 4126 { // refresh_token 已失效
		if c.username != "" && c.password != "" {
			return c.loginLocked(ctx)
		}
		return fmt.Errorf("pikpak: refresh_token 已失效，请重新填写或补充账密")
	}
	if tr.Code != 0 || tr.AccessToken == "" {
		return fmt.Errorf("pikpak: 刷新 token 失败(%d): %s", tr.Code, tr.message())
	}
	c.applyTokensLocked(&tr)
	return nil
}

// loginLocked 账密登录：先取登录场景 captcha_token，再 signin。
// 若用户已贴入验证过的 captcha_token，则直接用它（绕过人机验证墙），用后作废。
func (c *client) loginLocked(ctx context.Context) error {
	if c.username == "" || c.password == "" {
		return fmt.Errorf("pikpak: 未配置账密且无有效 refresh_token，无法登录")
	}
	captchaTok := c.initialCaptcha
	if captchaTok != "" {
		c.initialCaptcha = "" // 一次性：用后清空，避免下次登录复用失效值
	} else {
		if err := c.captchaInitLocked(ctx, "POST:/v1/auth/signin", loginMeta(c.username)); err != nil {
			return err
		}
		captchaTok = c.captchaToken
	}
	var tr tokenResp
	err := c.authPost(ctx, "/v1/auth/signin", map[string]string{
		"captcha_token": captchaTok,
		"client_id":     c.pf.clientID,
		"client_secret": c.pf.clientSecret,
		"username":      c.username,
		"password":      c.password,
	}, &tr)
	if err != nil {
		return err
	}
	if tr.Code != 0 || tr.AccessToken == "" {
		return fmt.Errorf("pikpak: 登录失败(%d): %s", tr.Code, tr.message())
	}
	c.applyTokensLocked(&tr)
	return nil
}

func (c *client) applyTokensLocked(tr *tokenResp) {
	c.accessToken = tr.AccessToken
	c.expiresAt = c.now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	if tr.Sub != "" {
		c.userID = tr.Sub
	}
	if tr.RefreshToken != "" && tr.RefreshToken != c.refreshToken {
		c.refreshToken = tr.RefreshToken
		if c.cfg != nil {
			c.cfg["refresh_token"] = tr.RefreshToken
			if c.persist != nil {
				if err := c.persist(c.cfg); err != nil {
					log.Printf("[pikpak] refresh_token 回写失败（不影响本次运行）: %v", err)
				}
			}
		}
	}
}

// captchaInitLocked 调 shield/captcha/init 换 captcha_token（不带 Bearer，不经 req 以免递归）。
func (c *client) captchaInitLocked(ctx context.Context, action string, meta map[string]string) error {
	body := map[string]any{
		"action":        action,
		"captcha_token": c.captchaToken,
		"client_id":     c.pf.clientID,
		"device_id":     c.deviceID,
		"meta":          meta,
		"redirect_uri":  redirectURI,
	}
	var out struct {
		apiError
		CaptchaToken string `json:"captcha_token"`
		URL          string `json:"url"`
	}
	if err := c.postJSON(ctx, c.authBase+"/v1/shield/captcha/init?client_id="+
		url.QueryEscape(c.pf.clientID), body, &out); err != nil {
		return err
	}
	if out.URL != "" {
		return fmt.Errorf("pikpak: 触发人机验证，请先在官方客户端登录一次后重试")
	}
	if out.Code != 0 || out.CaptchaToken == "" {
		return fmt.Errorf("pikpak: 获取 captcha_token 失败(%d): %s", out.Code, out.message())
	}
	c.captchaToken = out.CaptchaToken
	return nil
}

// refreshCaptcha 刷新登录后场景的 captcha_token（drive 请求收到 error_code=9 时用）。
func (c *client) refreshCaptcha(ctx context.Context, action string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts := c.now().UnixMilli()
	meta := map[string]string{
		"client_version": c.pf.clientVersion,
		"package_name":   c.pf.packageName,
		"user_id":        c.userID,
		"timestamp":      fmt.Sprint(ts),
		"captcha_sign":   captchaSign(c.pf.clientID, c.pf.clientVersion, c.pf.packageName, c.deviceID, ts, c.pf.salts),
	}
	return c.captchaInitLocked(ctx, action, meta)
}

// authPost 发认证域 POST（query 带 client_id），解析 JSON 到 out。
func (c *client) authPost(ctx context.Context, p string, body any, out any) error {
	return c.postJSON(ctx, c.authBase+p+"?client_id="+url.QueryEscape(c.pf.clientID), body, out)
}

func (c *client) postJSON(ctx context.Context, u string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, u, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.pf.userAgent)
	req.Header.Set("X-Device-ID", c.deviceID)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("pikpak: 响应解析失败(HTTP %d)", resp.StatusCode)
	}
	return nil
}

// req 发送带鉴权的 drive API 请求并解析 JSON 到 out（可为 nil）。
// error_code 4122/4121/16（token 过期）→ 强刷重试 1 次；
// 9（captcha_token 过期）→ 重取 captcha 重试 1 次；10 → 明确的限频错误。
func (c *client) req(ctx context.Context, method, u string, query url.Values, body any, out any) error {
	retriedAuth, retriedCaptcha := false, false
	for {
		var br io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return err
			}
			br = strings.NewReader(string(b))
		}
		full := u
		if len(query) > 0 {
			full = u + "?" + query.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, method, full, br)
		if err != nil {
			return err
		}
		tok, err := c.token(ctx)
		if err != nil {
			return err
		}
		c.mu.Lock()
		ct := c.captchaToken
		c.mu.Unlock()
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("X-Device-ID", c.deviceID)
		req.Header.Set("X-Captcha-Token", ct)
		req.Header.Set("User-Agent", c.pf.userAgent)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}

		var ae apiError
		_ = json.Unmarshal(data, &ae)
		switch {
		case ae.Code == 0 && resp.StatusCode < 300:
			if out != nil && len(data) > 0 {
				return json.Unmarshal(data, out)
			}
			return nil
		case (ae.Code == 4122 || ae.Code == 4121 || ae.Code == 16) && !retriedAuth:
			retriedAuth = true
			c.forceExpire()
			continue
		case ae.Code == 9 && !retriedCaptcha:
			retriedCaptcha = true
			if err := c.refreshCaptcha(ctx, actionOf(method, u)); err != nil {
				return err
			}
			continue
		case ae.Code == 10:
			return fmt.Errorf("pikpak: 操作频繁，请稍后再试")
		}
		if resp.StatusCode == 404 || strings.Contains(ae.Err, "not_found") {
			return driver.ErrNotFound
		}
		return fmt.Errorf("pikpak: 请求失败(HTTP %d, code %d): %s", resp.StatusCode, ae.Code, ae.message())
	}
}

// actionOf 构造 captcha 的 action 值：METHOD:/path（不含主机与 query）。
func actionOf(method, u string) string {
	if p, err := url.Parse(u); err == nil {
		return method + ":" + p.Path
	}
	return method + ":"
}

var emailRe = regexp.MustCompile(`^\w+([-+.]\w+)*@\w+([-.]\w+)*\.\w+([-.]\w+)*$`)
var digitsRe = regexp.MustCompile(`^\d{11,18}$`)

// loginMeta 按用户名形态生成登录场景 captcha meta：邮箱 / 手机号 / 用户名。
func loginMeta(username string) map[string]string {
	switch {
	case emailRe.MatchString(username):
		return map[string]string{"email": username}
	case digitsRe.MatchString(username):
		return map[string]string{"phone_number": username}
	default:
		return map[string]string{"username": username}
	}
}
