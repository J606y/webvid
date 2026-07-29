// Package index 维护 files 表全量索引：启动/手动触发时对全部存储 BFS 扫描，
// 写操作成功后由 handler 调用同步钩子增量更新，供搜索与媒体库查询。
package index

import (
	"context"
	"database/sql"
	"errors"
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
	"newlist/internal/util"
)

// adminIdent 扫描时使用的管理员身份（全视野）。
var adminIdent = &user.User{Role: "admin", BasePath: "/"}

// ErrBusy 重建进行中，此时不接受清空（handler 映射 409）。
var ErrBusy = errors.New("索引重建进行中")

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
	onChange   func() // 增量变动后回调（补跑预载），可空
	// pending 重建进行中收到的增量写操作。整表替换会把它们一并抹掉
	//（扫描开始时这些文件还不存在），所以替换提交后按序重放一遍。
	pending []func()
	// subtree 正在后台重扫的挂载路径 → 扫完是否还要再来一轮（见 startSubtree）。
	subtree map[string]bool
	// dirty 增量写改过条数、进度里的数字还没跟上（见 refreshCount）。
	dirty bool
}

func New(db *sql.DB, f *fs.FS) *Builder {
	b := &Builder{db: db, fs: f}
	// 进度是内存态：不回填的话，重启后一个建好的索引会显示成「共 0 项」，
	// 看着像索引没了。启动时数一次真实行数。
	if err := db.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&b.prog.Scanned); err != nil {
		log.Printf("[index] 读取索引条数失败: %v", err)
	}
	return b
}

// OnComplete 注册全量重建成功后的回调（用于触发媒体预载）。
func (b *Builder) OnComplete(fn func()) {
	b.mu.Lock()
	b.onComplete = fn
	b.mu.Unlock()
}

// OnChange 注册增量变动后的回调（上传、复制、改名、新挂载扫完）。
// 全量重建有 OnComplete，日常增删改过去谁也不通知——新放进来的视频于是永远等不到
// 后台预载，封面只能等浏览到它时当场生成。回调方自行防抖（见 preload.Schedule）。
func (b *Builder) OnChange(fn func()) {
	b.mu.Lock()
	b.onChange = fn
	b.mu.Unlock()
}

