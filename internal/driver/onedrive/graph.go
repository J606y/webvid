// Package onedrive 实现 OneDrive 双驱动（Microsoft Graph API，行为自实现，不含第三方代码）：
//   - "onedrive"：委托授权（refresh_token），个人/商业账号
//   - "onedrive_app"：应用授权（client_credentials），访问指定用户的 OneDrive
package onedrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"newlist/internal/driver"
	"newlist/internal/util"
)

// maxThrottleRetries 限流退避重试次数。撞限流是并发转存下的常态而非异常，
// 只重试一次等于把本能自愈的请求直接判死，整个文件夹任务跟着中断。
const maxThrottleRetries = 4

const (
	modeDelegated = "delegated" // refresh_token
	modeApp       = "app"       // client_credentials
)

type endpoints struct{ login, graph string }

func regionEndpoints(region string) endpoints {
	if region == "cn" {
		return endpoints{
			login: "https://login.partner.microsoftonline.cn",
			graph: "https://microsoftgraph.chinacloudapi.cn/v1.0",
		}
	}
	return endpoints{
		login: "https://login.microsoftonline.com",
		graph: "https://graph.microsoft.com/v1.0",
	}
}

// 上传/轮询等长耗时请求共用；不设全局 Timeout，超时全靠 ctx。
var httpClient = &http.Client{}

// client 封装 Graph 访问：token 缓存与刷新、统一请求、错误映射。
type client struct {
	mode      string
	loginBase string
	graphBase string
	driveBase string // {graph}/me/drive 或 {graph}/users/{email}/drive

	// 凭据
	tenantID     string
	clientID     string
	clientSecret string

	mu           sync.Mutex
	refreshToken string
	accessToken  string
	expiresAt    time.Time

	persist func(driver.Config) error // 配置回写（refresh_token 轮换）
	cfg     driver.Config             // 驱动持有的配置副本，回写时整体保存
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// token 获取重试参数：AAD 端点冷启动偶发连接级 EOF（真实账号实测约半数概率，
// 见 PROGRESS.md M6 段），瞬时错误多试几次基本可消化。
var (
	tokenAttempts  = 4
	tokenRetryBase = 500 * time.Millisecond // 退避 0.5s→1s→2s，测试可调小
)

// transientTokenError 标记可重试的 token 获取失败（网络层错误或 AAD 5xx）。
type transientTokenError struct{ err error }

func (e *transientTokenError) Error() string { return e.err.Error() }
func (e *transientTokenError) Unwrap() error { return e.err }

// token 返回有效 access_token；提前 5 分钟过期。
// 瞬时失败最多 tokenAttempts 次带退避；配置类失败（invalid_grant 等）仅重试 1 次。
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
				break // 配置类错误多试无益
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
		return "", te.err // 剥掉包装，上抛原始错误文案
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
	var tokenURL string
	switch c.mode {
	case modeApp:
		tokenURL = c.loginBase + "/" + url.PathEscape(c.tenantID) + "/oauth2/v2.0/token"
		form.Set("grant_type", "client_credentials")
		form.Set("client_id", c.clientID)
		form.Set("client_secret", c.clientSecret)
		// scope = graph 主机 + /.default
		gu, err := url.Parse(c.graphBase)
		if err != nil {
			return err
		}
		form.Set("scope", gu.Scheme+"://"+gu.Host+"/.default")
	default:
		tokenURL = c.loginBase + "/common/oauth2/v2.0/token"
		form.Set("grant_type", "refresh_token")
		form.Set("client_id", c.clientID)
		if c.clientSecret != "" {
			form.Set("client_secret", c.clientSecret)
		}
		form.Set("refresh_token", c.refreshToken)
	}

	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return &transientTokenError{err} // 网络层失败（EOF/超时/连接重置）
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &transientTokenError{err}
	}
	var tr tokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		perr := fmt.Errorf("OneDrive：token 响应解析失败(HTTP %d)", resp.StatusCode)
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
		ferr := fmt.Errorf("OneDrive：%s", aadMessage(msg))
		if resp.StatusCode >= 500 {
			return &transientTokenError{ferr}
		}
		return ferr
	}
	c.accessToken = tr.AccessToken
	c.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	// refresh_token 轮换：持久化新值，重启后仍可用
	if tr.RefreshToken != "" && tr.RefreshToken != c.refreshToken {
		c.refreshToken = tr.RefreshToken
		if c.cfg != nil {
			c.cfg["refresh_token"] = tr.RefreshToken
			if c.persist != nil {
				if err := c.persist(c.cfg); err != nil {
					log.Printf("[onedrive] refresh_token 回写失败（不影响本次运行）: %v", err)
				}
			}
		}
	}
	return nil
}

// graphError 是 Graph API 的标准错误体。
type graphError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func mapGraphError(status int, code, message string) error {
	switch {
	case code == "itemNotFound" || status == 404:
		return driver.ErrNotFound
	case code == "nameAlreadyExists":
		return driver.ErrExist
	case code == "invalidRequest" && status == 400:
		return driver.ErrBadName
	case code == "accessDenied" || status == 403:
		// 授权范围不含写权限（如只授了 Files.Read）或该盘对此账号只读 → 写操作被拒。
		return driver.ErrDenied
	case code == "quotaLimitReached" || code == "insufficientStorage" || status == 507:
		// OneDrive 配额用尽（读不占额度所以读正常、写全挂）。
		return driver.ErrQuota
	}
	// 其余未归类的 API 错误：包裹 ErrUpstream 把原始信息透传到界面，不再兜底成不透明 500。
	return fmt.Errorf("%w：%s(HTTP %d) %s", driver.ErrUpstream, code, status, message)
}

