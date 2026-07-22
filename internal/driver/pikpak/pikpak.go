package pikpak

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
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
		Name: "pikpak", Label: "PikPak", Remote: true,
		Fields: []driver.FieldSpec{
			{Name: "platform", Label: "客户端类型", Type: "select", Default: "web",
				Options: []string{"android", "web", "pc"},
				Help:    "签名所用客户端身份，一般用 web 即可"},
			{Name: "username", Label: "账号", Type: "string",
				Help: "邮箱 / 手机号 / 用户名；与「刷新令牌」二选一即可"},
			{Name: "password", Label: "密码", Type: "password", Secret: true,
				Help: "配合账号使用；仅用「刷新令牌」登录时可留空"},
			{Name: "refresh_token", Label: "刷新令牌", Type: "password", Secret: true,
				Help: "填了就直接用它登录（免账密）；账密登录成功后也会自动获取并回写。须与「设备 ID」成对填写"},
			{Name: "captcha_token", Label: "验证码令牌", Type: "password", Secret: true,
				Help: "仅账密登录触发人机验证时才需要：在官方验证页或 pik.bilivo.top 登录拿到已验证的 captcha_token 贴入，须与「设备 ID」同源，用一次即弃"},
			{Name: "device_id", Label: "设备 ID", Type: "string",
				Help: "纯账密登录留空会自动生成并保存；若用现成的刷新令牌/验证码令牌，必须填其来源的设备 ID"},
			{Name: "root_folder_id", Label: "根目录 ID", Type: "string",
				Help: "留空=整个网盘；填 PikPak 文件夹 ID 可只挂载该子目录"},
		},
	}, func() driver.Driver { return &PikPak{} })
}

// PikPak 是 PikPak 网盘驱动（ID 寻址，内部做路径→ID 解析与缓存）。
type PikPak struct {
	cli      *client
	root     string // root_folder_id，"" = 网盘根
	persist  func(driver.Config) error
	now      func() time.Time
	cacheTTL time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry // rel 路径 → 解析结果
}

type cacheEntry struct {
	f  pkFile
	at time.Time
}

// pkFile 是 PikPak 文件条目的最小投影。
type pkFile struct {
	id       string
	name     string
	size     int64
	isDir    bool
	modified time.Time
	thumb    string
	webLink  string
}

// rawFile 对应 API 返回的文件 JSON。
type rawFile struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	Size           string `json:"size"`
	ModifiedTime   string `json:"modified_time"`
	ThumbnailLink  string `json:"thumbnail_link"`
	WebContentLink string `json:"web_content_link"`
	Medias         []struct {
		Link struct {
			URL string `json:"url"`
		} `json:"link"`
	} `json:"medias"`
}

func (r *rawFile) toFile() pkFile {
	size, _ := strconv.ParseInt(r.Size, 10, 64)
	mod, _ := time.Parse(time.RFC3339, r.ModifiedTime)
	return pkFile{
		id:       r.ID,
		name:     r.Name,
		size:     size,
		isDir:    r.Kind == "drive#folder",
		modified: mod,
		thumb:    r.ThumbnailLink,
		webLink:  r.WebContentLink,
	}
}

func (f pkFile) fileInfo() model.FileInfo {
	return model.FileInfo{Name: f.name, Size: f.size, IsDir: f.isDir, Modified: f.modified}
}

func (d *PikPak) SetPersist(fn func(driver.Config) error) { d.persist = fn }

