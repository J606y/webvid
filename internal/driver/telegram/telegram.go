// Package telegram 把本人 Telegram 收藏夹（Saved Messages）挂载为只读存储：
// 每条含文件的消息 = 一个文件，命名 <消息ID>_<文件名>，平铺无目录。
// 典型用法：TG 里把视频转发到收藏夹 → 文件页复制到网盘存储 = 离线下载。
package telegram

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"newlist/internal/driver"
	"newlist/internal/model"
)

func init() {
	driver.Register(driver.Meta{
		Name: "telegram", Label: "Telegram 收藏夹", Remote: false, // 无直链，恒走本地代理流
		Fields: []driver.FieldSpec{
			{Name: "api_id", Label: "API ID", Type: "number", Required: true,
				Help: "在 my.telegram.org → API development tools 申请"},
			{Name: "api_hash", Label: "API Hash", Type: "password", Secret: true, Required: true},
			{Name: "phone", Label: "手机号", Type: "string", Required: true,
				Help: "国际格式，如 +8613800138000"},
			{Name: "socks5", Label: "SOCKS5 代理", Type: "string",
				Help: "host:port 或 user:pass@host:port，服务器无法直连 Telegram 时必填"},
			{Name: "session", Label: "会话", Type: "password", Secret: true,
				Help: "登录成功后自动写入，请勿手动修改"},
		},
	}, func() driver.Driver { return &Telegram{} })
}

// ErrNotLoggedIn 挂载时会话缺失/失效，提示去存储管理走验证码登录（文案会挂在状态标签上，保持简短）。
var ErrNotLoggedIn = errors.New("未登录：请点击钥匙按钮登录")

// Telegram 收藏夹只读驱动。
type Telegram struct {
	persist  func(driver.Config) error
	conn     *conn
	cacheTTL time.Duration
	now      func() time.Time

	mu     sync.Mutex
	list   []model.FileInfo
	listAt time.Time
}

func (d *Telegram) SetPersist(fn func(driver.Config) error) { d.persist = fn }

func (d *Telegram) Init(ctx context.Context, cfg driver.Config) error {
	if d.now == nil {
		d.now = time.Now
	}
	if d.cacheTTL == 0 {
		d.cacheTTL = 2 * time.Minute
	}
	if strings.TrimSpace(cfg["phone"]) == "" {
		return errors.New("telegram: 手机号必填")
	}
	store := &sessionStore{save: func(data []byte) {
		cfg["session"] = base64.StdEncoding.EncodeToString(data)
		if d.persist != nil {
			_ = d.persist(cfg)
		}
	}}
	c, err := connect(cfg, store)
	if err != nil {
		return err
	}
	st, err := c.client.Auth().Status(ctx)
	if err != nil {
		c.close()
		return fmt.Errorf("telegram: 获取登录状态失败: %w", err)
	}
	if !st.Authorized {
		c.close()
		return ErrNotLoggedIn
	}
	d.conn = c
	return nil
}

func (d *Telegram) Drop() error {
	if d.conn != nil {
		d.conn.close()
		d.conn = nil
	}
	return nil
}

func (d *Telegram) List(ctx context.Context, relPath string) ([]model.FileInfo, error) {
	if relPath != "" {
		return nil, driver.ErrNotFound
	}
	d.mu.Lock()
	if d.list != nil && d.now().Sub(d.listAt) < d.cacheTTL {
		out := append([]model.FileInfo(nil), d.list...)
		d.mu.Unlock()
		return out, nil
	}
	d.mu.Unlock()

	items, err := listSaved(ctx, d.conn.client.API())
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.list, d.listAt = items, d.now()
	d.mu.Unlock()
	return append([]model.FileInfo(nil), items...), nil
}

func (d *Telegram) Stat(ctx context.Context, relPath string) (model.FileInfo, error) {
	if relPath == "" {
		return model.FileInfo{Name: "/", IsDir: true, Modified: d.now()}, nil
	}
	id, err := parseMsgID(relPath)
	if err != nil {
		return model.FileInfo{}, err
	}
	msg, doc, err := d.message(ctx, id)
	if err != nil {
		return model.FileInfo{}, err
	}
	return model.FileInfo{
		Name: entryName(msg.ID, doc), Size: doc.Size,
		Modified: time.Unix(int64(msg.Date), 0),
	}, nil
}

func (d *Telegram) Link(ctx context.Context, relPath string) (*driver.Link, error) {
	id, err := parseMsgID(relPath)
	if err != nil {
		return nil, err
	}
	msg, doc, err := d.message(ctx, id)
	if err != nil {
		return nil, err
	}
	src := &docSource{
		loc:     doc.AsInputDocumentFileLocation(""),
		dc:      doc.DCID,
		getFile: d.getFile,
		refresh: func(rctx context.Context) (*tg.InputDocumentFileLocation, int, error) {
			_, nd, err := d.message(rctx, id)
			if err != nil {
				return nil, 0, err
			}
			return nd.AsInputDocumentFileLocation(""), nd.DCID, nil
		},
	}
	return &driver.Link{
		Local: newReader(ctx, src.fetch, doc.Size),
		Size:  doc.Size,
		Mod:   time.Unix(int64(msg.Date), 0),
	}, nil
}

// getFile 对指定 DC 调 upload.getFile 拉一块（不带 cdn_supported，服务器不会 CDN 重定向）。
func (d *Telegram) getFile(ctx context.Context, dc int, loc *tg.InputDocumentFileLocation, offset int64) ([]byte, error) {
	inv, err := d.conn.invoker(ctx, dc)
	if err != nil {
		return nil, err
	}
	r, err := tg.NewClient(inv).UploadGetFile(ctx, &tg.UploadGetFileRequest{
		Location: loc, Offset: offset, Limit: chunkSize,
	})
	if err != nil {
		return nil, err
	}
	f, ok := r.(*tg.UploadFile)
	if !ok {
		return nil, fmt.Errorf("telegram: 未预期的下载响应 %T", r)
	}
	return f.Bytes, nil
}

