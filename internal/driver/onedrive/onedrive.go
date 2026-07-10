package onedrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"newlist/internal/driver"
	"newlist/internal/model"
)

func init() {
	region := driver.FieldSpec{Name: "region", Label: "区域", Type: "select",
		Default: "global", Options: []string{"global", "cn"},
		Help: "cn = 世纪互联运营的中国区 OneDrive"}
	rootPath := driver.FieldSpec{Name: "root_folder_path", Label: "根目录路径", Type: "string",
		Default: "/", Help: "挂载 OneDrive 内的某个子目录，/ 表示整个网盘"}

	driver.Register(driver.Meta{
		Name: "onedrive", Label: "OneDrive", Remote: true,
		Fields: []driver.FieldSpec{
			region,
			{Name: "client_id", Label: "客户端 ID", Type: "string", Required: true,
				Help: "Azure 应用注册的 应用程序(客户端) ID"},
			{Name: "client_secret", Label: "客户端密码", Type: "password", Secret: true,
				Help: "机密客户端必填；公共客户端可留空"},
			{Name: "refresh_token", Label: "刷新令牌", Type: "password", Required: true, Secret: true,
				Help: "OAuth refresh_token，可直接粘贴 AList 已有值；会随轮换自动更新保存"},
			rootPath,
		},
	}, func() driver.Driver { return &OneDrive{mode: modeDelegated} })

	driver.Register(driver.Meta{
		Name: "onedrive_app", Label: "OneDrive APP", Remote: true,
		Fields: []driver.FieldSpec{
			region,
			{Name: "tenant_id", Label: "租户 ID", Type: "string", Required: true},
			{Name: "client_id", Label: "客户端 ID", Type: "string", Required: true},
			{Name: "client_secret", Label: "客户端密码", Type: "password", Required: true, Secret: true},
			{Name: "user_email", Label: "用户邮箱", Type: "string", Required: true,
				Help: "应用将访问该用户的 OneDrive（需管理员同意 Files.ReadWrite.All 应用权限）"},
			rootPath,
		},
	}, func() driver.Driver { return &OneDrive{mode: modeApp} })
}

// 上传相关常量：<4MiB 单请求；分块须为 320KiB 的倍数，10MiB 满足。
const (
	simpleUploadLimit = 4 << 20
	defaultChunkSize  = 10 << 20
)

// linkTTL 直链缓存有效期：远低于 downloadUrl 本身约 1h 的有效窗口，
// 缓存命中返回的 URL 必然仍有效（加速换链重取也安全）；每次播放/下载省一轮 Graph。
const linkTTL = 10 * time.Minute

// OneDrive 同时承担 onedrive / onedrive_app 两种模式。
type OneDrive struct {
	mode    string
	root    string // root_folder_path，POSIX、无首尾斜杠语义由 joinRel 处理
	cli     *client
	persist func(driver.Config) error
	chunk   int64 // 上传分块大小（测试可调小）

	linkMu    sync.Mutex
	linkCache map[string]cachedLink // relPath → 直链，写操作后整表清空
}

type cachedLink struct {
	url  string
	size int64
	mod  time.Time
	exp  time.Time
}

func (d *OneDrive) clearLinks() {
	d.linkMu.Lock()
	d.linkCache = map[string]cachedLink{}
	d.linkMu.Unlock()
}

// item 是 Graph driveItem 的最小投影。
type item struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Folder *struct {
		ChildCount int `json:"childCount"`
	} `json:"folder"`
	File *struct {
		MimeType string `json:"mimeType"`
	} `json:"file"`
	Modified    string `json:"lastModifiedDateTime"`
	DownloadURL string `json:"@microsoft.graph.downloadUrl"`
	ParentRef   *struct {
		DriveID string `json:"driveId"`
		ID      string `json:"id"`
	} `json:"parentReference"`
}

func (it *item) fileInfo() model.FileInfo {
	mod, _ := time.Parse(time.RFC3339, it.Modified)
	return model.FileInfo{Name: it.Name, Size: it.Size, IsDir: it.Folder != nil, Modified: mod}
}

const itemSelect = "$select=id,name,size,folder,file,lastModifiedDateTime"

func (d *OneDrive) SetPersist(fn func(driver.Config) error) { d.persist = fn }

