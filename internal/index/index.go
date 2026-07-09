// Package index 维护 files 表全量索引：启动/手动触发时对全部存储 BFS 扫描，
// 写操作成功后由 handler 调用同步钩子增量更新，供搜索与媒体库查询。
package index

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"path"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"newlist/internal/fs"
	"newlist/internal/model"
	"newlist/internal/user"
)

// adminIdent 扫描时使用的管理员身份（全视野）。
var adminIdent = &user.User{Role: "admin", BasePath: "/"}

type Progress struct {
	Running bool   `json:"running"`
	Scanned int64  `json:"scanned"`
	Current string `json:"current"`
	Err     string `json:"err"`
}

type Builder struct {
	db *sql.DB
	fs *fs.FS

	mu         sync.Mutex
	prog       Progress
	onComplete func() // 全量重建成功后回调（后台预载封面/源信息），可空
}

func New(db *sql.DB, f *fs.FS) *Builder { return &Builder{db: db, fs: f} }

// OnComplete 注册全量重建成功后的回调（用于触发媒体预载）。
func (b *Builder) OnComplete(fn func()) {
	b.mu.Lock()
	b.onComplete = fn
	b.mu.Unlock()
}

func (b *Builder) Progress() Progress {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.prog
}

// Rebuild 触发后台全量重建；已在运行时返回 false（handler 映射 409）。
func (b *Builder) Rebuild() bool {
	b.mu.Lock()
	if b.prog.Running {
		b.mu.Unlock()
		return false
	}
	b.prog = Progress{Running: true}
	b.mu.Unlock()
	go b.run()
	return true
}

func (b *Builder) update(current string, scanned int64) {
	b.mu.Lock()
	b.prog.Current = current
	b.prog.Scanned = scanned
	b.mu.Unlock()
}

func (b *Builder) finish(err error) {
	b.mu.Lock()
	b.prog.Running = false
	b.prog.Current = ""
	b.prog.Err = ""
	if err != nil {
		b.prog.Err = err.Error()
	}
	b.mu.Unlock()
	if err != nil {
		log.Printf("[index] 重建失败: %v", err)
	}
}

type row struct {
	path, parent, name string
	isDir              bool
	size               int64
	modified           string
	extType            string
}

func newRow(full string, fi model.FileInfo) row {
	ext := "other"
	if !fi.IsDir {
		ext = model.ExtType(fi.Name)
	}
	mod := ""
	if !fi.Modified.IsZero() {
		mod = fi.Modified.UTC().Format(time.RFC3339)
	}
	return row{
		path: full, parent: path.Dir(full), name: path.Base(full),
		isDir: fi.IsDir, size: fi.Size, modified: mod, extType: ext,
	}
}

const upsertSQL = `INSERT OR REPLACE INTO files(path,parent,name,name_lower,is_dir,size,modified,ext_type)
	VALUES(?,?,?,?,?,?,?,?)`

func (b *Builder) flush(batch []row) error {
	if len(batch) == 0 {
		return nil
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(upsertSQL)
	if err != nil {
		tx.Rollback()
		return err
	}
	for _, r := range batch {
		if _, err := stmt.Exec(r.path, r.parent, r.name, strings.ToLower(r.name),
			boolInt(r.isDir), r.size, r.modified, r.extType); err != nil {
			stmt.Close()
			tx.Rollback()
			return err
		}
	}
	stmt.Close()
	return tx.Commit()
}

func (b *Builder) run() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[index] 重建 panic: %v\n%s", r, debug.Stack())
			b.finish(fmt.Errorf("索引重建内部错误: %v", r))
		}
	}()
	ctx := context.Background()
	if _, err := b.db.Exec(`DELETE FROM files`); err != nil {
		b.finish(err)
		return
	}
	var scanned int64
	batch := make([]row, 0, 500)
	add := func(r row) error {
		batch = append(batch, r)
		scanned++
		if len(batch) >= 500 {
			err := b.flush(batch)
			batch = batch[:0]
			return err
		}
		return nil
	}

	for _, m := range b.fs.Mounts() {
		if !m.Enabled || m.Status != "" {
			continue
		}
		// 挂载点自身也入索引（可被搜索命中）
		if err := add(newRow(m.Path, model.FileInfo{Name: path.Base(m.Path), IsDir: true})); err != nil {
			b.finish(err)
			return
		}
		queue := []string{m.Path}
		for len(queue) > 0 {
			dir := queue[0]
			queue = queue[1:]
			b.update(dir, scanned)
			items, err := b.fs.List(ctx, adminIdent, dir)
			if err != nil {
				log.Printf("[index] 列目录失败 %s: %v", dir, err)
				continue
			}
			for _, it := range items {
				full := joinPath(dir, it.Name)
				if err := add(newRow(full, it)); err != nil {
					b.finish(err)
					return
				}
				if it.IsDir {
					queue = append(queue, full)
				}
			}
		}
	}
	if err := b.flush(batch); err != nil {
		b.finish(err)
		return
	}
	b.update("", scanned)
	b.finish(nil)
	log.Printf("[index] 索引完成，共 %d 条", scanned)

	b.mu.Lock()
	cb := b.onComplete
	b.mu.Unlock()
	if cb != nil {
		cb() // 触发后台预载（封面 + 视频源信息）
	}
}