func (b *Builder) notifyChange() {
	b.mu.Lock()
	fn := b.onChange
	b.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (b *Builder) Progress() Progress {
	b.refreshCount()
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

// Clear 清空索引：删掉 files 表全部行，进度归零。文件本身不受影响。
// 重建进行中返回 ErrBusy——那时清表毫无意义，重建提交时的整表替换会把数据写回来。
// 全程持锁：期间到来的增量写（queueOrRun）与新的 Rebuild 都排在后面，不与清空交错；
// queueOrRun 的 fn() 在锁外执行，不会死锁。
func (b *Builder) Clear() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.prog.Running {
		return ErrBusy
	}
	if _, err := b.db.Exec(`DELETE FROM files`); err != nil {
		return err
	}
	b.pending = nil     // 尚未重放的增量写一并作废（索引已空，重放无意义）
	b.prog = Progress{} // scanned 归零，顺带清掉上次重建的报错
	b.dirty = false     // 表已清空，归零就是真实条数
	log.Println("[index] 索引已删除")
	return nil
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
	// 成功时 Scanned 就是刚写进去的条数；失败时整表替换回滚了，扫出来的数字作不得数，
	// 标脏让下次读进度重数一遍真实行数
	b.dirty = err != nil
	if err != nil {
		b.prog.Err = util.Humanize(err)
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

// delPrefixSQL 删除某路径及其整个子树（占位符依次为 path, path, path）。
// 前缀匹配用 substr 而非 LIKE：路径可能含 % _ 等通配字符。
const delPrefixSQL = `DELETE FROM files WHERE path=? OR substr(path,1,length(?)+1)=?||'/'`

// replaceAll 在单个事务内清空并回填 files 表。
// 清表与回填必须同一个事务：早先是先 DELETE 再一边扫一边分批写，扫云盘的那几分钟里
// 搜索和媒体库空空如也，看着像数据没了。现在扫描全程不动数据库，读到的一直是上一版
// 索引，提交那一刻整体切到新版。
func (b *Builder) replaceAll(rows []row) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // 已 Commit 时为空操作；中途出错则整体回滚，旧索引原样保留
	if _, err := tx.Exec(`DELETE FROM files`); err != nil {
		return err
	}
	stmt, err := tx.Prepare(upsertSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.Exec(r.path, r.parent, r.name, strings.ToLower(r.name),
			util.BoolInt(r.isDir), r.size, r.modified, r.extType); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// replaceSubtree 在单个事务内替换某挂载子树的全部索引行：清掉旧行 + 写入新行。
// 与 replaceAll 同理，清与填必须同一个事务，否则扫描那几分钟里这个盘在搜索和媒体库里
// 是空的，看着像数据没了。
func (b *Builder) replaceSubtree(root string, rows []row) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // 已 Commit 时为空操作；中途出错则整体回滚，旧索引原样保留
	if _, err := tx.Exec(delPrefixSQL, root, root, root); err != nil {
		return err
	}
	stmt, err := tx.Prepare(upsertSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.Exec(r.path, r.parent, r.name, strings.ToLower(r.name),
			util.BoolInt(r.isDir), r.size, r.modified, r.extType); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// markDirty 记下"索引条数已经变了"。上传、删除、重扫都只打这个标记。
func (b *Builder) markDirty() {
	b.mu.Lock()
	b.dirty = true
	b.mu.Unlock()
}

// refreshCount 有过增量改动才重数一遍 files 表，让「共 N 项」跟得上。
// 只在读进度时数：批量删除、目录复制是热路径，每笔都扫一遍全表不值当。
// 重建进行中不动，那会儿的条数由重建自己维护。
func (b *Builder) refreshCount() {
	b.mu.Lock()
	need := b.dirty && !b.prog.Running
	b.mu.Unlock()
	if !need {
		return
	}
	var n int64
	if err := b.db.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		log.Printf("[index] 读取索引条数失败: %v", err)
		return
	}
	b.mu.Lock()
	if !b.prog.Running {
		b.prog.Scanned = n
		b.dirty = false
	}
	b.mu.Unlock()
}

// queueOrRun 执行一次增量写。重建进行中时同时排进 pending：这一笔既要作用于当前
// （旧版）索引让用户马上看到，也要在整表替换之后补回去，否则会被替换抹掉。
func (b *Builder) queueOrRun(fn func()) {
	b.mu.Lock()
	if b.prog.Running {
		b.pending = append(b.pending, fn)
	}
	b.mu.Unlock()
	fn()
	b.notifyChange() // 新文件进了索引，通知预载补封面（对方防抖，不怕连着来）
}

// replayPending 重放重建期间排队的增量写。须在 finish 之后调用——那时 Running 已置否，
// 重放本身不会再次入队。
func (b *Builder) replayPending() {
	b.mu.Lock()
	pend := b.pending
	b.pending = nil
	b.mu.Unlock()
	for _, fn := range pend {
		fn()
	}
}

func (b *Builder) run() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[index] 重建 panic: %v\n%s", r, debug.Stack())
			b.finish(fmt.Errorf("索引重建内部错误: %v", r))
		}
	}()
	ctx := context.Background()
	var scanned int64
	rows := make([]row, 0, 4096)
	add := func(r row) {
		rows = append(rows, r)
		scanned++
	}

	for _, m := range b.fs.Mounts() {
		if !m.Enabled || m.Status != "" {
			continue
		}
		b.walkMount(ctx, m.Path, func(dir string) { b.update(dir, scanned) }, add)
	}
	b.update("正在写入索引", scanned)
	if err := b.replaceAll(rows); err != nil {
		b.finish(err)
		return
	}
	b.update("", scanned)
	b.finish(nil)
	b.replayPending() // 替换期间发生的上传/删除/改名补回去
	log.Printf("[index] 索引完成，共 %d 条", scanned)
	b.notifyComplete()
}

// walkMount BFS 遍历一个挂载，每读到一项交给 visit（挂载点自身也入索引，可被搜索命中）。
// onDir 在进入每个目录前调用，供全量重建刷新进度；可空。
func (b *Builder) walkMount(ctx context.Context, root string, onDir func(string), visit func(row)) {
	visit(newRow(root, model.FileInfo{Name: path.Base(root), IsDir: true}))
	queue := []string{root}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		if onDir != nil {
			onDir(dir)
		}
		items, err := b.fs.List(ctx, adminIdent, dir)
		if err != nil {
			log.Printf("[index] 列目录失败 %s: %v", dir, err)
			continue
		}
		for _, it := range items {
			full := util.JoinLogical(dir, it.Name)
			visit(newRow(full, it))
			if it.IsDir {
				queue = append(queue, full)
			}
		}
	}
}