func (d *OneDrive) Init(ctx context.Context, cfg driver.Config) error {
	ep := regionEndpoints(cfg["region"])
	d.root = strings.Trim(path.Clean("/"+cfg["root_folder_path"]), "/")
	if d.root == "." {
		d.root = ""
	}
	if d.chunk == 0 {
		d.chunk = defaultChunkSize
	}
	d.linkCache = map[string]cachedLink{}

	c := &client{
		mode:         d.mode,
		loginBase:    ep.login,
		graphBase:    ep.graph,
		tenantID:     cfg["tenant_id"],
		clientID:     cfg["client_id"],
		clientSecret: cfg["client_secret"],
		refreshToken: cfg["refresh_token"],
		persist:      d.persist,
		cfg:          cfg,
	}
	switch d.mode {
	case modeApp:
		if c.tenantID == "" || c.clientID == "" || c.clientSecret == "" || cfg["user_email"] == "" {
			return errors.New("onedrive_app: tenant_id/client_id/client_secret/user_email 均必填")
		}
		c.driveBase = ep.graph + "/users/" + escapeSeg(cfg["user_email"]) + "/drive"
	default:
		if c.clientID == "" || c.refreshToken == "" {
			return errors.New("onedrive: client_id 与 refresh_token 必填")
		}
		c.driveBase = ep.graph + "/me/drive"
	}
	d.cli = c

	// 验证配置：取一次 token + 读根条目，失败原样上抛（存储状态里可见）
	if _, err := c.token(ctx); err != nil {
		return err
	}
	var it item
	return c.req(ctx, http.MethodGet, c.itemURL(d.root, "", "?"+itemSelect), nil, &it)
}

func (d *OneDrive) Drop() error { return nil }

// checkName 按 OneDrive 命名规则校验新建名称。
func checkName(name string) error {
	if name == "" || name == "." || name == ".." {
		return driver.ErrBadName
	}
	if strings.ContainsAny(name, `"*:<>?/\|`) {
		return driver.ErrBadName
	}
	for _, r := range name {
		if r < 0x20 {
			return driver.ErrBadName
		}
	}
	if strings.HasPrefix(name, " ") || strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return driver.ErrBadName
	}
	return nil
}

// escapeSeg 转义 URL 路径单段值（用户邮箱等），并截断斜杠防路径注入。
func escapeSeg(s string) string {
	return url.PathEscape(strings.SplitN(s, "/", 2)[0])
}

type listResp struct {
	Value    []item `json:"value"`
	NextLink string `json:"@odata.nextLink"`
}

func (d *OneDrive) List(ctx context.Context, relPath string) ([]model.FileInfo, error) {
	u := d.cli.itemURL(d.root, relPath, "/children?$top=1000&"+itemSelect)
	var out []model.FileInfo
	for u != "" {
		var lr listResp
		if err := d.cli.req(ctx, http.MethodGet, u, nil, &lr); err != nil {
			return nil, err
		}
		for i := range lr.Value {
			out = append(out, lr.Value[i].fileInfo())
		}
		u = lr.NextLink
	}
	return out, nil
}

func (d *OneDrive) Stat(ctx context.Context, relPath string) (model.FileInfo, error) {
	// 直链缓存里已有该文件的 size/mtime（与 downloadUrl 同一响应取回）：直接复用。
	// 代理/转码播放每次打开都会 Stat+Link，ffmpeg 探测+seek 连开多次，
	// 叠加拉流本身的请求量容易撞 Graph 限流；缓存只含文件（目录无 downloadUrl）。
	d.linkMu.Lock()
	if cl, ok := d.linkCache[relPath]; ok && time.Now().Before(cl.exp) {
		d.linkMu.Unlock()
		return model.FileInfo{Name: path.Base(relPath), Size: cl.size, Modified: cl.mod}, nil
	}
	d.linkMu.Unlock()

	var it item
	if err := d.cli.req(ctx, http.MethodGet,
		d.cli.itemURL(d.root, relPath, "?"+itemSelect), nil, &it); err != nil {
		return model.FileInfo{}, err
	}
	fi := it.fileInfo()
	if relPath == "" {
		fi.IsDir = true
	}
	return fi, nil
}