// ---- 写操作同步钩子（handler 成功后调用；失败仅记日志，不影响主流程） ----

// Upsert 单条写入（上传 / 新建目录）。
func (b *Builder) Upsert(logical string, fi model.FileInfo) {
	r := newRow(logical, fi)
	if _, err := b.db.Exec(upsertSQL, r.path, r.parent, r.name, strings.ToLower(r.name),
		boolInt(r.isDir), r.size, r.modified, r.extType); err != nil {
		log.Printf("[index] upsert %s: %v", logical, err)
	}
}

// DeletePrefix 删除路径及其整个子树的索引行。
// 前缀匹配用 substr 而非 LIKE：路径可能含 % _ 等通配字符。
func (b *Builder) DeletePrefix(logical string) {
	if _, err := b.db.Exec(
		`DELETE FROM files WHERE path=? OR substr(path,1,length(?)+1)=?||'/'`,
		logical, logical, logical); err != nil {
		log.Printf("[index] delete %s: %v", logical, err)
	}
}

// RenamePrefix 重命名/移动：把 oldPath 前缀整体替换为 newPath。
func (b *Builder) RenamePrefix(oldPath, newPath string) {
	tx, err := b.db.Begin()
	if err != nil {
		log.Printf("[index] rename begin: %v", err)
		return
	}
	name := path.Base(newPath)
	// 自身行：路径、父目录、名称、扩展类型全部重算（目录恒为 other）
	if _, err := tx.Exec(
		`UPDATE files SET path=?, parent=?, name=?, name_lower=?,
		 ext_type=CASE WHEN is_dir=1 THEN 'other' ELSE ? END WHERE path=?`,
		newPath, path.Dir(newPath), name, strings.ToLower(name),
		model.ExtType(name), oldPath); err != nil {
		tx.Rollback()
		log.Printf("[index] rename self %s: %v", oldPath, err)
		return
	}
	// 子树行：path/parent 都以 oldPath 开头，统一做前缀替换
	// （直接子项 parent==oldPath 时 substr 取出空串，结果恰为 newPath）
	if _, err := tx.Exec(
		`UPDATE files SET path=?||substr(path,length(?)+1), parent=?||substr(parent,length(?)+1)
		 WHERE substr(path,1,length(?)+1)=?||'/'`,
		newPath, oldPath, newPath, oldPath, oldPath, oldPath); err != nil {
		tx.Rollback()
		log.Printf("[index] rename subtree %s: %v", oldPath, err)
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[index] rename commit: %v", err)
	}
}

// ScanSubtree 后台扫描某子树并写入索引（复制目录后调用）。
func (b *Builder) ScanSubtree(logical string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[index] ScanSubtree %s panic: %v", logical, r)
			}
		}()
		ctx := context.Background()
		fi, err := b.fs.Get(ctx, adminIdent, logical)
		if err != nil {
			return
		}
		b.Upsert(logical, fi)
		if !fi.IsDir {
			return
		}
		queue := []string{logical}
		for len(queue) > 0 {
			dir := queue[0]
			queue = queue[1:]
			items, err := b.fs.List(ctx, adminIdent, dir)
			if err != nil {
				continue
			}
			for _, it := range items {
				full := joinPath(dir, it.Name)
				b.Upsert(full, it)
				if it.IsDir {
					queue = append(queue, full)
				}
			}
		}
	}()
}

func joinPath(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