func (d *PikPak) Init(ctx context.Context, cfg driver.Config) error {
	if d.now == nil {
		d.now = time.Now
	}
	if d.cacheTTL == 0 {
		d.cacheTTL = 2 * time.Minute
	}
	d.cache = map[string]cacheEntry{}
	d.root = strings.TrimSpace(cfg["root_folder_id"])

	pf, ok := platforms[cfg["platform"]]
	if !ok {
		pf = platforms["web"]
	}
	hasAccount := cfg["username"] != "" && cfg["password"] != ""
	// captcha_token 只是账密登录的辅助，单独给它无法登录；至少要有账密或 refresh_token。
	if !hasAccount && cfg["refresh_token"] == "" {
		return fmt.Errorf("pikpak: 请填写账号与密码，或填写「刷新令牌 + 设备 ID」")
	}

	deviceID := strings.TrimSpace(cfg["device_id"])
	if deviceID == "" {
		// 现成的 refresh_token / captcha_token 都绑定其来源设备，device_id 必须一并提供，不能凭空生成。
		if cfg["refresh_token"] != "" || cfg["captcha_token"] != "" {
			return fmt.Errorf("pikpak: 使用现成的刷新令牌/验证码令牌时，必须一并填写其来源的设备 ID")
		}
		// 纯账密登录：按账密派生稳定 device_id（可复现）并保存。
		sum := md5.Sum([]byte(cfg["username"] + cfg["password"]))
		deviceID = hex.EncodeToString(sum[:])
		cfg["device_id"] = deviceID
		if d.persist != nil {
			_ = d.persist(cfg)
		}
	}

	c := &client{
		authBase:       defaultAuthBase,
		driveBase:      defaultDriveBase,
		pf:             pf,
		username:       cfg["username"],
		password:       cfg["password"],
		deviceID:       deviceID,
		refreshToken:   cfg["refresh_token"],
		initialCaptcha: cfg["captcha_token"],
		persist:        d.persist,
		cfg:            cfg,
		now:            d.now,
	}
	d.cli = c

	// 验证配置：取一次 token（内部会按需登录），再拉一次登录后 captcha_token。
	if _, err := c.token(ctx); err != nil {
		return err
	}
	return c.refreshCaptcha(ctx, "GET:/drive/v1/files")
}

func (d *PikPak) Drop() error { return nil }

// filesResp 是 list 端点响应。
type filesResp struct {
	Files         []rawFile `json:"files"`
	NextPageToken string    `json:"next_page_token"`
}

