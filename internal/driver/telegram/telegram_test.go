package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"newlist/internal/driver"
)

// ---- 文件名映射 ----

func docWith(name, mime string, date int64) *tg.Document {
	d := &tg.Document{MimeType: mime, Date: int(date), Size: 42}
	if name != "" {
		d.Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: name}}
	}
	return d
}

func TestEntryName(t *testing.T) {
	cases := []struct {
		msgID int
		doc   *tg.Document
		want  string
	}{
		{5, docWith("电影.mkv", "video/x-matroska", 0), "5_电影.mkv"},
		{7, docWith("a/b\\c\x01.mp4", "video/mp4", 0), "7_a_b_c_.mp4"},
		{9, docWith("  .hidden  ", "", 0), "9_hidden"},
		{11, docWith("", "video/mp4", time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC).Unix()), "11_file_20260709.mp4"},
		{13, docWith("", "application/x-unknown", time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC).Unix()), "13_file_20260709.bin"},
	}
	for _, c := range cases {
		if got := entryName(c.msgID, c.doc); got != c.want {
			t.Errorf("entryName(%d) = %q, want %q", c.msgID, got, c.want)
		}
	}
}

func TestParseMsgID(t *testing.T) {
	if id, err := parseMsgID("123_movie.mp4"); err != nil || id != 123 {
		t.Fatalf("got (%d,%v), want (123,nil)", id, err)
	}
	// 单层对话文件夹前缀也能反解析出末段 msgID（文件解析不依赖文件夹）
	if id, err := parseMsgID("电影频道/123_movie.mp4"); err != nil || id != 123 {
		t.Fatalf("folder got (%d,%v), want (123,nil)", id, err)
	}
	for _, bad := range []string{"", "abc", "abc_x.mp4", "-3_x", "0_x", "12"} {
		if _, err := parseMsgID(bad); !errors.Is(err, driver.ErrNotFound) {
			t.Errorf("parseMsgID(%q) err = %v, want ErrNotFound", bad, err)
		}
	}
	// 名字回环：entryName 生成的名字必能反解析回消息 ID
	name := entryName(456, docWith("视频_带下划线.mp4", "video/mp4", 0))
	if id, err := parseMsgID(name); err != nil || id != 456 {
		t.Fatalf("roundtrip %q got (%d,%v)", name, id, err)
	}
}

// ---- reader：Seek 对齐 / 跨块读 / EOF ----

// fakeFetch 造一份内容为 0..size-1 循环字节的假文件，记录每次取块的 offset。
func fakeFetch(size int64, calls *[]int64) fetchFunc {
	return func(_ context.Context, off int64) ([]byte, error) {
		if calls != nil {
			*calls = append(*calls, off)
		}
		if off%chunkSize != 0 {
			return nil, fmt.Errorf("offset %d 未对齐", off)
		}
		if off >= size {
			return nil, nil
		}
		n := chunkSize
		if off+int64(n) > size {
			n = int(size - off)
		}
		b := make([]byte, n)
		for i := range b {
			b[i] = byte((off + int64(i)) % 251)
		}
		return b, nil
	}
}

func TestReaderSequential(t *testing.T) {
	size := int64(chunkSize + chunkSize/2) // 1.5 块
	var calls []int64
	r := newReader(context.Background(), fakeFetch(size, &calls), size)
	got, err := io.ReadAll(r)
	if err != nil || int64(len(got)) != size {
		t.Fatalf("ReadAll: len=%d err=%v", len(got), err)
	}
	for i, b := range got {
		if b != byte(i%251) {
			t.Fatalf("字节 %d 不符", i)
		}
	}
	if len(calls) != 2 || calls[0] != 0 || calls[1] != chunkSize {
		t.Fatalf("取块序列 %v，want [0 %d]", calls, chunkSize)
	}
}

