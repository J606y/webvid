package googledrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"newlist/internal/driver"
	"newlist/internal/model"
	"newlist/internal/util"
)

func init() {
	driver.Register(driver.Meta{
		Name: "googledrive", Label: "Google Drive", Remote: true,
		Fields: []driver.FieldSpec{
			{Name: "client_id", Label: "客户端 ID", Type: "string", Required: true,
				Help: "Google Cloud OAuth 客户端 ID（类型选『Web 应用』）"},
			{Name: "client_secret", Label: "客户端密钥", Type: "password", Required: true, Secret: true,
				Help: "OAuth 客户端密钥"},
			{Name: "refresh_token", Label: "刷新令牌", Type: "password", Secret: true,
				Help: "由后台「授权」按钮自动获取回填；也可手工粘贴。留空则该存储未就绪"},
			{Name: "root_folder_id", Label: "根目录 ID", Type: "string",
				Help: "留空=我的云端硬盘根（root）；填 Google Drive 文件夹 ID 可只挂载该子目录"},
		},
	}, func() driver.Driver { return &GDrive{} })
}

// 上传常量：<5MiB 走 multipart 单请求；否则 resumable 分块，块须为 256KiB 整数倍。
const (
	simpleUploadLimit = 5 << 20
	defaultChunkSize  = 16 << 20 // 16MiB = 64×256KiB
	folderMime        = "application/vnd.google-apps.folder"
)

// 路径缓存条数上限。全量索引会 BFS 整个挂载，每列一次目录就把全部子项写进缓存——
// 不设上限等于把云盘上每一条路径都留在内存里。负条目（已确认不存在）单独限额，
// 免得把有用的正条目挤光。按每条约 300 字节算，两项合计不到 8MB。
const (
	pathCacheEntries = 20000
	missCacheEntries = 5000
)

// GDrive 是 Google Drive 驱动（ID 寻址，内部做路径→ID 解析与 TTL 缓存）。
type GDrive struct {
	cli      *client
	root     string // 解析后的根目录文件 ID（root_folder_id 为空时=「我的云端硬盘」根）
	persist  func(driver.Config) error
	now      func() time.Time
	cacheTTL time.Duration
	chunk    int64

	cache *util.TTLCache[gdFile] // rel 路径 → 解析结果（TTL + LRU 上限，自带锁）

	mu     sync.Mutex
	flight map[string]*lookupWait // rel 路径 → 正在跑的解析，同路径只跑一趟
}

// lookupWait 是一次在途的路径解析：后到的调用等它出结果，不重复翻页。
type lookupWait struct {
	done chan struct{}
	f    gdFile
	err  error
}

// gdFile 是 Drive 文件条目的最小投影。
type gdFile struct {
	id       string
	name     string
	size     int64
	isDir    bool
	native   bool // Google 原生文档（无二进制内容，不能直接下载）
	modified time.Time
}

// rawFile 对应 Drive API 返回的文件 JSON（size 为字符串）。
type rawFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	Size         string `json:"size"`
	ModifiedTime string `json:"modifiedTime"`
}

func (r *rawFile) toFile() gdFile {
	size, _ := strconv.ParseInt(r.Size, 10, 64)
	mod, _ := time.Parse(time.RFC3339, r.ModifiedTime)
	isDir := r.MimeType == folderMime
	return gdFile{
		id:       r.ID,
		name:     r.Name,
		size:     size,
		isDir:    isDir,
		native:   !isDir && strings.HasPrefix(r.MimeType, "application/vnd.google-apps."),
		modified: mod,
	}
}

func (f gdFile) fileInfo() model.FileInfo {
	return model.FileInfo{Name: f.name, Size: f.size, IsDir: f.isDir, Modified: f.modified}
}

func (d *GDrive) SetPersist(fn func(driver.Config) error) { d.persist = fn }