// listAll 列出某文件夹 ID 下所有条目（翻页合并）。
func (d *PikPak) listAll(ctx context.Context, parentID string) ([]pkFile, error) {
	var out []pkFile
	pageToken := ""
	for {
		q := url.Values{}
		q.Set("parent_id", parentID)
		q.Set("thumbnail_size", "SIZE_LARGE")
		q.Set("with_audit", "true")
		q.Set("limit", "100")
		q.Set("filters", `{"phase":{"eq":"PHASE_TYPE_COMPLETE"},"trashed":{"eq":false}}`)
		if pageToken != "" {
			q.Set("page_token", pageToken)
		}
		var resp filesResp
		if err := d.cli.req(ctx, http.MethodGet, d.cli.driveBase+"/drive/v1/files", q, nil, &resp); err != nil {
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

// lookup 把相对路径解析为 pkFile；"" = 根目录。带 TTL 缓存。
func (d *PikPak) lookup(ctx context.Context, rel string) (pkFile, error) {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return pkFile{id: d.root, isDir: true, name: ""}, nil
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
		return pkFile{}, err
	}
	if !parent.isDir {
		return pkFile{}, driver.ErrNotFound
	}
	children, err := d.listAll(ctx, parent.id)
	if err != nil {
		return pkFile{}, err
	}
	base := path.Base(rel)
	for _, ch := range children {
		d.cachePut(util.JoinRel(parentRel, ch.name), ch)
		if ch.name == base {
			return ch, nil
		}
	}
	return pkFile{}, driver.ErrNotFound
}

func (d *PikPak) cacheGet(rel string) (pkFile, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.cache[rel]
	if !ok || d.now().Sub(e.at) > d.cacheTTL {
		return pkFile{}, false
	}
	return e.f, true
}

func (d *PikPak) cachePut(rel string, f pkFile) {
	d.mu.Lock()
	d.cache[rel] = cacheEntry{f: f, at: d.now()}
	d.mu.Unlock()
}

func (d *PikPak) cacheClear() {
	d.mu.Lock()
	d.cache = map[string]cacheEntry{}
	d.mu.Unlock()
}

func (d *PikPak) List(ctx context.Context, relPath string) ([]model.FileInfo, error) {
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
	for _, ch := range children {
		d.cachePut(util.JoinRel(rel, ch.name), ch)
		out = append(out, ch.fileInfo())
	}
	return out, nil
}

func (d *PikPak) Stat(ctx context.Context, relPath string) (model.FileInfo, error) {
	f, err := d.lookup(ctx, relPath)
	if err != nil {
		return model.FileInfo{}, err
	}
	if strings.Trim(relPath, "/") == "" {
		f.isDir = true
	}
	return f.fileInfo(), nil
}

func (d *PikPak) Link(ctx context.Context, relPath string) (*driver.Link, error) {
	f, err := d.lookup(ctx, relPath)
	if err != nil {
		return nil, err
	}
	if f.isDir {
		return nil, driver.ErrNotFound
	}
	q := url.Values{}
	q.Set("_magic", "2021")
	q.Set("usage", "FETCH")
	q.Set("thumbnail_size", "SIZE_LARGE")
	var raw rawFile
	if err := d.cli.req(ctx, http.MethodGet, d.cli.driveBase+"/drive/v1/files/"+url.PathEscape(f.id),
		q, nil, &raw); err != nil {
		return nil, err
	}
	link := raw.WebContentLink
	if link == "" && len(raw.Medias) > 0 {
		link = raw.Medias[0].Link.URL
	}
	if link == "" {
		return nil, driver.ErrNotFound
	}
	// 直链绑定客户端 UA；代理模式时上层复用该头。
	h := http.Header{}
	h.Set("User-Agent", d.cli.pf.userAgent)
	return &driver.Link{URL: link, Header: h, Size: f.size, Mod: f.modified}, nil
}

func (d *PikPak) Thumb(ctx context.Context, relPath string) (string, error) {
	f, err := d.lookup(ctx, relPath)
	if err != nil {
		return "", err
	}
	if f.thumb == "" {
		return "", driver.ErrNotFound
	}
	return f.thumb, nil
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
	if len(name) > 250 {
		return driver.ErrBadName
	}
	return nil
}

func (d *PikPak) MakeDir(ctx context.Context, relPath string) error {
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
	curRel := ""
	for i, seg := range segs {
		curRel = util.JoinRel(curRel, seg)
		children, err := d.listAll(ctx, parentID)
		if err != nil {
			return err
		}
		var found *pkFile
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

func (d *PikPak) createFolder(ctx context.Context, parentID, name string) (string, error) {
	body := map[string]any{"kind": "drive#folder", "parent_id": parentID, "name": name}
	var resp struct {
		File rawFile `json:"file"`
	}
	if err := d.cli.req(ctx, http.MethodPost, d.cli.driveBase+"/drive/v1/files", nil, body, &resp); err != nil {
		return "", err
	}
	return resp.File.ID, nil
}

func (d *PikPak) Rename(ctx context.Context, relPath, newName string) error {
	if err := checkName(newName); err != nil {
		return err
	}
	f, err := d.lookup(ctx, relPath)
	if err != nil {
		return err
	}
	err = d.cli.req(ctx, http.MethodPatch, d.cli.driveBase+"/drive/v1/files/"+url.PathEscape(f.id),
		nil, map[string]any{"name": newName}, nil)
	if err != nil {
		return err
	}
	d.cacheClear()
	return nil
}

func (d *PikPak) Remove(ctx context.Context, relPath string) error {
	if strings.Trim(relPath, "/") == "" {
		return driver.ErrNotSupported
	}
	f, err := d.lookup(ctx, relPath)
	if err != nil {
		return err
	}
	err = d.cli.req(ctx, http.MethodPost, d.cli.driveBase+"/drive/v1/files:batchTrash",
		nil, map[string]any{"ids": []string{f.id}}, nil)
	if err != nil {
		return err
	}
	d.cacheClear()
	return nil
}

func (d *PikPak) Move(ctx context.Context, srcRel, dstDirRel string) error {
	return d.moveOrCopy(ctx, srcRel, dstDirRel, "files:batchMove")
}

func (d *PikPak) Copy(ctx context.Context, srcRel, dstDirRel string) error {
	return d.moveOrCopy(ctx, srcRel, dstDirRel, "files:batchCopy")
}

func (d *PikPak) moveOrCopy(ctx context.Context, srcRel, dstDirRel, op string) error {
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
	// 预检查目标是否已有同名条目（PikPak 允许重名，需自行拒绝以贴合语义）。
	dstPath := util.JoinRel(strings.Trim(dstDirRel, "/"), src.name)
	if _, err := d.lookup(ctx, dstPath); err == nil {
		return driver.ErrExist
	}
	body := map[string]any{"ids": []string{src.id}, "to": map[string]any{"parent_id": dstDir.id}}
	if err := d.cli.req(ctx, http.MethodPost, d.cli.driveBase+"/drive/v1/"+op, nil, body, nil); err != nil {
		return err
	}
	d.cacheClear()
	return nil
}
