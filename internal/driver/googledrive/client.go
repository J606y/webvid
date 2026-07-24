// Package googledrive 实现 Google Drive 驱动（Drive API v3，行为自实现，不含第三方 SDK）。
// Drive 以文件 ID 寻址，驱动内部做「相对路径 ↔ 文件 ID」解析与 TTL 缓存（见 googledrive.go）。
// 认证走 OAuth2 refresh_token；后台一键授权流程见 oauth.go / handler_googledrive.go。
package googledrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"newlist/internal/driver"
)

const (
	authURLBase = "https://accounts.google.com/o/oauth2/v2/auth"
	driveScope  = "https://www.googleapis.com/auth/drive"
)

// 端点用 var 以便单测指向 httptest mock。
var (
	driveAPIBase    = "https://www.googleapis.com/drive/v3"
	driveUploadBase = "https://www.googleapis.com/upload/drive/v3"
	tokenURL        = "https://oauth2.googleapis.com/token"
)

// 长耗时请求（上传/拉流）共用；不设全局 Timeout，超时全靠 ctx。
var httpClient = &http.Client{}

// client 封装 Drive 访问：token 缓存与刷新、统一请求、错误映射。
type client struct {
	clientID     string
	clientSecret string

	mu           sync.Mutex
	refreshToken string
	accessToken  string
	expiresAt    time.Time

	persist func(driver.Config) error // 配置回写（refresh_token 轮换，Google 很少触发但保留）
	cfg     driver.Config
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

var (
	tokenAttempts  = 4
	tokenRetryBase = 500 * time.Millisecond // 退避 0.5s→1s→2s，测试可调小
)

// transientTokenError 标记可重试的 token 获取失败（网络层错误或 5xx）。
type transientTokenError struct{ err error }

func (e *transientTokenError) Error() string { return e.err.Error() }
func (e *transientTokenError) Unwrap() error  { return e.err }

// token 返回有效 access_token；提前 5 分钟过期。瞬时失败带退避重试。
func (c *client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" && time.Now().Before(c.expiresAt.Add(-5*time.Minute)) {
		return c.accessToken, nil
	}
	var lastErr error
	for attempt := 0; attempt < tokenAttempts; attempt++ {
		if attempt > 0 {
			var te *transientTokenError
			if !errors.As(lastErr, &te) && attempt >= 2 {
				break // 配置类错误（invalid_grant 等）多试无益
			}
			select {
			case <-time.After(tokenRetryBase << (attempt - 1)):
			case <-ctx.Done():
				return "", lastErr
			}
		}
		if err := c.refreshLocked(ctx); err != nil {
			lastErr = err
			continue
		}
		return c.accessToken, nil
	}
	var te *transientTokenError
	if errors.As(lastErr, &te) {
		return "", te.err
	}
	return "", lastErr
}

// forceRefresh 丢弃缓存的 access_token（收到 401 时用）。
func (c *client) forceRefresh() {
	c.mu.Lock()
	c.accessToken = ""
	c.mu.Unlock()
}

func (c *client) refreshLocked(ctx context.Context) error {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("refresh_token", c.refreshToken)

	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return &transientTokenError{err} // 网络层失败
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &transientTokenError{err}
	}
	var tr tokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		perr := fmt.Errorf("googledrive: token 响应解析失败(HTTP %d)", resp.StatusCode)
		if resp.StatusCode >= 500 {
			return &transientTokenError{perr}
		}
		return perr
	}
	if resp.StatusCode != 200 || tr.AccessToken == "" {
		msg := tr.ErrorDesc
		if msg == "" {
			msg = tr.Error
		}
		// invalid_grant 常见于：refresh_token 失效/被撤销，或 OAuth 应用仍是「测试」状态令 token 7 天过期。
		ferr := fmt.Errorf("googledrive: 获取 token 失败(HTTP %d): %s", resp.StatusCode, msg)
		if resp.StatusCode >= 500 {
			return &transientTokenError{ferr}
		}
		return ferr
	}
	c.accessToken = tr.AccessToken
	c.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	if tr.RefreshToken != "" && tr.RefreshToken != c.refreshToken { // Google 一般不轮换，兜底处理
		c.refreshToken = tr.RefreshToken
		if c.cfg != nil {
			c.cfg["refresh_token"] = tr.RefreshToken
			if c.persist != nil {
				if err := c.persist(c.cfg); err != nil {
					log.Printf("[googledrive] refresh_token 回写失败（不影响本次运行）: %v", err)
				}
			}
		}
	}
	return nil
}

// gError 是 Drive API 的标准错误体。
type gError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Errors  []struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"error"`
}

func (e *gError) reason() string {
	if len(e.Error.Errors) > 0 {
		return e.Error.Errors[0].Reason
	}
	return ""
}

// mapDriveError 把 Drive 错误映射到 driver 哨兵，其余包 ErrUpstream 透传原始信息。
func mapDriveError(status int, ge *gError) error {
	reason := ge.reason()
	switch {
	case status == 404 || reason == "notFound":
		return driver.ErrNotFound
	case reason == "storageQuotaExceeded":
		return driver.ErrQuota
	case status == 403 && (reason == "insufficientPermissions" ||
		reason == "insufficientFilePermissions" || reason == "appNotAuthorizedToFile"):
		return driver.ErrDenied
	}
	code := reason
	if code == "" {
		code = strconv.Itoa(ge.Error.Code)
	}
	return fmt.Errorf("%w：%s(HTTP %d) %s", driver.ErrUpstream, code, status, ge.Error.Message)
}

// isRateLimited 判断 403/429 是否为限流（应退避重试而非直接失败）。
func isRateLimited(status int, reason string) bool {
	if status == 429 {
		return true
	}
	return status == 403 && (reason == "rateLimitExceeded" || reason == "userRateLimitExceeded")
}

// req 发送带鉴权的 Drive JSON 请求。q 查询参数（可 nil），body JSON 对象（可 nil），out 解析响应（可 nil）。
// 401 强制刷新 token 重试 1 次；限流按 Retry-After（≤5s）退避重试 1 次。
func (c *client) req(ctx context.Context, method, rawURL string, q url.Values, body any, out any) error {
	u := rawURL
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	retried401, retriedRL := false, false
	for {
		var br io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return err
			}
			br = strings.NewReader(string(b))
		}
		req, err := http.NewRequestWithContext(ctx, method, u, br)
		if err != nil {
			return err
		}
		tok, err := c.token(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
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
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out != nil && len(data) > 0 {
				return json.Unmarshal(data, out)
			}
			return nil
		}
		var ge gError
		json.Unmarshal(data, &ge)
		reason := ge.reason()
		if resp.StatusCode == 401 && !retried401 {
			retried401 = true
			c.forceRefresh()
			continue
		}
		if isRateLimited(resp.StatusCode, reason) && !retriedRL {
			retriedRL = true
			wait := 2 * time.Second
			if ra, _ := strconv.Atoi(resp.Header.Get("Retry-After")); ra > 0 {
				wait = time.Duration(ra) * time.Second
			}
			if wait > 5*time.Second {
				wait = 5 * time.Second
			}
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return mapDriveError(resp.StatusCode, &ge)
	}
}