// notifyComplete 触发后台预载（封面 + 视频源信息）。
func (b *Builder) notifyComplete() {
	b.mu.Lock()
	cb := b.onComplete
	b.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// ---- 单个挂载的重扫（存储新增/换配置/修好后调用，不动其它存储的索引行） ----

// ReplaceSubtree 后台重扫一个挂载并整体替换它在索引里的行。
// 存储的增删改过去一律触发全量重建——扫遍所有网盘再全库预载一遍，改个排序也得等几分钟。
// 真正需要动索引的只有那一个盘，扫它就够了。
func (b *Builder) ReplaceSubtree(root string) {
	b.queueOrRun(func() { b.startSubtree(root) })
}

// startSubtree 起一轮后台重扫。同一挂载已在扫时只打个标记，等这轮扫完再补一轮：
// 连点两次保存不该并发扫同一个云盘两遍，而扫到一半的那轮又不能代表最新配置。
func (b *Builder) startSubtree(root string) {
	b.mu.Lock()
	if b.subtree == nil {
		b.subtree = map[string]bool{}
	}
	if _, busy := b.subtree[root]; busy {
		b.subtree[root] = true
		b.mu.Unlock()
		return
	}
	b.subtree[root] = false
	b.mu.Unlock()
	go func() {
		for {
			if err := b.scanReplace(root); err != nil {
				log.Printf("[index] 重扫 %s 失败: %v", root, err)
			}
			b.notifyChange() // 这个盘扫完了，去给新内容补封面与源信息
			b.mu.Lock()
			again := b.subtree[root]
			if !again {
				delete(b.subtree, root)
				b.mu.Unlock()
				return
			}
			b.subtree[root] = false
			b.mu.Unlock()
		}
	}()
}

// scanReplace 扫完一个挂载，再一次性替换它的索引行。
// 挂载不存在、已停用或初始化失败时原样保留旧行：这些情况扫不出任何东西，清空只会让一次
// 网络抖动抹掉整个盘的索引。真要摘掉（删除存储、停用）由调用方显式 DeletePrefix。
func (b *Builder) scanReplace(root string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("索引重扫内部错误: %v", r)
			log.Printf("[index] 重扫 %s panic: %v\n%s", root, r, debug.Stack())
		}
	}()
	var mount *fs.Mount
	for _, m := range b.fs.Mounts() {
		if m.Path == root {
			mount = m
			break
		}
	}
	if mount == nil || !mount.Enabled || mount.Status != "" {
		return nil
	}
	rows := make([]row, 0, 1024)
	b.walkMount(context.Background(), root, nil, func(r row) { rows = append(rows, r) })
	if e := b.replaceSubtree(root, rows); e != nil {
		return e
	}
	log.Printf("[index] 已重扫 %s，共 %d 条", root, len(rows))
	b.markDirty()
	b.notifyComplete() // 新进来的媒体交给后台预载预热封面与源信息
	return nil
}

// ---- 写操作同步钩子（handler 成功后调用；失败仅记日志，不影响主流程） ----

// Upsert 单条写入（上传 / 新建目录）。
func (b *Builder) Upsert(logical string, fi model.FileInfo) {
	b.queueOrRun(func() { b.upsert(logical, fi) })
}

func (b *Builder) upsert(logical string, fi model.FileInfo) {
	r := newRow(logical, fi)
	if _, err := b.db.Exec(upsertSQL, r.path, r.parent, r.name, strings.ToLower(r.name),
		util.BoolInt(r.isDir), r.size, r.modified, r.extType); err != nil {
		log.Printf("[index] upsert %s: %v", logical, err)
		return
	}
	b.markDirty()
}

// DeletePrefix 删除路径及其整个子树的索引行。
func (b *Builder) DeletePrefix(logical string) {
	b.queueOrRun(func() {
		if _, err := b.db.Exec(delPrefixSQL, logical, logical, logical); err != nil {
			log.Printf("[index] delete %s: %v", logical, err)
			return
		}
		b.markDirty()
	})
}

// RenamePrefix 重命名/移动：把 oldPath 前缀整体替换为 newPath。
func (b *Builder) RenamePrefix(oldPath, newPath string) {
	b.queueOrRun(func() { b.renamePrefix(oldPath, newPath) })
}

func (b *Builder) renamePrefix(oldPath, newPath string) {
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
// 整棵子树作为一笔排进重放队列（内部走不入队的 upsert），否则一次目录复制
// 会往队列里塞成千上万条。
func (b *Builder) ScanSubtree(logical string) {
	b.queueOrRun(func() { b.scanSubtree(logical) })
}

func (b *Builder) scanSubtree(logical string) {
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
		b.upsert(logical, fi)
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
				full := util.JoinLogical(dir, it.Name)
				b.upsert(full, it)
				if it.IsDir {
					queue = append(queue, full)
				}
			}
		}
	}()
}
