// Package fs 维护虚拟挂载树：把 storages 表中的挂载路径映射到驱动实例，
// 统一做路径归一化、用户 base_path 视野强制、根/中间层虚拟目录合并。
package fs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"newlist/internal/driver"
	"newlist/internal/limiter"
	"newlist/internal/model"
	"newlist/internal/stream"
	"newlist/internal/user"
)

var ErrBadPath = errors.New("路径非法")

type Mount struct {
	ID      int64         `json:"id"`
	Path    string        `json:"mount_path"`
	Driver  string        `json:"driver"`
	Enabled bool          `json:"enabled"`
	Status  string        `json:"status"` // "" = ok，否则为 Init 错误信息
	Cfg     driver.Config `json:"-"`
	drv     driver.Driver
}

type FS struct {
	db      *sql.DB
	mu      sync.RWMutex
	mounts  []*Mount // 按挂载路径长度降序，用于最长前缀匹配
	copyLim *limiter.Limiter
}

func New(db *sql.DB) *FS { return &FS{db: db} }

// SetCopyLimiter 设置跨存储转存的全局限速器（所有复制任务共享同一速率配额）。
func (f *FS) SetCopyLimiter(l *limiter.Limiter) { f.copyLim = l }

// NormPath 归一化逻辑路径：拒绝反斜杠与 NUL，POSIX Clean，恒以 / 开头。
func NormPath(p string) (string, error) {
	if strings.ContainsAny(p, "\\\x00") {
		return "", ErrBadPath
	}
	return path.Clean("/" + p), nil
}

