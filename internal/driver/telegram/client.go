package telegram

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/contrib/bg"
	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/transport"
	"golang.org/x/net/proxy"

	"newlist/internal/driver"
)

// sessionStore 把 gotd 会话桥接到存储配置：Load 读 cfg["session"]（base64），
// Store 更新内存副本并触发 save 回调（驱动运行期经 ConfigPersister 落库，
// 登录流程只留内存、成功后一次性导出）。
type sessionStore struct {
	mu   sync.Mutex
	data []byte
	save func(data []byte)
}

func (s *sessionStore) LoadSession(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data) == 0 {
		return nil, session.ErrNotFound
	}
	return append([]byte(nil), s.data...), nil
}

func (s *sessionStore) StoreSession(_ context.Context, data []byte) error {
	s.mu.Lock()
	s.data = append([]byte(nil), data...)
	save := s.save
	s.mu.Unlock()
	if save != nil {
		save(append([]byte(nil), data...))
	}
	return nil
}

func (s *sessionStore) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...)
}

// newClient 按存储配置构建 gotd 客户端（不发起连接）。
func newClient(cfg driver.Config, store *sessionStore) (*telegram.Client, error) {
	apiID, err := strconv.Atoi(strings.TrimSpace(cfg["api_id"]))
	if err != nil || apiID <= 0 {
		return nil, fmt.Errorf("telegram: api_id 必须是正整数（my.telegram.org 申请）")
	}
	apiHash := strings.TrimSpace(cfg["api_hash"])
	if apiHash == "" {
		return nil, fmt.Errorf("telegram: api_hash 必填")
	}
	if b, err := base64.StdEncoding.DecodeString(cfg["session"]); err == nil && len(b) > 0 {
		store.data = b
	}
	opts := telegram.Options{
		SessionStorage: store,
		Middlewares:    []telegram.Middleware{floodwait.NewSimpleWaiter()},
		DialTimeout:    10 * time.Second,
		// 设备指纹伪装成 Telegram Desktop：gotd 默认 device_model=runtime.Version()
		//（形如 "go1.26"）+ system="windows"，第三方 api_id 配脚本指纹是 Telegram
		// 风控静默不投验证码的已知诱因（send_code 成功但官方对话永远等不来消息）。
		Device: telegram.DeviceTDesktopWindows(),
	}
	if addr := strings.TrimSpace(cfg["socks5"]); addr != "" {
		dial, err := socksDial(addr)
		if err != nil {
			return nil, err
		}
		// 代理路径也用与 TDesktopResolver 一致的传输层（混淆 + abridged）
		opts.Resolver = dcs.Plain(dcs.PlainOptions{Dial: dial, Protocol: transport.Abridged, Obfuscated: true})
	} else {
		opts.Resolver = telegram.TDesktopResolver()
	}
	return telegram.NewClient(apiID, apiHash, opts), nil
}

// socksDial 解析 [user:pass@]host:port 为 SOCKS5 拨号函数。
func socksDial(addr string) (dcs.DialFunc, error) {
	addr = strings.TrimPrefix(strings.TrimPrefix(addr, "socks5://"), "socks://")
	var auth *proxy.Auth
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		u, p, _ := strings.Cut(addr[:i], ":")
		auth = &proxy.Auth{User: u, Password: p}
		addr = addr[i+1:]
	}
	d, err := proxy.SOCKS5("tcp", addr, auth, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("telegram: SOCKS5 代理地址无效: %w", err)
	}
	if cd, ok := d.(proxy.ContextDialer); ok {
		return cd.DialContext, nil
	}
	return func(_ context.Context, network, address string) (net.Conn, error) {
		return d.Dial(network, address)
	}, nil
}

// startupTimeout 后台连接就绪等待上限；须小于 fs.Reload 给 Init 的 30s 预算。
const startupTimeout = 15 * time.Second

// loginStartupTimeout 验证码登录（send_code）专用的握手等待上限。登录不走 fs.Reload
// 的 30s 预算约束，给足 60s：某些出口 IP 到 Telegram DC 的 MTProto 握手较慢，15s 偏紧。
const loginStartupTimeout = 60 * time.Second

// connect 构建客户端并后台连接（bg.Connect 常驻，close 时停止）。
func connect(cfg driver.Config, store *sessionStore) (*conn, error) {
	client, err := newClient(cfg, store)
	if err != nil {
		return nil, err
	}
	stop, err := bg.Connect(client, bg.WithStartupTimeout(startupTimeout))
	if err != nil {
		return nil, fmt.Errorf("telegram: 连接失败（检查网络或 SOCKS5 代理）: %w", err)
	}
	return &conn{
		client: client,
		stop:   stop,
		wait:   floodwait.NewSimpleWaiter(),
		pools:  map[int]dcPool{},
	}, nil
}

// dcPool 一条异地 DC 连接：inv 已包 floodwait，closer 用于释放。
type dcPool struct {
	inv    tg.Invoker
	closer telegram.CloseInvoker
}

// conn 一条就绪的 gotd 连接：主 DC 走 client 本体（自带中间件），
// 异地 DC（文件不在登录 DC 时）按需建池并缓存。
type conn struct {
	client *telegram.Client
	stop   bg.StopFunc
	wait   *floodwait.SimpleWaiter

	mu    sync.Mutex
	pools map[int]dcPool
}

// invoker 返回面向 dc 的调用器；dc<=0 或等于当前 DC 时用主连接。
func (c *conn) invoker(ctx context.Context, dc int) (tg.Invoker, error) {
	if dc <= 0 || dc == c.client.Config().ThisDC {
		return c.client, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.pools[dc]; ok {
		return p.inv, nil
	}
	// 池独立于请求生命周期，用后台 ctx 建（请求取消不应拆掉共享池）
	pctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	closer, err := c.client.DC(pctx, dc, 2)
	if err != nil {
		return nil, fmt.Errorf("telegram: 连接 DC%d 失败: %w", dc, err)
	}
	// client.DC 不经过客户端中间件，FLOOD_WAIT 自动等待需手动包一层
	p := dcPool{inv: c.wait.Handle(closer), closer: closer}
	c.pools[dc] = p
	return p.inv, nil
}

func (c *conn) close() {
	c.mu.Lock()
	for _, p := range c.pools {
		_ = p.closer.Close()
	}
	c.pools = map[int]dcPool{}
	c.mu.Unlock()
	if c.stop != nil {
		_ = c.stop()
	}
}