func TestReaderSeekMidChunk(t *testing.T) {
	size := int64(3 * chunkSize)
	var calls []int64
	r := newReader(context.Background(), fakeFetch(size, &calls), size)

	// ServeContent 探大小的套路：SeekEnd 不触发网络
	if n, err := r.Seek(0, io.SeekEnd); err != nil || n != size {
		t.Fatalf("SeekEnd got (%d,%v)", n, err)
	}
	if len(calls) != 0 {
		t.Fatalf("SeekEnd 不应取块，calls=%v", calls)
	}

	// 跳到第二块中间读 8 字节：只取一次块，且 offset 对齐
	pos := int64(chunkSize + 100)
	if _, err := r.Seek(pos, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	if buf[0] != byte(pos%251) {
		t.Fatalf("seek 后首字节不符")
	}
	if len(calls) != 1 || calls[0] != chunkSize {
		t.Fatalf("calls=%v, want [%d]", calls, chunkSize)
	}

	// 同块内继续读不再取块
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("同块复读不应再取块，calls=%v", calls)
	}
}

func TestReaderEOFAndCancel(t *testing.T) {
	size := int64(100)
	r := newReader(context.Background(), fakeFetch(size, nil), size)
	if _, err := r.Seek(size, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("越界读 err=%v, want EOF", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rc := newReader(ctx, fakeFetch(size, nil), size)
	if _, err := rc.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后读 err=%v", err)
	}
}

// ---- docSource：FILE_MIGRATE 换 DC / file_reference 过期刷新 ----

func TestSourceFileMigrate(t *testing.T) {
	var dcSeen []int
	s := &docSource{
		loc: &tg.InputDocumentFileLocation{ID: 1},
		dc:  0,
		getFile: func(_ context.Context, dc int, _ *tg.InputDocumentFileLocation, _ int64) ([]byte, error) {
			dcSeen = append(dcSeen, dc)
			if dc != 5 {
				return nil, tgerr.New(303, "FILE_MIGRATE_5")
			}
			return []byte("ok"), nil
		},
		refresh: func(context.Context) (*tg.InputDocumentFileLocation, int, error) {
			t.Fatal("FILE_MIGRATE 不应触发 refresh")
			return nil, 0, nil
		},
	}
	b, err := s.fetch(context.Background(), 0)
	if err != nil || string(b) != "ok" {
		t.Fatalf("fetch got (%q,%v)", b, err)
	}
	if len(dcSeen) != 2 || dcSeen[1] != 5 {
		t.Fatalf("dc 序列 %v，want [0 5]", dcSeen)
	}
	// 迁移结果被记住：下一块直接打 DC5
	if _, err := s.fetch(context.Background(), chunkSize); err != nil {
		t.Fatal(err)
	}
	if dcSeen[len(dcSeen)-1] != 5 {
		t.Fatalf("迁移后仍打 dc%d", dcSeen[len(dcSeen)-1])
	}
}

func TestSourceFileReferenceExpired(t *testing.T) {
	refreshed := 0
	s := &docSource{
		loc: &tg.InputDocumentFileLocation{ID: 1, FileReference: []byte("old")},
		getFile: func(_ context.Context, _ int, loc *tg.InputDocumentFileLocation, _ int64) ([]byte, error) {
			if string(loc.FileReference) == "old" {
				return nil, tgerr.New(400, "FILE_REFERENCE_EXPIRED")
			}
			return []byte("data"), nil
		},
		refresh: func(context.Context) (*tg.InputDocumentFileLocation, int, error) {
			refreshed++
			return &tg.InputDocumentFileLocation{ID: 1, FileReference: []byte("new")}, 4, nil
		},
	}
	b, err := s.fetch(context.Background(), 0)
	if err != nil || string(b) != "data" {
		t.Fatalf("fetch got (%q,%v)", b, err)
	}
	if refreshed != 1 {
		t.Fatalf("refresh 次数 %d", refreshed)
	}
	if s.dc != 4 {
		t.Fatalf("刷新后 dc=%d，want 4", s.dc)
	}
}

func TestSourceOtherErrorPassthrough(t *testing.T) {
	boom := errors.New("boom")
	s := &docSource{
		loc:     &tg.InputDocumentFileLocation{},
		getFile: func(context.Context, int, *tg.InputDocumentFileLocation, int64) ([]byte, error) { return nil, boom },
		refresh: func(context.Context) (*tg.InputDocumentFileLocation, int, error) { return nil, 0, nil },
	}
	if _, err := s.fetch(context.Background(), 0); !errors.Is(err, boom) {
		t.Fatalf("err=%v, want boom", err)
	}
}

// ---- 列表分页 ----

type fakeHistory struct {
	pages [][]tg.MessageClass
	users [][]tg.UserClass // 与 pages 平行，可为 nil（响应里的 Users 实体）
	chats [][]tg.ChatClass // 与 pages 平行，可为 nil（响应里的 Chats 实体）
	reqs  []int            // 每次请求的 OffsetID
}

func (f *fakeHistory) MessagesGetHistory(_ context.Context, req *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error) {
	f.reqs = append(f.reqs, req.OffsetID)
	i := len(f.reqs) - 1
	if i >= len(f.pages) {
		return &tg.MessagesMessagesSlice{}, nil
	}
	s := &tg.MessagesMessagesSlice{Messages: f.pages[i]}
	if i < len(f.users) {
		s.Users = f.users[i]
	}
	if i < len(f.chats) {
		s.Chats = f.chats[i]
	}
	return s, nil
}

// msgFwd 造一条从指定 peer 转发进收藏夹的文件消息（带 SavedFromPeer）。
func msgFwd(id int, name string, peer tg.PeerClass) *tg.Message {
	m := msgDoc(id, name)
	var fwd tg.MessageFwdHeader
	fwd.SetSavedFromPeer(peer)
	m.SetFwdFrom(fwd)
	return m
}

// msgFwdName 造一条隐藏转发（只给 FromName 字符串、无 peer）的文件消息。
func msgFwdName(id int, name, fromName string) *tg.Message {
	m := msgDoc(id, name)
	m.SetFwdFrom(tg.MessageFwdHeader{FromName: fromName})
	return m
}

func msgMedia(id int, doc *tg.Document) *tg.Message {
	media := &tg.MessageMediaDocument{}
	media.SetDocument(doc) // 置可选字段标记位
	return &tg.Message{ID: id, Date: 1700000000, Media: media}
}

func msgDoc(id int, name string) *tg.Message {
	return msgMedia(id, docWith(name, "video/mp4", 0))
}

// msgVideo 普通「以视频发送/转发」的消息：无文件名属性，仅 DocumentAttributeVideo。
func msgVideo(id int, date int64) *tg.Message {
	d := &tg.Document{MimeType: "video/mp4", Date: int(date), Size: 42,
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{}}}
	return msgMedia(id, d)
}

