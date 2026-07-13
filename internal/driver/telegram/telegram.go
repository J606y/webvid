// Package telegram 把本人 Telegram 收藏夹（Saved Messages）挂载为只读存储：
// 每条含文件的消息 = 一个文件，命名 <消息ID>_<文件名>。
// 转发进收藏夹的消息按来源对话分文件夹（<对话名>/<消息ID>_<文件名>），
// 非转发的直接上传文件留在根目录。
// 典型用法：TG 里把视频转发到收藏夹 → 文件页复制到网盘存储 = 离线下载。
package telegram

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"sort"
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
	tree   *savedTree
	treeAt time.Time
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
	t, err := d.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if relPath == "" {
		return append([]model.FileInfo(nil), t.root...), nil
	}
	files, ok := t.sub[relPath]
	if !ok {
		return nil, driver.ErrNotFound
	}
	return append([]model.FileInfo(nil), files...), nil
}

// snapshot 返回收藏夹目录树（缓存有效直接返，过期时重建）。
// 树一旦建成即只读，可安全共享指针；返回给调用方的切片各自再拷贝。
func (d *Telegram) snapshot(ctx context.Context) (*savedTree, error) {
	d.mu.Lock()
	if d.tree != nil && d.now().Sub(d.treeAt) < d.cacheTTL {
		t := d.tree
		d.mu.Unlock()
		return t, nil
	}
	d.mu.Unlock()

	t, err := listSaved(ctx, d.conn.client.API())
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.tree, d.treeAt = t, d.now()
	d.mu.Unlock()
	return t, nil
}