// req 发送带鉴权的 Graph 请求并解析 JSON 到 out（可为 nil）。
// 401 强制刷新 token 重试 1 次；429 按 util.ThrottleWait 退避重试 maxThrottleRetries 次。
// body 为 JSON 可序列化对象或 nil。
func (c *client) req(ctx context.Context, method, u string, body any, out any) error {
	retried401, rlAttempts := false, 0
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

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			if out != nil && len(data) > 0 {
				return json.Unmarshal(data, out)
			}
			// 202 等场景由调用方通过 outHeader 变体处理；此处成功即返回
			return nil
		case resp.StatusCode == 401 && !retried401:
			retried401 = true
			c.forceRefresh()
			continue
		case resp.StatusCode == 429 && rlAttempts < maxThrottleRetries:
			rlAttempts++
			select {
			case <-time.After(util.ThrottleWait(rlAttempts, resp.Header.Get("Retry-After"))):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		var ge graphError
		json.Unmarshal(data, &ge)
		return mapGraphError(resp.StatusCode, ge.Error.Code, ge.Error.Message)
	}
}

// reqHeader 同 req，但额外返回响应头（Copy 需要 Location 监控 URL）。
// body 每轮重新序列化，撞 429 时可以安全退避重发。
func (c *client) reqHeader(ctx context.Context, method, u string, body any) (http.Header, error) {
	for attempt := 1; ; attempt++ {
		var br io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			br = strings.NewReader(string(b))
		}
		req, err := http.NewRequestWithContext(ctx, method, u, br)
		if err != nil {
			return nil, err
		}
		tok, err := c.token(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp.Header, nil
		}
		if resp.StatusCode == 429 && attempt <= maxThrottleRetries {
			select {
			case <-time.After(util.ThrottleWait(attempt, resp.Header.Get("Retry-After"))):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		var ge graphError
		json.Unmarshal(data, &ge)
		return nil, mapGraphError(resp.StatusCode, ge.Error.Code, ge.Error.Message)
	}
}

// itemURL 构造 path-based addressing 的条目 URL。
// rel 为相对驱动 root_folder_path 的 POSIX 路径（"" = 根）；
// suffix 形如 ""、"/children"、"/content"、"/createUploadSession"、"/thumbnails/0/large"、"/copy"。
func (c *client) itemURL(root, rel, suffix string) string {
	full := joinRel(root, rel)
	if full == "" {
		return c.driveBase + "/root" + suffix
	}
	segs := strings.Split(full, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return c.driveBase + "/root:/" + strings.Join(segs, "/") + ":" + suffix
}

// drivePath 构造 parentReference.path 值（Move 用）：/drive/root:/A/B，根为 /drive/root:
func drivePath(root, rel string) string {
	full := joinRel(root, rel)
	if full == "" {
		return "/drive/root:"
	}
	return "/drive/root:/" + full
}

// joinRel 拼接存储根目录与相对路径，返回不带首尾斜杠的 POSIX 路径（"" = drive 根）。
func joinRel(root, rel string) string {
	root = strings.Trim(root, "/")
	rel = strings.Trim(rel, "/")
	switch {
	case root == "":
		return rel
	case rel == "":
		return root
	default:
		return root + "/" + rel
	}
}

// aadMessage 把 Azure AD 的 token 错误压成一句人话。
// AAD 的 error_description 会把 Trace ID / Correlation ID / Timestamp 塞在同一行，
// 对用户毫无意义——常见错误码直接翻成「哪里填错了、该去哪改」，未知码保留首句
// （含 AADSTS 码，便于按码检索），只丢掉诊断噪音。
func aadMessage(msg string) string {
	code := ""
	if i := strings.Index(msg, "AADSTS"); i >= 0 {
		rest := msg[i:]
		if end := strings.IndexAny(rest, ": \n"); end > 0 {
			code = rest[:end]
		} else {
			code = rest
		}
	}
	switch code {
	case "AADSTS7000215", "AADSTS7000222":
		return "client_secret 无效或已过期，请在 Azure 门户重新生成后填入"
	case "AADSTS700016", "AADSTS500011":
		return "client_id 对应的应用不存在，请检查 client_id"
	case "AADSTS90002":
		return "tenant_id 不存在，请检查租户 ID"
	case "AADSTS70008", "AADSTS700082", "AADSTS50173":
		return "登录授权已过期，请重新获取 refresh_token"
	case "AADSTS65001":
		return "该应用尚未获得授权，请重新完成一次授权"
	case "AADSTS9002313":
		return "凭据格式有误，请检查 client_id / client_secret / refresh_token"
	}
	// 未知码：截掉 Trace ID 起的诊断噪音，保留可读首句
	for _, cut := range []string{"Trace ID:", "Correlation ID:", "Timestamp:", "\n"} {
		if i := strings.Index(msg, cut); i > 0 {
			msg = msg[:i]
		}
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "获取访问令牌失败，请检查存储配置"
	}
	return "获取访问令牌失败：" + msg
}
