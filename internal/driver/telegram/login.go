package telegram

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
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

// CodeSent 告诉用户验证码实际发到了哪。Telegram 对第三方 api_id 基本不发短信：
// 账号在别处登录过就走 App 内服务消息（官方账号「Telegram」），坐等短信的用户
// 会以为“收不到验证码”。
type CodeSent struct {
	SentTo  string `json:"sent_to"`           // 人话：码发到哪了
	Resend  string `json:"resend,omitempty"`  // 重发可切换的通道（空=服务端没给备选）
	Timeout int    `json:"timeout,omitempty"` // 约多少秒后才允许重发切换
}

// sentDesc 验证码实际投递通道 → 给用户看的人话。
func sentDesc(t tg.AuthSentCodeTypeClass) string {
	switch t.(type) {
	case *tg.AuthSentCodeTypeApp:
		return "验证码已发到已登录的 Telegram 客户端：打开手机/电脑上的 Telegram，看官方账号「Telegram」的服务消息（不是短信）"
	case *tg.AuthSentCodeTypeSMS, *tg.AuthSentCodeTypeSMSWord, *tg.AuthSentCodeTypeSMSPhrase:
		return "验证码已通过短信发送"
	case *tg.AuthSentCodeTypeCall:
		return "Telegram 将来电语音播报验证码，请接听"
	case *tg.AuthSentCodeTypeFlashCall, *tg.AuthSentCodeTypeMissedCall:
		return "Telegram 将用未接来电发码：来电号码的末几位数字就是验证码"
	case *tg.AuthSentCodeTypeEmailCode:
		return "验证码已发到该账号绑定的登录邮箱"
	case *tg.AuthSentCodeTypeFragmentSMS:
		return "该号码是 Fragment 匿名号，验证码发到了 fragment.com"
	case *tg.AuthSentCodeTypeFirebaseSMS:
		return "Telegram 选择了仅官方 App 支持的推送验证，本应用收不到，请点「重新发送」换通道"
	default:
		return fmt.Sprintf("验证码已发送（通道 %T）", t)
	}
}

// nextDesc 重发（resendCode）会切换到的通道名。
func nextDesc(t tg.AuthCodeTypeClass) string {
	switch t.(type) {
	case *tg.AuthCodeTypeSMS:
		return "短信"
	case *tg.AuthCodeTypeCall:
		return "语音电话"
	case *tg.AuthCodeTypeFlashCall, *tg.AuthCodeTypeMissedCall:
		return "未接来电（号码末几位为验证码）"
	case *tg.AuthCodeTypeFragmentSMS:
		return "Fragment"
	default:
		return "其他方式"
	}
}

func describeSent(code *tg.AuthSentCode) *CodeSent {
	out := &CodeSent{SentTo: sentDesc(code.Type), Timeout: code.Timeout}
	if code.NextType != nil {
		out.Resend = nextDesc(code.NextType)
	} else if _, app := code.Type.(*tg.AuthSentCodeTypeApp); app {
		// 无备选通道（第三方 api_id 常态）：收不到只能从投递侧排查，把方向给足
		out.SentTo += "；若几分钟仍未收到：确认查看的是该手机号对应的账号（多账号勿看错）且该号仍登录在某台设备上；短时间反复请求会被 Telegram 静默处理，隔几小时再试成功率更高"
	}
	return out
}

// loginTTL 登录会话保活时长：验证码经 App 通道投递偶有数分钟延迟，给足取码时间。
const loginTTL = 10 * time.Minute

// take 取走 id 的登录会话（从表中摘除，由调用方决定放回或停掉）；
// 过期或手机号已改则就地回收，返回 nil。
func (m *LoginManager) take(id int64, phone string) *pendingLogin {
	m.mu.Lock()
	p := m.pending[id]
	if p != nil {
		delete(m.pending, id)
	}
	m.mu.Unlock()
	if p == nil {
		return nil
	}
	if time.Now().After(p.expires) || p.phone != phone {
		_ = p.stop()
		return nil
	}
	return p
}

// SendCode 请求发送验证码，返回验证码实际投递到了哪。
//
// 已有未过期的登录会话（用户点「重新发送」）时，在同一连接上走 auth.resendCode
// 切换投递通道（App→短信→电话）——重复全新 sendCode 只会按原通道再发一遍，
// 等短信的用户照样收不到。
func (m *LoginManager) SendCode(ctx context.Context, id int64, cfg driver.Config) (*CodeSent, error) {
	phone := strings.TrimSpace(cfg["phone"])
	if phone == "" {
		return nil, errors.New("telegram: 存储配置里手机号为空")
	}
	if p := m.take(id, phone); p != nil {
		sent, err := p.client.API().AuthResendCode(ctx, &tg.AuthResendCodeRequest{
			PhoneNumber: phone, PhoneCodeHash: p.codeHash,
		})
		if err == nil {
			if code, ok := sent.(*tg.AuthSentCode); ok {
				m.mu.Lock()
				p.codeHash = code.PhoneCodeHash
				p.expires = time.Now().Add(loginTTL)
				m.pending[id] = p
				m.mu.Unlock()
				log.Printf("telegram: resend_code 已切换通道 -> %T (next %v)", code.Type, code.NextType)
				return describeSent(code), nil
			}
		}
		// 没有备选通道 / 码已失效等 → 拆掉旧会话，落回全新 sendCode
		log.Printf("telegram: resend_code 未成功（%v），改走全新 send_code", err)
		_ = p.stop()
	}
	store := &sessionStore{}
	client, err := newClient(cfg, store)
	if err != nil {
		return nil, err
	}
	stop, err := bg.Connect(client, bg.WithStartupTimeout(loginStartupTimeout))
	if err != nil {
		return nil, fmt.Errorf("telegram: 连接失败（检查网络或 SOCKS5 代理）: %w", err)
	}
	sent, err := client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		_ = stop()
		return nil, fmt.Errorf("telegram: 发送验证码失败: %w", err)
	}
	code, ok := sent.(*tg.AuthSentCode)
	if !ok {
		_ = stop()
		return nil, fmt.Errorf("telegram: 未预期的发码响应 %T", sent)
	}
	if _, bad := code.Type.(*tg.AuthSentCodeTypeSetUpEmailRequired); bad {
		_ = stop()
		return nil, errors.New("telegram: Telegram 要求该账号先在官方客户端登录并设置登录邮箱，之后才能在第三方应用登录")
	}
	log.Printf("telegram: send_code 已发出 -> %T (next %v, timeout %ds)", code.Type, code.NextType, code.Timeout)
	m.mu.Lock()
	if old := m.pending[id]; old != nil {
		_ = old.stop()
	}
	m.pending[id] = &pendingLogin{
		client: client, stop: stop, store: store,
		phone: phone, codeHash: code.PhoneCodeHash,
		expires: time.Now().Add(loginTTL),
	}
	m.mu.Unlock()
	return describeSent(code), nil
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