func (d *GDrive) Init(ctx context.Context, cfg driver.Config) error {
	if d.now == nil {
		d.now = time.Now
	}
	if d.cacheTTL == 0 {
		// 路径缓存过期后，解析一条路径要把每一级父目录重列一遍（listAll 按 1000 条
		// 顺序翻页）。2 分钟太短：隔一会儿再点一次视频，就得先把整个父目录翻完才能
		// 开始拉流。本进程内的写操作都会 cacheClear 兜底，外部改动最多迟这么久看到。
		d.cacheTTL = 15 * time.Minute
	}
	if d.chunk == 0 {
		d.chunk = defaultChunkSize
	}
	d.cache = util.NewTTLCache[gdFile](d.cacheTTL, pathCacheEntries, missCacheEntries)
	d.cache.SetNow(d.now)
	d.flight = map[string]*lookupWait{}

	clientID := strings.TrimSpace(cfg["client_id"])
	clientSecret := strings.TrimSpace(cfg["client_secret"])
	refreshToken := strings.TrimSpace(cfg["refresh_token"])
	if clientID == "" || clientSecret == "" {
		return errors.New("Google Drive：client_id 与 client_secret 必填")
	}
	if refreshToken == "" {
		return errors.New("Google Drive：尚未授权，请在后台点「授权」完成 Google 登录")
	}
	d.cli = &client{
		clientID:     clientID,
		clientSecret: clientSecret,
		refreshToken: refreshToken,
		persist:      d.persist,
		cfg:          cfg,
	}

	// 解析根目录 ID（"" → "root" 别名 → 具体文件 ID），顺带验证 token 与访问权限。
	rootID := strings.TrimSpace(cfg["root_folder_id"])
	if rootID == "" {
		rootID = "root"
	}
	var meta rawFile
	if err := d.cli.req(ctx, http.MethodGet, driveAPIBase+"/files/"+url.PathEscape(rootID),
		url.Values{"fields": {"id,mimeType"}, "supportsAllDrives": {"true"}}, nil, &meta); err != nil {
		return err
	}
	d.root = meta.ID
	return nil
}

func (d *GDrive) Drop() error { return nil }