func TestListSaved(t *testing.T) {
	// 造两页：首页 pageSize 条（触发翻页），第二页 1 条
	var page1 []tg.MessageClass
	for i := 0; i < pageSize; i++ {
		page1 = append(page1, msgDoc(1000-i, fmt.Sprintf("v%d.mp4", i)))
	}
	f := &fakeHistory{pages: [][]tg.MessageClass{page1, {msgDoc(3, "last.mp4")}}}
	tr, err := listSaved(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	items := tr.root // 无转发头 → 全部平铺在根，无对话文件夹
	if len(items) != pageSize+1 {
		t.Fatalf("条数 %d, want %d", len(items), pageSize+1)
	}
	if len(tr.sub) != 0 {
		t.Fatalf("不应产生对话文件夹，sub=%v", tr.sub)
	}
	if items[0].Name != "1000_v0.mp4" || items[len(items)-1].Name != "3_last.mp4" {
		t.Fatalf("首尾 %q / %q", items[0].Name, items[len(items)-1].Name)
	}
	if len(f.reqs) != 2 || f.reqs[1] != 1000-pageSize+1 {
		t.Fatalf("翻页请求 %v", f.reqs)
	}
}

func TestListSavedSkipsNonDocument(t *testing.T) {
	sticker := &tg.Document{MimeType: "image/webp", Date: 1, Size: 1,
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{}}}
	f := &fakeHistory{pages: [][]tg.MessageClass{{
		msgDoc(10, "a.mp4"),
		msgVideo(9, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC).Unix()), // 「以视频发送」也必须列出（#37 回归）
		&tg.Message{ID: 8, Date: 1},                                 // 无媒体（纯文本）
		&tg.Message{ID: 7, Date: 1, Media: &tg.MessageMediaPhoto{}}, // 照片（非 Document）
		msgMedia(6, sticker),                                        // 贴纸
		&tg.MessageService{ID: 5},                                   // 服务消息
	}}}
	tr, err := listSaved(context.Background(), f)
	if err != nil || len(tr.root) != 2 {
		t.Fatalf("got %v, %v", tr.root, err)
	}
	if tr.root[0].Name != "10_a.mp4" || tr.root[1].Name != "9_file_20260710.mp4" {
		t.Fatalf("条目 %q / %q", tr.root[0].Name, tr.root[1].Name)
	}
}

