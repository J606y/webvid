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

// GDrive 是 Google Drive 驱动（ID 寻址，内部做路径→ID 解析与 TTL 缓存）。
type GDrive struct {
	cli      *client
	root     string // 解析后的根目录文件 ID（root_folder_id 为空时=「我的云端硬盘」根）
	persist  func(driver.Config) error
	now      func() time.Time
	cacheTTL time.Duration
	chunk    int64

	mu    sync.Mutex
	cache map[string]cacheEntry // rel 路径 → 解析结果
}

type cacheEntry struct {
	f  gdFile
	at time.Time
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
		d.cacheTTL = 2 * time.Minute
	}
	if d.chunk == 0 {
		d.chunk = defaultChunkSize
	}
	d.cache = map[string]cacheEntry{}

	clientID := strings.TrimSpace(cfg["client_id"])
	clientSecret := strings.TrimSpace(cfg["client_secret"])
	refreshToken := strings.TrimSpace(cfg["refresh_token"])
	if clientID == "" || clientSecret == "" {
		return errors.New("googledrive: client_id 与 client_secret 必填")
	}
	if refreshToken == "" {
		return errors.New("googledrive: 尚未授权，请在后台点「授权」完成 Google 登录")
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
func (d *GDrive) lookup(ctx context.Context, rel string) (gdFile, error) {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return gdFile{id: d.root, isDir: true, name: ""}, nil
	}
	if f, ok := d.cacheGet(rel); ok {
		return f, nil
	}
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
	return gdFile{}, driver.ErrNotFound
}

func (d *GDrive) cacheGet(rel string) (gdFile, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.cache[rel]
	if !ok || d.now().Sub(e.at) > d.cacheTTL {
		return gdFile{}, false
	}
	return e.f, true
}

func (d *GDrive) cachePut(rel string, f gdFile) {
	d.mu.Lock()
	d.cache[rel] = cacheEntry{f: f, at: d.now()}
	d.mu.Unlock()
}

func (d *GDrive) cacheClear() {
	d.mu.Lock()
	d.cache = map[string]cacheEntry{}
	d.mu.Unlock()
}

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
	u := driveAPIBase + "/files/" + url.PathEscape(f.id) + "?alt=media&supportsAllDrives=true"
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
	return doUpload(ctx, http.MethodPost, u, tok, "multipart/related; boundary="+mw.Boundary(), &buf, int64(buf.Len()))
}

// doUpload 发一个上传请求并按 Drive 错误体映射失败。
func doUpload(ctx context.Context, method, u, tok, contentType string, r io.Reader, size int64) error {
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
	return mapDriveError(resp.StatusCode, &ge)
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
		return errors.New("googledrive: 未获得上传会话 URL")
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
		for attempt := 0; attempt <= 2; attempt++ {
			lastErr = putChunk(ctx, sessionURL, chunk, off, size)
			if lastErr == nil {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if lastErr != nil {
			return fmt.Errorf("googledrive: 分块上传失败(offset=%d): %w", off, lastErr)
		}
		off += int64(n)
	}
	return nil
}

func putChunk(ctx context.Context, sessionURL string, chunk []byte, off, total int64) error {
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
	return mapDriveError(resp.StatusCode, &ge)
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