// listAll 列出某文件夹 ID 下所有条目（翻页合并，排除回收站）。
func (d *GDrive) listAll(ctx context.Context, parentID string) ([]gdFile, error) {
	var out []gdFile
	pageToken := ""
	for {
		q := url.Values{}
		q.Set("q", "'"+parentID+"' in parents and trashed=false")
		q.Set("fields", "nextPageToken,files(id,name,mimeType,size,modifiedTime)")
		q.Set("pageSize", "1000")
		q.Set("orderBy", "folder,name")
		q.Set("supportsAllDrives", "true")
		q.Set("includeItemsFromAllDrives", "true")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var resp struct {
			NextPageToken string    `json:"nextPageToken"`
			Files         []rawFile `json:"files"`
		}
		if err := d.cli.req(ctx, http.MethodGet, driveAPIBase+"/files", q, nil, &resp); err != nil {
			return nil, err
		}
		for i := range resp.Files {
			out = append(out, resp.Files[i].toFile())
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return out, nil
}

// lookup 把相对路径解析为 gdFile；"" = 根目录。递归解析父级 + TTL 缓存（镜像 pikpak）。
//
// 同一路径的并发解析只跑一趟：一屏封面、预载 worker 与播放请求会在同一瞬间打到同一个
// 目录上，各解析各的就是 N 份完整翻页。后到的等在途那次的结果。
func (d *GDrive) lookup(ctx context.Context, rel string) (gdFile, error) {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return gdFile{id: d.root, isDir: true, name: ""}, nil
	}
	for {
		if f, missing, ok := d.cache.Get(rel); ok {
			if missing {
				return gdFile{}, driver.ErrNotFound
			}
			return f, nil
		}
		w, lead := d.beginLookup(rel)
		if lead {
			f, err := d.resolve(ctx, rel)
			d.endLookup(rel, w, f, err)
			return f, err
		}
		select {
		case <-w.done:
		case <-ctx.Done():
			return gdFile{}, ctx.Err()
		}
		if !errors.Is(w.err, context.Canceled) {
			return w.f, w.err
		}
		// 领跑者是被它自己的 ctx 掐掉的，与本次调用无关（本次的 ctx 还活着，
		// 上面的 select 已经验过），重来一趟：要么这次命中缓存，要么自己领跑。
	}
}

// beginLookup 登记一次解析，lead=true 表示由本次调用负责真去解析。
func (d *GDrive) beginLookup(rel string) (*lookupWait, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.flight == nil { // 测试可能不经 Init 直接构造
		d.flight = map[string]*lookupWait{}
	}
	if w, ok := d.flight[rel]; ok {
		return w, false
	}
	w := &lookupWait{done: make(chan struct{})}
	d.flight[rel] = w
	return w, true
}

// endLookup 交出结果并唤醒等待者。等待者只在 done 关闭之后读 f/err，故此处赋值安全。
func (d *GDrive) endLookup(rel string, w *lookupWait, f gdFile, err error) {
	d.mu.Lock()
	delete(d.flight, rel)
	d.mu.Unlock()
	w.f, w.err = f, err
	close(w.done)
}

// resolve 真去解析一次：递归解析父级 → 列出父目录 → 顺带缓存全部同级项。
func (d *GDrive) resolve(ctx context.Context, rel string) (gdFile, error) {
	parentRel := path.Dir(rel)
	if parentRel == "." {
		parentRel = ""
	}
	parent, err := d.lookup(ctx, parentRel)
	if err != nil {
		return gdFile{}, err
	}
	if !parent.isDir {
		return gdFile{}, driver.ErrNotFound
	}
	children, err := d.listAll(ctx, parent.id)
	if err != nil {
		return gdFile{}, err
	}
	base := path.Base(rel)
	var match *gdFile
	for i := range children {
		d.cachePut(util.JoinRel(parentRel, children[i].name), children[i])
		if children[i].name == base {
			match = &children[i]
		}
	}
	if match != nil {
		return *match, nil
	}
	// 记住「这个路径不存在」。只缓存找得到的路径时，每查一次不存在的路径都要把父目录
	// 整个重列一遍——上传前的探测、播放前的 Stat 都在这条路径上，目标目录一大就是
	// 成千上万次列举。
	// 安全性：所有让路径由无变有的写操作（Put/MakeDir/Rename/Move/Copy）成功后都会
	// cacheClear，负缓存不会盖住刚建好的文件；外部改动则和正缓存一样受 TTL 约束。
	d.cache.PutMissing(rel)
	return gdFile{}, driver.ErrNotFound
}

func (d *GDrive) cachePut(rel string, f gdFile) { d.cache.Put(rel, f) }

func (d *GDrive) cacheClear() { d.cache.Clear() }

func (d *GDrive) List(ctx context.Context, relPath string) ([]model.FileInfo, error) {
	f, err := d.lookup(ctx, relPath)
	if err != nil {
		return nil, err
	}
	if !f.isDir {
		return nil, driver.ErrNotFound
	}
	children, err := d.listAll(ctx, f.id)
	if err != nil {
		return nil, err
	}
	rel := strings.Trim(relPath, "/")
	out := make([]model.FileInfo, 0, len(children))
	for i := range children {
		d.cachePut(util.JoinRel(rel, children[i].name), children[i])
		out = append(out, children[i].fileInfo())
	}
	return out, nil
}

func (d *GDrive) Stat(ctx context.Context, relPath string) (model.FileInfo, error) {
	f, err := d.lookup(ctx, relPath)
	if err != nil {
		return model.FileInfo{}, err
	}
	if strings.Trim(relPath, "/") == "" {
		f.isDir = true
	}
	return f.fileInfo(), nil
}

func (d *GDrive) Link(ctx context.Context, relPath string) (*driver.Link, error) {
	f, err := d.lookup(ctx, relPath)
	if err != nil {
		return nil, err
	}
	if f.isDir || f.native {
		return nil, driver.ErrNotSupported // 目录 / Google 原生文档无直接二进制
	}
	tok, err := d.cli.token(ctx)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok)
	// alt=media 需带 Bearer 头 → 上层被迫走代理中转（见 handler_raw：带 Header 的直链不 302）。
	// acknowledgeAbuse：Google 把某些文件（多为体积大、传播广的压缩包/媒体）标记为可疑，
	// 不带这个参数就直接 403 拒绝下载。这里挂载的是用户自己的云盘，风险由本人承担。
	u := driveAPIBase + "/files/" + url.PathEscape(f.id) +
		"?alt=media&supportsAllDrives=true&acknowledgeAbuse=true"
	return &driver.Link{URL: u, Header: h, Size: f.size, Mod: f.modified}, nil
}

// RefreshLink 拉流 401/403 时强制换链：直链 URL 稳定，过期的是 Bearer——刷新 token 重建。
func (d *GDrive) RefreshLink(ctx context.Context, relPath string) (*driver.Link, error) {
	d.cli.forceRefresh()
	return d.Link(ctx, relPath)
}