func (d *OneDrive) Link(ctx context.Context, relPath string) (*driver.Link, error) {
	d.linkMu.Lock()
	if cl, ok := d.linkCache[relPath]; ok && time.Now().Before(cl.exp) {
		d.linkMu.Unlock()
		return &driver.Link{URL: cl.url, Size: cl.size, Mod: cl.mod}, nil
	}
	d.linkMu.Unlock()

	var it item
	if err := d.cli.req(ctx, http.MethodGet,
		d.cli.itemURL(d.root, relPath, "?$select=id,size,lastModifiedDateTime,content.downloadUrl"),
		nil, &it); err != nil {
		return nil, err
	}
	if it.DownloadURL == "" {
		return nil, driver.ErrNotFound // 目录或异常条目
	}
	mod, _ := time.Parse(time.RFC3339, it.Modified)
	d.linkMu.Lock()
	if d.linkCache == nil { // 测试可能不经 Init 直接构造
		d.linkCache = map[string]cachedLink{}
	}
	d.linkCache[relPath] = cachedLink{url: it.DownloadURL, size: it.Size, mod: mod,
		exp: time.Now().Add(linkTTL)}
	d.linkMu.Unlock()
	return &driver.Link{URL: it.DownloadURL, Size: it.Size, Mod: mod}, nil
}

// RefreshLink 直链确认失效（拉流 401/403）时强制重取：先废弃缓存条目再取新链。
// Link 的 TTL 缓存以"downloadUrl 有效期约 1h"为前提，但直链也可能被云端提前
// 作废（文件被改写、令牌撤销等）——没有强制通道，重连在 TTL 内会一直拿到同一条死链。
func (d *OneDrive) RefreshLink(ctx context.Context, relPath string) (*driver.Link, error) {
	d.linkMu.Lock()
	delete(d.linkCache, relPath)
	d.linkMu.Unlock()
	return d.Link(ctx, relPath)
}

type thumbResp struct {
	URL string `json:"url"`
}

func (d *OneDrive) Thumb(ctx context.Context, relPath string) (string, error) {
	var tr thumbResp
	if err := d.cli.req(ctx, http.MethodGet,
		d.cli.itemURL(d.root, relPath, "/thumbnails/0/large"), nil, &tr); err != nil {
		return "", err
	}
	if tr.URL == "" {
		return "", driver.ErrNotFound
	}
	return tr.URL, nil
}

// MakeDir 逐级创建（Graph 不支持递归建目录）；最后一级已存在 → ErrExist。
func (d *OneDrive) MakeDir(ctx context.Context, relPath string) error {
	relPath = strings.Trim(relPath, "/")
	if relPath == "" {
		return driver.ErrExist
	}
	segs := strings.Split(relPath, "/")
	parent := ""
	for i, seg := range segs {
		if err := checkName(seg); err != nil {
			return err
		}
		body := map[string]any{
			"name": seg, "folder": map[string]any{},
			"@microsoft.graph.conflictBehavior": "fail",
		}
		err := d.cli.req(ctx, http.MethodPost,
			d.cli.itemURL(d.root, parent, "/children"), body, nil)
		if err != nil {
			last := i == len(segs)-1
			if errors.Is(err, driver.ErrExist) && !last {
				// 中间级已存在：继续深入
			} else {
				return err
			}
		}
		parent = joinRel(parent, seg)
	}
	return nil
}

func (d *OneDrive) Rename(ctx context.Context, relPath, newName string) error {
	if err := checkName(newName); err != nil {
		return err
	}
	err := d.cli.req(ctx, http.MethodPatch,
		d.cli.itemURL(d.root, relPath, ""), map[string]any{"name": newName}, nil)
	if err == nil {
		d.clearLinks()
	}
	return err
}

func (d *OneDrive) Remove(ctx context.Context, relPath string) error {
	if strings.Trim(relPath, "/") == "" {
		return driver.ErrNotSupported // 不允许删挂载根
	}
	err := d.cli.req(ctx, http.MethodDelete, d.cli.itemURL(d.root, relPath, ""), nil, nil)
	if err == nil {
		d.clearLinks()
	}
	return err
}

func (d *OneDrive) Move(ctx context.Context, srcRel, dstDirRel string) error {
	body := map[string]any{
		"parentReference": map[string]any{"path": drivePath(d.root, dstDirRel)},
		"name":            path.Base(srcRel),
	}
	err := d.cli.req(ctx, http.MethodPatch, d.cli.itemURL(d.root, srcRel, ""), body, nil)
	if err == nil {
		d.clearLinks()
	}
	return err
}

