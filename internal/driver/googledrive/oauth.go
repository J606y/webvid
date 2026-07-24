package googledrive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// AuthURL 构造 Google OAuth 同意页 URL。access_type=offline + prompt=consent 保证每次都返回 refresh_token。
func AuthURL(clientID, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", driveScope)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("state", state)
	return authURLBase + "?" + q.Encode()
}

// Exchange 用授权码换取 token；返回 refresh_token。
func Exchange(ctx context.Context, clientID, clientSecret, code, redirectURI string) (refreshToken string, err error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("redirect_uri", redirectURI)

	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr tokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("googledrive: 授权响应解析失败(HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != 200 || tr.RefreshToken == "" {
		msg := tr.ErrorDesc
		if msg == "" {
			msg = tr.Error
		}
		if msg == "" && tr.RefreshToken == "" {
			msg = "未返回 refresh_token（请确认 OAuth 应用已发布为『生产』，且是首次授权/已勾选离线访问）"
		}
		return "", fmt.Errorf("googledrive: 授权失败(HTTP %d): %s", resp.StatusCode, msg)
	}
	return tr.RefreshToken, nil
}

// --- 后台一键授权的 pending state 注册表（镜像 telegram LoginManager）---

const oauthTTL = 10 * time.Minute

type pendingAuth struct {
	storageID   int64
	redirectURI string // 与生成 auth_url 时一致，换 token 必须复用同一个
	expires     time.Time
}

// OAuthManager 在 auth_url 与 callback 两个请求之间保存待完成的授权（进程级单例）。
type OAuthManager struct {
	mu      sync.Mutex
	pending map[string]pendingAuth // state → pending
}

// OAuth 供 server 层调用。
var OAuth = &OAuthManager{pending: map[string]pendingAuth{}}

// Put 登记一个待完成授权（state 由调用方随机生成），顺带清理过期项。
func (m *OAuthManager) Put(state string, storageID int64, redirectURI string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for k, v := range m.pending {
		if now.After(v.expires) {
			delete(m.pending, k)
		}
	}
	m.pending[state] = pendingAuth{storageID: storageID, redirectURI: redirectURI, expires: now.Add(oauthTTL)}
}

// Take 取出并消费一个 state（单次有效）；过期/不存在返回 ok=false。
func (m *OAuthManager) Take(state string) (storageID int64, redirectURI string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, exists := m.pending[state]
	delete(m.pending, state)
	if !exists || time.Now().After(p.expires) {
		return 0, "", false
	}
	return p.storageID, p.redirectURI, true
}