func (d *Telegram) Stat(ctx context.Context, relPath string) (model.FileInfo, error) {
	if relPath == "" {
		return model.FileInfo{Name: "/", IsDir: true, Modified: d.now()}, nil
	}
	// 已知的对话文件夹（单层名，无 "/"）→ 目录。
	if !strings.Contains(relPath, "/") {
		if t, err := d.snapshot(ctx); err == nil {
			if _, ok := t.sub[relPath]; ok {
				return model.FileInfo{Name: relPath, IsDir: true, Modified: d.now()}, nil
			}
		}
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

// ---- 收藏夹列表（按来源对话分组）----

// history List 依赖的最小 API 面（单测注入假实现）。
type history interface {
	MessagesGetHistory(ctx context.Context, request *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error)
}

const pageSize = 100

// savedTree 是收藏夹的两层目录快照（构建后只读）。
type savedTree struct {
	root []model.FileInfo            // List("") 返回：各对话文件夹（目录在前）+ 根散文件
	sub  map[string][]model.FileInfo // 文件夹名 -> 该对话下的文件
}

// folderRef 一条文件消息归属的对话：key 为稳定去重键（""=根），name 为显示名。
type folderRef struct {
	key  string
	name string
}

type fileEntry struct {
	ref  folderRef
	info model.FileInfo
}

// listSaved 分页拉全收藏夹的文件消息（服务端按新→旧返回），按来源对话分组成树。
// 必须全量翻历史在客户端挑文件，不能用 messages.search 的 InputMessagesFilterDocument：
// 该 filter 只命中「以文件发送」的消息（客户端"文件"页签），普通转发的视频/音乐/GIF
// 虽底层同为 Document 却不被命中，收藏夹全是转发视频时列表会整个为空。
func listSaved(ctx context.Context, api history) (*savedTree, error) {
	nt := nameTable{}
	var entries []fileEntry
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
		msgs, users, chats, err := pageOf(r)
		if err != nil {
			return nil, err
		}
		nt.add(users, chats) // 本页实体先入表，供本页消息的转发头解析对话名
		if len(msgs) == 0 {
			break
		}
		last := offsetID
		for _, m := range msgs {
			last = m.GetID() // 服务消息也推进偏移，防整页非文件时误判尾页
			msg, ok := m.(*tg.Message)
			if !ok {
				continue
			}
			doc := docOf(msg)
			if doc == nil {
				continue
			}
			entries = append(entries, fileEntry{
				ref: fwdFolder(msg, nt),
				info: model.FileInfo{
					Name: entryName(msg.ID, doc), Size: doc.Size,
					Modified: time.Unix(int64(msg.Date), 0),
				},
			})
		}
		if len(msgs) < pageSize || last == offsetID || (offsetID != 0 && last > offsetID) {
			break // 尾页 / 偏移未推进（防御死循环）
		}
		offsetID = last
	}
	return buildTree(entries), nil
}

// buildTree 把文件条目按对话分组成两层树；同名不同对话追加序号消歧（不合并）。
func buildTree(entries []fileEntry) *savedTree {
	t := &savedTree{sub: map[string][]model.FileInfo{}}
	folderName := map[string]string{} // 去重键 -> 分配的唯一文件夹名
	usedBy := map[string]string{}     // 文件夹名 -> 占用它的去重键
	var order []string                // 文件夹名首次出现顺序

	assign := func(ref folderRef) string {
		if name, ok := folderName[ref.key]; ok {
			return name
		}
		base := sanitizeName(ref.name)
		if base == "" {
			base = "对话"
		}
		name := base
		for n := 2; ; n++ { // 撞名不同对话：base、base_2、base_3… 直到未被占用
			owner, taken := usedBy[name]
			if !taken || owner == ref.key {
				break
			}
			name = base + "_" + strconv.Itoa(n)
		}
		folderName[ref.key] = name
		usedBy[name] = ref.key
		order = append(order, name)
		return name
	}

	for _, e := range entries {
		if e.ref.key == "" {
			t.root = append(t.root, e.info) // 非转发 → 根散文件
			continue
		}
		name := assign(e.ref)
		t.sub[name] = append(t.sub[name], e.info)
	}

	// 目录条目排在 root 前部；目录修改时间取该对话下最新文件时间。
	dirs := make([]model.FileInfo, 0, len(order))
	for _, name := range order {
		var mod time.Time
		for _, f := range t.sub[name] {
			if f.Modified.After(mod) {
				mod = f.Modified
			}
		}
		dirs = append(dirs, model.FileInfo{Name: name, IsDir: true, Modified: mod})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	t.root = append(dirs, t.root...)
	return t
}

// nameTable 累积 peerKey -> 显示名（实体来自各页响应的 Users/Chats）。
type nameTable map[string]string

func (nt nameTable) add(users []tg.UserClass, chats []tg.ChatClass) {
	for _, u := range users {
		if uu, ok := u.(*tg.User); ok {
			if disp := userDisplay(uu); disp != "" {
				nt["u"+strconv.FormatInt(uu.ID, 10)] = disp
			}
		}
	}
	for _, c := range chats {
		switch cc := c.(type) {
		case *tg.Chat:
			if cc.Title != "" {
				nt["c"+strconv.FormatInt(cc.ID, 10)] = cc.Title
			}
		case *tg.Channel:
			if cc.Title != "" {
				nt["ch"+strconv.FormatInt(cc.ID, 10)] = cc.Title
			}
		}
	}
}

// fwdFolder 从转发头解析来源对话；无转发信息返回零值 folderRef（归根目录）。
// 依次尝试：SavedFromPeer/FromID 的已解析真实名 → FromName 字符串 → 按 peer id 造通用名。
func fwdFolder(msg *tg.Message, nt nameTable) folderRef {
	fwd, ok := msg.GetFwdFrom()
	if !ok {
		return folderRef{}
	}
	var peers []tg.PeerClass
	if p, ok := fwd.GetSavedFromPeer(); ok { // 消息原本所在的对话（最贴合「对话」）
		peers = append(peers, p)
	}
	if p, ok := fwd.GetFromID(); ok { // 退回原始发送者
		peers = append(peers, p)
	}
	for _, p := range peers {
		if k := peerKey(p); k != "" {
			if n := nt[k]; n != "" {
				return folderRef{key: k, name: n}
			}
		}
	}
	if s := strings.TrimSpace(fwd.FromName); s != "" { // 隐藏转发只给字符串名
		return folderRef{key: "name:" + s, name: s}
	}
	for _, p := range peers { // 有 peer 但无实体名：稳定通用名，避免不同对话混入根目录
		if k := peerKey(p); k != "" {
			return folderRef{key: k, name: genericPeerName(p)}
		}
	}
	return folderRef{}
}

// peerKey 给 peer 一个带类型前缀的稳定键（跨类型 id 可能撞号，前缀区分）。
func peerKey(p tg.PeerClass) string {
	switch pp := p.(type) {
	case *tg.PeerUser:
		return "u" + strconv.FormatInt(pp.UserID, 10)
	case *tg.PeerChat:
		return "c" + strconv.FormatInt(pp.ChatID, 10)
	case *tg.PeerChannel:
		return "ch" + strconv.FormatInt(pp.ChannelID, 10)
	}
	return ""
}

// genericPeerName 实体名缺失时按 peer id 造稳定通用名。
func genericPeerName(p tg.PeerClass) string {
	switch pp := p.(type) {
	case *tg.PeerUser:
		return "用户" + strconv.FormatInt(pp.UserID, 10)
	case *tg.PeerChat:
		return "群组" + strconv.FormatInt(pp.ChatID, 10)
	case *tg.PeerChannel:
		return "频道" + strconv.FormatInt(pp.ChannelID, 10)
	}
	return ""
}

// userDisplay 用户显示名：姓名 → 用户名 → 空（交由 genericPeerName 兜底）。
func userDisplay(u *tg.User) string {
	name := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
	if name != "" {
		return name
	}
	return strings.TrimSpace(u.Username)
}

// pageOf 从消息响应取出消息与实体（三种响应类型都带 Users/Chats）。
func pageOf(r tg.MessagesMessagesClass) ([]tg.MessageClass, []tg.UserClass, []tg.ChatClass, error) {
	switch v := r.(type) {
	case *tg.MessagesMessages:
		return v.Messages, v.Users, v.Chats, nil
	case *tg.MessagesMessagesSlice:
		return v.Messages, v.Users, v.Chats, nil
	case *tg.MessagesChannelMessages:
		return v.Messages, v.Users, v.Chats, nil
	default:
		return nil, nil, nil, fmt.Errorf("telegram: 未预期的消息响应 %T", r)
	}
}

func messagesOf(r tg.MessagesMessagesClass) ([]tg.MessageClass, error) {
	msgs, _, _, err := pageOf(r)
	return msgs, err
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

// parseMsgID 从 "<对话>/<msgID>_<name>"（或根下 "<msgID>_<name>"）反解析消息 ID。
// 文件解析只认末段的 msgID，与所在文件夹无关——取块不依赖文件夹名。
func parseMsgID(rel string) (int, error) {
	idStr, _, ok := strings.Cut(path.Base(rel), "_")
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