// ---- 按来源对话分组 ----

func TestListSavedGroupsByChat(t *testing.T) {
	chA := &tg.PeerChannel{ChannelID: 555}
	usrB := &tg.PeerUser{UserID: 111}
	page := []tg.MessageClass{
		msgFwd(30, "a.mp4", chA),
		msgFwd(29, "b.mp4", chA),
		msgFwd(28, "c.mp4", usrB),
		msgDoc(27, "loose.mp4"), // 非转发 → 根散文件
	}
	f := &fakeHistory{
		pages: [][]tg.MessageClass{page},
		chats: [][]tg.ChatClass{{&tg.Channel{ID: 555, Title: "电影频道"}}},
		users: [][]tg.UserClass{{&tg.User{ID: 111, FirstName: "小明"}}},
	}
	tr, err := listSaved(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.sub) != 2 {
		t.Fatalf("文件夹数 %d, want 2: %v", len(tr.sub), tr.sub)
	}
	if got := len(tr.sub["电影频道"]); got != 2 {
		t.Fatalf("电影频道 文件 %d, want 2", got)
	}
	if got := len(tr.sub["小明"]); got != 1 {
		t.Fatalf("小明 文件 %d, want 1", got)
	}
	var dirs, files int
	for _, it := range tr.root {
		if it.IsDir {
			dirs++
		} else {
			files++
		}
	}
	if dirs != 2 || files != 1 {
		t.Fatalf("root dirs=%d files=%d, want 2/1", dirs, files)
	}
	// 目录排在散文件前，散文件在末尾
	if tr.root[len(tr.root)-1].Name != "27_loose.mp4" {
		t.Fatalf("根散文件应在末尾，got %q", tr.root[len(tr.root)-1].Name)
	}
}

func TestListSavedFromName(t *testing.T) {
	f := &fakeHistory{pages: [][]tg.MessageClass{{
		msgFwdName(10, "x.mp4", "神秘转发者"),
	}}}
	tr, err := listSaved(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.sub["神秘转发者"]) != 1 {
		t.Fatalf("隐藏转发应按 FromName 建文件夹，sub=%v", tr.sub)
	}
}

// 有 peer 但响应无对应实体：落按 id 造的通用名文件夹，不混入根目录。
func TestListSavedUnknownPeerGeneric(t *testing.T) {
	f := &fakeHistory{pages: [][]tg.MessageClass{{
		msgFwd(9, "y.mp4", &tg.PeerChannel{ChannelID: 777}),
	}}} // 无 chats 实体
	tr, err := listSaved(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.sub["频道777"]) != 1 {
		t.Fatalf("未解析 peer 应落通用名文件夹，sub=%v", tr.sub)
	}
	if len(tr.root) != 1 || !tr.root[0].IsDir {
		t.Fatalf("root 应只有 1 个目录，got %v", tr.root)
	}
}

// 同名不同对话：分成两个文件夹消歧，不合并。
func TestListSavedNameCollision(t *testing.T) {
	f := &fakeHistory{
		pages: [][]tg.MessageClass{{
			msgFwd(5, "a.mp4", &tg.PeerChannel{ChannelID: 1}),
			msgFwd(4, "b.mp4", &tg.PeerChannel{ChannelID: 2}),
		}},
		chats: [][]tg.ChatClass{{
			&tg.Channel{ID: 1, Title: "频道"},
			&tg.Channel{ID: 2, Title: "频道"},
		}},
	}
	tr, err := listSaved(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.sub) != 2 {
		t.Fatalf("撞名应分成两个文件夹，got %d: %v", len(tr.sub), tr.sub)
	}
	for name, files := range tr.sub {
		if len(files) != 1 {
			t.Fatalf("文件夹 %q 有 %d 文件，应为 1（未合并）", name, len(files))
		}
	}
}