// Copy 走 Graph 异步复制：202 + Location 监控 URL 轮询。
func (d *OneDrive) Copy(ctx context.Context, srcRel, dstDirRel string) error {
	var dst item
	if err := d.cli.req(ctx, http.MethodGet,
		d.cli.itemURL(d.root, dstDirRel, "?$select=id,parentReference"), nil, &dst); err != nil {
		return err
	}
	driveID := ""
	if dst.ParentRef != nil {
		driveID = dst.ParentRef.DriveID
	}
	parentRef := map[string]any{"id": dst.ID}
	if driveID != "" {
		parentRef["driveId"] = driveID
	}
	h, err := d.cli.reqHeader(ctx, http.MethodPost, d.cli.itemURL(d.root, srcRel, "/copy"),
		map[string]any{"parentReference": parentRef, "name": path.Base(srcRel)})
	if err != nil {
		return err
	}
	monitor := h.Get("Location")
	if monitor == "" {
		return nil // 个别情况同步完成
	}
	return waitMonitor(ctx, monitor)
}

type monitorResp struct {
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// waitMonitor 轮询异步操作监控 URL（预签名，不带 Bearer）。
func waitMonitor(ctx context.Context, u string) error {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var mr monitorResp
		if err := jsonUnmarshal(data, &mr); err != nil {
			// 监控 URL 完成后可能 303/200 返回条目本体，视为完成
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				return nil
			}
			return fmt.Errorf("onedrive: 复制监控解析失败(HTTP %d)", resp.StatusCode)
		}
		switch mr.Status {
		case "completed":
			return nil
		case "failed":
			msg := "未知原因"
			if mr.Error != nil {
				msg = mr.Error.Message
			}
			return errors.New("onedrive: 复制失败: " + msg)
		}
	}
}

// Put 上传：<4MiB 单请求 PUT content；否则 createUploadSession 分块，每块失败重试 2 次。
func (d *OneDrive) Put(ctx context.Context, dstDirRel, name string, r io.Reader, size int64) error {
	if err := checkName(name); err != nil {
		return err
	}
	if size < 0 { // 未知长度：先落临时文件测长（内存恒定）
		tmp, err := os.CreateTemp("", "nl-od-*")
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
	target := joinRel(dstDirRel, name)

	var err error
	if size < simpleUploadLimit {
		err = d.putSmall(ctx, target, r, size)
	} else {
		err = d.putSession(ctx, target, r, size)
	}
	if err == nil {
		d.clearLinks() // replace 覆盖旧文件 → 旧直链作废
	}
	return err
}

func (d *OneDrive) putSmall(ctx context.Context, targetRel string, r io.Reader, size int64) error {
	u := d.cli.itemURL(d.root, targetRel, "/content?@microsoft.graph.conflictBehavior=replace")
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, r)
	if err != nil {
		return err
	}
	tok, err := d.cli.token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = size
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	var ge graphError
	jsonUnmarshal(data, &ge)
	return mapGraphError(resp.StatusCode, ge.Error.Code, ge.Error.Message)
}

type sessionResp struct {
	UploadURL string `json:"uploadUrl"`
}

func (d *OneDrive) putSession(ctx context.Context, targetRel string, r io.Reader, size int64) error {
	var sr sessionResp
	err := d.cli.req(ctx, http.MethodPost,
		d.cli.itemURL(d.root, targetRel, "/createUploadSession"),
		map[string]any{"item": map[string]any{"@microsoft.graph.conflictBehavior": "replace"}}, &sr)
	if err != nil {
		return err
	}
	if sr.UploadURL == "" {
		return errors.New("onedrive: 未获得上传会话 URL")
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
		for attempt := 0; attempt <= 2; attempt++ { // 每块最多 3 次（1+重试2）
			lastErr = d.putChunk(ctx, sr.UploadURL, chunk, off, size)
			if lastErr == nil {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if lastErr != nil {
			return fmt.Errorf("onedrive: 分块上传失败(offset=%d): %w", off, lastErr)
		}
		off += int64(n)
	}
	return nil
}

func (d *OneDrive) putChunk(ctx context.Context, uploadURL string, chunk []byte, off, total int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(chunk))
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
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	var ge graphError
	jsonUnmarshal(data, &ge)
	return mapGraphError(resp.StatusCode, ge.Error.Code, ge.Error.Message)
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// jsonUnmarshal 小工具：空数据视为错误，避免误判成功。
func jsonUnmarshal(data []byte, v any) error {
	if len(data) == 0 {
		return errors.New("empty body")
	}
	return json.Unmarshal(data, v)
}
