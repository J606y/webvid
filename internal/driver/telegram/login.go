package telegram

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/contrib/bg"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"

	"newlist/internal/driver"
)

// pendingLogin 一次进行中的验证码登录：send_code 与 sign_in 两个 HTTP 请求
// 之间必须保持同一连接（phone_code_hash 绑定该会话）。
type pendingLogin struct {
	client   *telegram.Client
	stop     bg.StopFunc
	store    *sessionStore
	phone    string
	codeHash string
	expires  time.Time
}

// LoginManager 管理各存储的登录会话；Logins 为进程级单例。
type LoginManager struct {
	mu      sync.Mutex
	pending map[int64]*pendingLogin
}

// Logins 供 server 层调用（自用单进程，单例足够）。
var Logins = &LoginManager{pending: map[int64]*pendingLogin{}}

// SendCode 用存储配置建临时连接并请求发送验证码。
func (m *LoginManager) SendCode(ctx context.Context, id int64, cfg driver.Config) error {
	phone := strings.TrimSpace(cfg["phone"])
	if phone == "" {
		return errors.New("telegram: 存储配置里手机号为空")
	}
	store := &sessionStore{}
	client, err := newClient(cfg, store)
	if err != nil {
		return err
	}
	stop, err := bg.Connect(client, bg.WithStartupTimeout(startupTimeout))
	if err != nil {
		return fmt.Errorf("telegram: 连接失败（检查网络或 SOCKS5 代理）: %w", err)
	}
	sent, err := client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		_ = stop()
		return fmt.Errorf("telegram: 发送验证码失败: %w", err)
	}
	code, ok := sent.(*tg.AuthSentCode)
	if !ok {
		_ = stop()
		return fmt.Errorf("telegram: 未预期的发码响应 %T", sent)
	}
	m.mu.Lock()
	if old := m.pending[id]; old != nil {
		_ = old.stop()
	}
	m.pending[id] = &pendingLogin{
		client: client, stop: stop, store: store,
		phone: phone, codeHash: code.PhoneCodeHash,
		expires: time.Now().Add(5 * time.Minute),
	}
	m.mu.Unlock()
	return nil
}

// SignIn 提交验证码与可选两步密码。
// needPassword=true 表示账号开了两步验证、需要补交密码（不算失败，会话保留）。
// 成功返回 base64 会话串，调用方负责写回存储配置。
func (m *LoginManager) SignIn(ctx context.Context, id int64, code, password string) (sessionB64 string, needPassword bool, err error) {
	m.mu.Lock()
	p := m.pending[id]
	if p != nil && time.Now().After(p.expires) { // 过期即回收后台连接
		delete(m.pending, id)
		_ = p.stop()
		p = nil
	}
	m.mu.Unlock()
	if p == nil {
		return "", false, errors.New("telegram: 登录会话不存在或已过期，请重新发送验证码")
	}
	_, err = p.client.Auth().SignIn(ctx, p.phone, code, p.codeHash)
	if errors.Is(err, auth.ErrPasswordAuthNeeded) {
		if password == "" {
			return "", true, nil
		}
		_, err = p.client.Auth().Password(ctx, password)
	}
	if err != nil {
		return "", false, fmt.Errorf("telegram: 登录失败: %w", err)
	}
	// 登录成功即授权绑定到本连接的 auth key，gotd 已把会话写进 store
	data := p.store.bytes()
	if len(data) == 0 {
		return "", false, errors.New("telegram: 登录成功但未取到会话数据")
	}
	m.mu.Lock()
	delete(m.pending, id)
	m.mu.Unlock()
	_ = p.stop()
	return base64.StdEncoding.EncodeToString(data), false, nil
}