// checkName 校验新建/改名的名称。
func checkName(name string) error {
	if name == "" || name == "." || name == ".." {
		return driver.ErrBadName
	}
	if strings.ContainsAny(name, `/\`) {
		return driver.ErrBadName
	}
	for _, r := range name {
		if r < 0x20 {
			return driver.ErrBadName
		}
	}
	if len(name) > 255 {
		return driver.ErrBadName
	}
	return nil
}

func (d *GDrive) MakeDir(ctx context.Context, relPath string) error {
	rel := strings.Trim(relPath, "/")
	if rel == "" {
		return driver.ErrExist
	}
	segs := strings.Split(rel, "/")
	for _, s := range segs {
		if err := checkName(s); err != nil {
			return err
		}
	}
	// 从根往下逐级：已存在则沿用其 id，缺失则创建。
	parentID := d.root
	for i, seg := range segs {
		children, err := d.listAll(ctx, parentID)
		if err != nil {
			return err
		}
		var found *gdFile
		for j := range children {
			if children[j].name == seg {
				found = &children[j]
				break
			}
		}
		if found != nil {
			if i == len(segs)-1 {
				return driver.ErrExist
			}
			if !found.isDir {
				return driver.ErrBadName
			}
			parentID = found.id
			continue
		}
		id, err := d.createFolder(ctx, parentID, seg)
		if err != nil {
			return err
		}
		parentID = id
	}
	d.cacheClear()
	return nil
}

// findChild 精确查询父目录下指定名字的直接子项（Put 覆盖判定用，避免列全目录）。
func (d *GDrive) findChild(ctx context.Context, parentID, name string) (gdFile, bool, error) {
	esc := strings.ReplaceAll(name, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `'`, `\'`)
	q := url.Values{}
	q.Set("q", "name='"+esc+"' and '"+parentID+"' in parents and trashed=false")
	q.Set("fields", "files(id,name,mimeType,size,modifiedTime)")
	q.Set("pageSize", "10")
	q.Set("supportsAllDrives", "true")
	q.Set("includeItemsFromAllDrives", "true")
	var resp struct {
		Files []rawFile `json:"files"`
	}
	if err := d.cli.req(ctx, http.MethodGet, driveAPIBase+"/files", q, nil, &resp); err != nil {
		return gdFile{}, false, err
	}
	for i := range resp.Files {
		if f := resp.Files[i].toFile(); f.name == name {
			return f, true, nil
		}
	}
	return gdFile{}, false, nil
}

func (d *GDrive) createFolder(ctx context.Context, parentID, name string) (string, error) {
	body := map[string]any{"name": name, "mimeType": folderMime, "parents": []string{parentID}}
	q := url.Values{"fields": {"id"}, "supportsAllDrives": {"true"}}
	var resp rawFile
	if err := d.cli.req(ctx, http.MethodPost, driveAPIBase+"/files", q, body, &resp); err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (d *GDrive) Rename(ctx context.Context, relPath, newName string) error {
	if err := checkName(newName); err != nil {
		return err
	}
	f, err := d.lookup(ctx, relPath)
	if err != nil {
		return err
	}
	q := url.Values{"fields": {"id"}, "supportsAllDrives": {"true"}}
	if err := d.cli.req(ctx, http.MethodPatch, driveAPIBase+"/files/"+url.PathEscape(f.id),
		q, map[string]any{"name": newName}, nil); err != nil {
		return err
	}
	d.cacheClear()
	return nil
}

func (d *GDrive) Remove(ctx context.Context, relPath string) error {
	if strings.Trim(relPath, "/") == "" {
		return driver.ErrNotSupported
	}
	f, err := d.lookup(ctx, relPath)
	if err != nil {
		return err
	}
	// 移入回收站（可恢复），同 pikpak 语义。
	q := url.Values{"fields": {"id"}, "supportsAllDrives": {"true"}}
	if err := d.cli.req(ctx, http.MethodPatch, driveAPIBase+"/files/"+url.PathEscape(f.id),
		q, map[string]any{"trashed": true}, nil); err != nil {
		return err
	}
	d.cacheClear()
	return nil
}

func (d *GDrive) Move(ctx context.Context, srcRel, dstDirRel string) error {
	src, err := d.lookup(ctx, srcRel)
	if err != nil {
		return err
	}
	dstDir, err := d.lookup(ctx, dstDirRel)
	if err != nil {
		return err
	}
	if !dstDir.isDir {
		return driver.ErrNotFound
	}
	// 同名预检（Drive 允许重名，需自行拒绝以贴合语义）。
	if _, err := d.lookup(ctx, util.JoinRel(strings.Trim(dstDirRel, "/"), src.name)); err == nil {
		return driver.ErrExist
	}
	// 取当前父目录 ID 用于 removeParents。
	parentRel := path.Dir(strings.Trim(srcRel, "/"))
	if parentRel == "." {
		parentRel = ""
	}
	srcParent, err := d.lookup(ctx, parentRel)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("addParents", dstDir.id)
	q.Set("removeParents", srcParent.id)
	q.Set("fields", "id")
	q.Set("supportsAllDrives", "true")
	if err := d.cli.req(ctx, http.MethodPatch, driveAPIBase+"/files/"+url.PathEscape(src.id),
		q, map[string]any{}, nil); err != nil {
		return err
	}
	d.cacheClear()
	return nil
}

func (d *GDrive) Copy(ctx context.Context, srcRel, dstDirRel string) error {
	src, err := d.lookup(ctx, srcRel)
	if err != nil {
		return err
	}
	if src.isDir {
		return driver.ErrNotSupported // Drive files.copy 不递归复制文件夹
	}
	dstDir, err := d.lookup(ctx, dstDirRel)
	if err != nil {
		return err
	}
	if !dstDir.isDir {
		return driver.ErrNotFound
	}
	if _, err := d.lookup(ctx, util.JoinRel(strings.Trim(dstDirRel, "/"), src.name)); err == nil {
		return driver.ErrExist
	}
	q := url.Values{"fields": {"id"}, "supportsAllDrives": {"true"}}
	body := map[string]any{"parents": []string{dstDir.id}, "name": src.name}
	if err := d.cli.req(ctx, http.MethodPost, driveAPIBase+"/files/"+url.PathEscape(src.id)+"/copy",
		q, body, nil); err != nil {
		return err
	}
	d.cacheClear()
	return nil
}

// Put 上传：目标目录已有同名文件则更新其内容（覆盖），否则新建；<5MiB multipart，否则 resumable 分块。
func (d *GDrive) Put(ctx context.Context, dstDirRel, name string, r io.Reader, size int64) error {
	if err := checkName(name); err != nil {
		return err
	}
	dstDir, err := d.lookup(ctx, dstDirRel)
	if err != nil {
		return err
	}
	if !dstDir.isDir {
		return driver.ErrNotFound
	}
	// 查同名文件（覆盖语义：存在则更新内容，避免 Drive 产生重名副本）。
	// 用精确查询而非列全目录——批量上传同目录时列全目录是 O(n²)。
	existingID := ""
	if f, ok, err := d.findChild(ctx, dstDir.id, name); err == nil && ok && !f.isDir {
		existingID = f.id
	}
	if size < 0 { // 未知长度：先落临时文件测长（内存恒定）
		tmp, err := os.CreateTemp("", "nl-gd-*")
		if err != nil {
			return err
		}
		defer func() { tmp.Close(); os.Remove(tmp.Name()) }()
		if size, err = io.Copy(tmp, r); err != nil {
			return err
		}
		if _, err = tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		r = tmp
	}
	if size < simpleUploadLimit {
		err = d.putSimple(ctx, dstDir.id, existingID, name, r, size)
	} else {
		err = d.putResumable(ctx, dstDir.id, existingID, name, r, size)
	}
	if err == nil {
		d.cacheClear()
	}
	return err
}

// putSimple 小文件：新建走 multipart/related（元数据+内容一次），覆盖走 uploadType=media（仅内容）。
func (d *GDrive) putSimple(ctx context.Context, parentID, existingID, name string, r io.Reader, size int64) error {
	tok, err := d.cli.token(ctx)
	if err != nil {
		return err
	}
	if existingID != "" {
		u := driveUploadBase + "/files/" + url.PathEscape(existingID) + "?uploadType=media&supportsAllDrives=true"
		return doUpload(ctx, http.MethodPatch, u, tok, "application/octet-stream", r, size)
	}
	// 新建：multipart/related（part1=元数据 JSON，part2=二进制）。小文件缓冲进内存无妨。
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h1 := textproto.MIMEHeader{}
	h1.Set("Content-Type", "application/json; charset=UTF-8")
	p1, err := mw.CreatePart(h1)
	if err != nil {
		return err
	}
	fmt.Fprintf(p1, `{"name":%q,"parents":[%q]}`, name, parentID)
	h2 := textproto.MIMEHeader{}
	h2.Set("Content-Type", "application/octet-stream")
	p2, err := mw.CreatePart(h2)
	if err != nil {
		return err
	}
	if _, err := io.Copy(p2, r); err != nil {
		return err
	}
	mw.Close()
	u := driveUploadBase + "/files?uploadType=multipart&supportsAllDrives=true"
	// 用 bytes.Reader 而非 &buf：内容已经整个在内存里，给 doUpload 一个能回绕的 body，
	// 撞限流时就能就地退避重发，不必把整个文件退回上层重来。
	body := bytes.NewReader(buf.Bytes())
	return doUpload(ctx, http.MethodPost, u, tok, "multipart/related; boundary="+mw.Boundary(), body, body.Size())
}

// doUpload 发一个上传请求并按 Drive 错误体映射失败。
// 撞限流时退避重试——但仅限 body 能回绕（io.Seeker）的情况：转存时 r 是源端网络流，
// 读过就没了，就地重发只会传出半个文件，那种情况原样返回让上层重来整个文件。
func doUpload(ctx context.Context, method, u, tok, contentType string, r io.Reader, size int64) error {
	seeker, rewindable := r.(io.Seeker)
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, u, r)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", contentType)
		if size >= 0 {
			req.ContentLength = size
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		var ge gError
		json.Unmarshal(data, &ge)
		if rewindable && isRateLimited(resp.StatusCode, ge.reason()) && attempt <= maxThrottleRetries {
			select {
			case <-time.After(util.ThrottleWait(attempt, resp.Header.Get("Retry-After"))):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return mapDriveError(resp.StatusCode, &ge)
	}
}

// putResumable 大文件：initiate 会话拿 Location，逐块 PUT（Content-Range），每块重试 2 次。
func (d *GDrive) putResumable(ctx context.Context, parentID, existingID, name string, r io.Reader, size int64) error {
	tok, err := d.cli.token(ctx)
	if err != nil {
		return err
	}
	var initURL, method string
	var meta string
	if existingID != "" {
		initURL = driveUploadBase + "/files/" + url.PathEscape(existingID) + "?uploadType=resumable&supportsAllDrives=true"
		method, meta = http.MethodPatch, "{}"
	} else {
		initURL = driveUploadBase + "/files?uploadType=resumable&supportsAllDrives=true"
		method = http.MethodPost
		meta = fmt.Sprintf(`{"name":%q,"parents":[%q]}`, name, parentID)
	}
	req, err := http.NewRequestWithContext(ctx, method, initURL, strings.NewReader(meta))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	sessionURL := resp.Header.Get("Location")
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var ge gError
		json.Unmarshal(data, &ge)
		return mapDriveError(resp.StatusCode, &ge)
	}
	if sessionURL == "" {
		return errors.New("Google Drive：未获得上传会话 URL")
	}

	buf := make([]byte, d.chunk)
	var off int64
	for off < size {
		n, err := io.ReadFull(r, buf[:min64(d.chunk, size-off)])
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		chunk := buf[:n]
		var lastErr error
		// 限流已由 putChunk 就地按 Retry-After 退避（秒级到分钟级）；这层只管网络抖动
		// 一类的硬错误，短暂让一下再重发即可，不必也不该按限流的节奏等。
		for attempt := 1; attempt <= 3; attempt++ {
			lastErr = putChunk(ctx, sessionURL, chunk, off, size)
			if lastErr == nil {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt == 3 {
				break
			}
			select {
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if lastErr != nil {
			return fmt.Errorf("Google Drive：分块上传失败(offset=%d): %w", off, lastErr)
		}
		off += int64(n)
	}
	return nil
}

// putChunk 传一个分块；限流时按服务端的 Retry-After 就地退避重发——
// 分块在内存里，重发绝对安全，且大文件的请求量几乎全在这条路径上。
func putChunk(ctx context.Context, sessionURL string, chunk []byte, off, total int64) error {
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURL, bytes.NewReader(chunk))
		if err != nil {
			return err
		}
		end := off + int64(len(chunk)) - 1
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, end, total))
		req.ContentLength = int64(len(chunk))
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		// 308 Resume Incomplete = 该块已收、还有后续；2xx = 末块完成。
		if resp.StatusCode == 308 || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
			return nil
		}
		var ge gError
		json.Unmarshal(data, &ge)
		if isRateLimited(resp.StatusCode, ge.reason()) && attempt <= maxThrottleRetries {
			select {
			case <-time.After(util.ThrottleWait(attempt, resp.Header.Get("Retry-After"))):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return mapDriveError(resp.StatusCode, &ge)
	}
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