// message 按消息 ID 取单条并提取文件（比列表缓存新鲜，file_reference 也最新）。
func (d *Telegram) message(ctx context.Context, id int) (*tg.Message, *tg.Document, error) {
	r, err := d.conn.client.API().MessagesGetMessages(ctx,
		[]tg.InputMessageClass{&tg.InputMessageID{ID: id}})
	if err != nil {
		return nil, nil, fmt.Errorf("telegram: 读取消息失败: %w", err)
	}
	msgs, err := messagesOf(r)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range msgs {
		msg, ok := m.(*tg.Message)
		if !ok || msg.ID != id {
			continue
		}
		if doc := docOf(msg); doc != nil {
			return msg, doc, nil
		}
	}
	return nil, nil, driver.ErrNotFound
}

// ---- 收藏夹列表 ----

// history List 依赖的最小 API 面（单测注入假实现）。
type history interface {
	MessagesGetHistory(ctx context.Context, request *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error)
}

const pageSize = 100

// listSaved 分页拉全收藏夹的文件消息（服务端按新→旧返回）。
// 必须全量翻历史在客户端挑文件，不能用 messages.search 的 InputMessagesFilterDocument：
// 该 filter 只命中「以文件发送」的消息（客户端"文件"页签），普通转发的视频/音乐/GIF
// 虽底层同为 Document 却不被命中，收藏夹全是转发视频时列表会整个为空。
func listSaved(ctx context.Context, api history) ([]model.FileInfo, error) {
	var out []model.FileInfo
	offsetID := 0
	for {
		r, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     &tg.InputPeerSelf{},
			OffsetID: offsetID,
			Limit:    pageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("telegram: 拉取收藏列表失败: %w", err)
		}
		msgs, err := messagesOf(r)
		if err != nil {
			return nil, err
		}
		if len(msgs) == 0 {
			return out, nil
		}
		last := offsetID
		for _, m := range msgs {
			last = m.GetID() // 服务消息也推进偏移，防整页非文件时误判尾页
			msg, ok := m.(*tg.Message)
			if !ok {
				continue
			}
			if doc := docOf(msg); doc != nil {
				out = append(out, model.FileInfo{
					Name: entryName(msg.ID, doc), Size: doc.Size,
					Modified: time.Unix(int64(msg.Date), 0),
				})
			}
		}
		if len(msgs) < pageSize || last == offsetID || (offsetID != 0 && last > offsetID) {
			return out, nil // 尾页 / 偏移未推进（防御死循环）
		}
		offsetID = last
	}
}

func messagesOf(r tg.MessagesMessagesClass) ([]tg.MessageClass, error) {
	switch v := r.(type) {
	case *tg.MessagesMessages:
		return v.Messages, nil
	case *tg.MessagesMessagesSlice:
		return v.Messages, nil
	case *tg.MessagesChannelMessages:
		return v.Messages, nil
	default:
		return nil, fmt.Errorf("telegram: 未预期的消息响应 %T", r)
	}
}

func docOf(msg *tg.Message) *tg.Document {
	media, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok {
		return nil
	}
	dc, ok := media.GetDocument()
	if !ok {
		return nil
	}
	doc, ok := dc.(*tg.Document)
	if !ok {
		return nil
	}
	for _, a := range doc.Attributes {
		if _, ok := a.(*tg.DocumentAttributeSticker); ok {
			return nil // 贴纸底层也是 Document，但不是网盘意义上的文件
		}
	}
	return doc
}

// ---- 消息 ↔ 文件名映射 ----

// entryName 生成条目名：<消息ID>_<清洗后的原文件名>；无文件名按 mime 兜底。
func entryName(msgID int, doc *tg.Document) string {
	name := ""
	for _, a := range doc.Attributes {
		if fn, ok := a.(*tg.DocumentAttributeFilename); ok {
			name = fn.FileName
			break
		}
	}
	name = sanitizeName(name)
	if name == "" {
		name = "file_" + time.Unix(int64(doc.Date), 0).Format("20060102") + extByMime(doc.MimeType)
	}
	return strconv.Itoa(msgID) + "_" + name
}

// parseMsgID 从 "<msgID>_<name>" 反解析消息 ID。
func parseMsgID(rel string) (int, error) {
	if strings.Contains(rel, "/") {
		return 0, driver.ErrNotFound // 平铺存储无子目录
	}
	idStr, _, ok := strings.Cut(rel, "_")
	if !ok {
		return 0, driver.ErrNotFound
	}
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		return 0, driver.ErrNotFound
	}
	return id, nil
}

// sanitizeName 清洗文件名中的路径分隔符与控制字符，去掉首尾点/空格。
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), ". ")
}

// extByMime 常见类型的扩展名兜底（Windows 上 mime 包查注册表不可靠，用显式表）。
var mimeExt = map[string]string{
	"video/mp4": ".mp4", "video/x-matroska": ".mkv", "video/quicktime": ".mov",
	"video/webm": ".webm", "video/mpeg": ".mpg", "video/mp2t": ".ts", "video/3gpp": ".3gp",
	"audio/mpeg": ".mp3", "audio/mp4": ".m4a", "audio/flac": ".flac", "audio/ogg": ".ogg",
	"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp",
	"application/pdf": ".pdf", "application/zip": ".zip",
}

func extByMime(m string) string {
	if e, ok := mimeExt[m]; ok {
		return e
	}
	return ".bin"
}