// Reload 从 storages 表重建全部驱动实例（增删改存储后调用）。
func (f *FS) Reload(ctx context.Context) error {
	rows, err := f.db.Query(`SELECT id, mount_path, driver, config, enabled FROM storages ORDER BY ord, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var next []*Mount
	for rows.Next() {
		m := &Mount{}
		var cfgJSON string
		var enabled int
		if err := rows.Scan(&m.ID, &m.Path, &m.Driver, &cfgJSON, &enabled); err != nil {
			return err
		}
		m.Enabled = enabled != 0
		m.Path = path.Clean("/" + strings.ReplaceAll(m.Path, "\\", "/"))
		if err := json.Unmarshal([]byte(cfgJSON), &m.Cfg); err != nil {
			m.Cfg = driver.Config{}
		}
		next = append(next, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, m := range next {
		if !m.Enabled {
			m.Status = "已停用"
			continue
		}
		factory, ok := driver.Get(m.Driver)
		if !ok {
			m.Status = "未知驱动: " + m.Driver
			continue
		}
		d := factory()
		if cp, ok := d.(driver.ConfigPersister); ok {
			id := m.ID
			cp.SetPersist(func(cfg driver.Config) error {
				b, err := json.Marshal(cfg)
				if err != nil {
					return err
				}
				_, err = f.db.Exec(`UPDATE storages SET config=? WHERE id=?`, string(b), id)
				return err
			})
		}
		ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := d.Init(ictx, m.Cfg)
		cancel()
		if err != nil {
			m.Status = err.Error()
			log.Printf("[fs] 存储 %s (%s) 初始化失败: %v", m.Path, m.Driver, err)
			continue
		}
		m.drv = d
	}
	sort.Slice(next, func(i, j int) bool { return len(next[i].Path) > len(next[j].Path) })

	f.mu.Lock()
	old := f.mounts
	f.mounts = next
	f.mu.Unlock()
	for _, m := range old {
		if m.drv != nil {
			m.drv.Drop()
		}
	}
	return nil
}

func (f *FS) Mounts() []*Mount {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]*Mount, len(f.mounts))
	copy(out, f.mounts)
	return out
}

// accessOK：p 是否完全在用户视野（base_path）内。
func accessOK(u *user.User, p string) bool {
	base := u.BasePath
	return base == "/" || p == base || strings.HasPrefix(p, base+"/")
}

// navOK：是否允许"看到/经过"p —— 在视野内，或是通往视野的祖先目录。
func navOK(u *user.User, p string) bool {
	if accessOK(u, p) || p == "/" {
		return true
	}
	return strings.HasPrefix(u.BasePath, p+"/")
}

// findMount 最长前缀匹配；返回挂载与相对路径。
func (f *FS) findMount(p string) (*Mount, string) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, m := range f.mounts {
		if m.drv == nil {
			continue
		}
		if p == m.Path {
			return m, ""
		}
		prefix := m.Path
		if prefix != "/" {
			prefix += "/"
		}
		if strings.HasPrefix(p, prefix) {
			return m, strings.TrimPrefix(p, prefix)
		}
	}
	return nil, ""
}

// isVirtualDir：p 是否是某挂载路径的祖先（含根）。
func (f *FS) isVirtualDir(p string) bool {
	if p == "/" {
		return true
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, m := range f.mounts {
		if strings.HasPrefix(m.Path, p+"/") {
			return true
		}
	}
	return false
}

// Resolve 严格解析：路径必须在用户视野内且落在某个存储上。
func (f *FS) Resolve(u *user.User, p string) (*Mount, string, error) {
	p, err := NormPath(p)
	if err != nil {
		return nil, "", err
	}
	if !accessOK(u, p) {
		return nil, "", driver.ErrNotFound
	}
	m, rel := f.findMount(p)
	if m == nil {
		return nil, "", driver.ErrNotFound
	}
	return m, rel, nil
}

func joinPath(p, name string) string {
	if p == "/" {
		return "/" + name
	}
	return p + "/" + name
}

// Get 返回单个条目信息（含虚拟目录）。
func (f *FS) Get(ctx context.Context, u *user.User, p string) (model.FileInfo, error) {
	p, err := NormPath(p)
	if err != nil {
		return model.FileInfo{}, err
	}
	if !navOK(u, p) {
		return model.FileInfo{}, driver.ErrNotFound
	}
	if m, rel := f.findMount(p); m != nil && accessOK(u, p) {
		fi, err := m.drv.Stat(ctx, rel)
		if err == nil {
			if rel == "" {
				fi.Name = path.Base(p)
			}
			return fi, nil
		}
		if !errors.Is(err, driver.ErrNotFound) {
			return model.FileInfo{}, err
		}
	}
	if f.isVirtualDir(p) {
		name := path.Base(p)
		if p == "/" {
			name = "/"
		}
		return model.FileInfo{Name: name, IsDir: true}, nil
	}
	return model.FileInfo{}, driver.ErrNotFound
}

// List 列目录：存储内容 + 该层的挂载点虚拟目录合并；按用户视野过滤。
func (f *FS) List(ctx context.Context, u *user.User, p string) ([]model.FileInfo, error) {
	p, err := NormPath(p)
	if err != nil {
		return nil, err
	}
	if !navOK(u, p) {
		return nil, driver.ErrNotFound
	}
	out := map[string]model.FileInfo{}
	foundAny := false

	if m, rel := f.findMount(p); m != nil && accessOK(u, p) {
		items, err := m.drv.List(ctx, rel)
		if err == nil {
			foundAny = true
			for _, it := range items {
				if navOK(u, joinPath(p, it.Name)) {
					out[it.Name] = it
				}
			}
		} else if !errors.Is(err, driver.ErrNotFound) {
			return nil, err
		}
	}

	// 挂载点虚拟目录（挂载路径的下一段）
	prefix := p
	if prefix != "/" {
		prefix += "/"
	}
	f.mu.RLock()
	for _, m := range f.mounts {
		if m.Path == p || !strings.HasPrefix(m.Path, prefix) {
			continue
		}
		seg := strings.SplitN(strings.TrimPrefix(m.Path, prefix), "/", 2)[0]
		fp := joinPath(p, seg)
		if navOK(u, fp) {
			out[seg] = model.FileInfo{Name: seg, IsDir: true}
			foundAny = true
		}
	}
	f.mu.RUnlock()

	if !foundAny && !f.isVirtualDir(p) {
		return nil, driver.ErrNotFound
	}
	items := make([]model.FileInfo, 0, len(out))
	for _, v := range out {
		items = append(items, v)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

// AccelOpts 挂载的加速配置（driver.CommonRemoteFields，本地驱动无这些字段→零值）。
type AccelOpts struct {
	Proxy      bool  // 代理模式：下载/播放经服务器中转
	Threads    int   // 并发 Range 连接数
	ChunkBytes int64 // 分块大小（字节）
}

// accelOpts 解析挂载配置；缺省 threads=4、chunk_mb=4，钳制到安全区间。
func (m *Mount) accelOpts() AccelOpts {
	o := AccelOpts{Proxy: m.Cfg["proxy"] == "true", Threads: 4, ChunkBytes: 4 << 20}
	if n, err := strconv.Atoi(m.Cfg["threads"]); err == nil {
		o.Threads = min(max(n, 1), 32)
	}
	if n, err := strconv.Atoi(m.Cfg["chunk_mb"]); err == nil {
		o.ChunkBytes = int64(min(max(n, 1), 64)) << 20
	}
	return o
}

// MediaVisible 该挂载的内容是否在指定界面展示（kind: video=视频库 / image=照片墙 / search=搜索）。
// 由通用配置字段 show_video / show_photo / show_search 控制，缺省展示（键不存在视为 true）。
func (m *Mount) MediaVisible(kind string) bool {
	key := "show_video"
	switch kind {
	case "image":
		key = "show_photo"
	case "search":
		key = "show_search"
	}
	return m.Cfg[key] != "false"
}

// LinkResult 是 LinkEx 的返回：内容访问方式 + 条目信息 + 加速配置 + 直链刷新回调。
type LinkResult struct {
	Link    *driver.Link
	Info    model.FileInfo
	Accel   AccelOpts
	Refresh func(ctx context.Context) (*driver.Link, error) // 直链过期时重取
}

// LinkEx 获取文件内容访问方式及所在挂载的加速配置（raw 代理/转存加速用）。
func (f *FS) LinkEx(ctx context.Context, u *user.User, p string) (*LinkResult, error) {
	m, rel, err := f.Resolve(u, p)
	if err != nil {
		return nil, err
	}
	fi, err := m.drv.Stat(ctx, rel)
	if err != nil {
		return nil, err
	}
	if fi.IsDir {
		return nil, driver.ErrNotFound
	}
	lk, err := m.drv.Link(ctx, rel)
	if err != nil {
		return nil, err
	}
	drv := m.drv
	return &LinkResult{
		Link:  lk,
		Info:  fi,
		Accel: m.accelOpts(),
		Refresh: func(ctx context.Context) (*driver.Link, error) {
			return drv.Link(ctx, rel)
		},
	}, nil
}

// Provider 把 LinkResult 适配成 stream.LinkProvider：首链只消费一次，之后经 Refresh 重取
// （直链过期时 MultiReader 会强制重调）。
func (r *LinkResult) Provider() stream.LinkProvider {
	var mu sync.Mutex
	first := r.Link
	return func(ctx context.Context) (string, http.Header, error) {
		mu.Lock()
		lk := first
		first = nil
		mu.Unlock()
		if lk == nil {
			var err error
			lk, err = r.Refresh(ctx)
			if err != nil {
				return "", nil, err
			}
		}
		if lk.URL == "" {
			return "", nil, errors.New("驱动未返回直链")
		}
		return lk.URL, lk.Header, nil
	}
}

// Caps 描述某目录下允许的操作。
type Caps struct {
	Write  bool `json:"write"`
	Upload bool `json:"upload"`
}

func (f *FS) Caps(u *user.User, p string) Caps {
	if !u.AllowWrite() {
		return Caps{}
	}
	p, err := NormPath(p)
	if err != nil || !accessOK(u, p) {
		return Caps{}
	}
	m, _ := f.findMount(p)
	if m == nil {
		return Caps{}
	}
	_, w := m.drv.(driver.Writer)
	_, up := m.drv.(driver.Uploader)
	return Caps{Write: w, Upload: up}
}

func (f *FS) writer(u *user.User, p string) (*Mount, string, driver.Writer, error) {
	m, rel, err := f.Resolve(u, p)
	if err != nil {
		return nil, "", nil, err
	}
	w, ok := m.drv.(driver.Writer)
	if !ok {
		return nil, "", nil, driver.ErrNotSupported
	}
	return m, rel, w, nil
}

func (f *FS) MakeDir(ctx context.Context, u *user.User, p string) error {
	_, rel, w, err := f.writer(u, p)
	if err != nil {
		return err
	}
	if rel == "" {
		return driver.ErrExist
	}
	return w.MakeDir(ctx, rel)
}

func (f *FS) Rename(ctx context.Context, u *user.User, p, newName string) error {
	_, rel, w, err := f.writer(u, p)
	if err != nil {
		return err
	}
	if rel == "" {
		return driver.ErrNotSupported // 挂载点本身通过存储管理改名
	}
	return w.Rename(ctx, rel, newName)
}

func (f *FS) Remove(ctx context.Context, u *user.User, p string) error {
	_, rel, w, err := f.writer(u, p)
	if err != nil {
		return err
	}
	return w.Remove(ctx, rel)
}

// MoveCopy 同存储移动/复制到目标目录；跨存储由 Transfer 后台任务接管
// （handler 用 SameStorage 预先分流，此处仅作防御性拦截）。
func (f *FS) MoveCopy(ctx context.Context, u *user.User, src, dstDir string, isMove bool) error {
	sm, srcRel, w, err := f.writer(u, src)
	if err != nil {
		return err
	}
	dm, dstRel, err := f.Resolve(u, dstDir)
	if err != nil {
		return err
	}
	if sm.ID != dm.ID {
		return errors.New("跨存储请走转存任务接口")
	}
	if isMove {
		return w.Move(ctx, srcRel, dstRel)
	}
	return w.Copy(ctx, srcRel, dstRel)
}

// Put 上传：dstDir 为目标目录逻辑路径。
func (f *FS) Put(ctx context.Context, u *user.User, dstDir, name string, r io.Reader, size int64, overwrite bool) error {
	m, rel, err := f.Resolve(u, dstDir)
	if err != nil {
		return err
	}
	up, ok := m.drv.(driver.Uploader)
	if !ok {
		return driver.ErrNotSupported
	}
	target := name
	if rel != "" {
		target = rel + "/" + name
	}
	if !overwrite {
		if _, err := m.drv.Stat(ctx, target); err == nil {
			return driver.ErrExist
		}
	}
	return up.Put(ctx, rel, name, r, size)
}

// Driver 暴露某路径对应的驱动（缩略图等服务用）。
func (f *FS) Driver(u *user.User, p string) (driver.Driver, string, error) {
	m, rel, err := f.Resolve(u, p)
	if err != nil {
		return nil, "", err
	}
	return m.drv, rel, nil
}
