// Package task 提供内存态后台任务管理：worker pool + 状态机 + 结构化进度。
// v1 为内存实现，重启后任务列表丢失（转存本身幂等，可重发起）。
package task

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"newlist/internal/model"
	"newlist/internal/util"
)

type State string

const (
	StatePending  State = "pending"
	StateRunning  State = "running"
	StateDone     State = "done"
	StateError    State = "error"
	StateCanceled State = "canceled"
)

// 任务组：每组独立队列与 worker 池，线程数可在后台设置里分别调整。
const (
	GroupCopy    = "copy"    // 跨存储复制/移动转存
	GroupOffline = "offline" // 离线下载
)

// maxWorkers 单组线程数上限，防误填超大值把云盘 API 打挂。
const maxWorkers = 32

var (
	ErrNotFound  = errors.New("任务不存在")
	ErrForbidden = errors.New("无权操作该任务")
	ErrBadState  = errors.New("任务当前状态不允许此操作")
)

// Func 是任务执行体；通过 *Task 上报进度，ctx 取消时应尽快返回 ctx.Err()。
type Func func(ctx context.Context, t *Task) error

// FileState 是清单里单个文件的状态。
type FileState string

const (
	FilePending FileState = "pending" // 已规划，还没轮到
	FileRunning FileState = "running"
	FileDone    FileState = "done"
	FileSkipped FileState = "skipped" // 目标已有同名同大小，断点续传跳过
	FileError   FileState = "error"
)

// FileProgress 是清单里单个文件的进度快照。
type FileProgress struct {
	Path  string    `json:"path"` // 相对转存根的展示路径
	Size  int64     `json:"size"`
	Done  int64     `json:"done"`
	State FileState `json:"state"`
	Err   string    `json:"error"`
}

// FileCounts 是清单的分状态计数。随状态迁移增量维护——任务列表每 1.5 秒轮询一次，
// 几万条的清单不该每次都全表数一遍。
type FileCounts struct {
	Total   int `json:"total"`
	Pending int `json:"pending"`
	Running int `json:"running"`
	Done    int `json:"done"`
	Skipped int `json:"skipped"`
	Error   int `json:"error"`
}

func (c *FileCounts) add(st FileState, d int) {
	switch st {
	case FilePending:
		c.Pending += d
	case FileRunning:
		c.Running += d
	case FileDone:
		c.Done += d
	case FileSkipped:
		c.Skipped += d
	case FileError:
		c.Error += d
	}
}

type Task struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Group     string     `json:"group"` // 所属任务组，创建后不变
	Owner     int64      `json:"owner"`
	State     State      `json:"state"`
	Total     int64      `json:"total"`
	Done      int64      `json:"done"`
	Speed     int64      `json:"speed"`        // B/s
	CurFile   string     `json:"cur_file"`     // 在途最早的文件；与 Active 均由 active 派生，仅 snapshot 填充
	Active    int        `json:"active_files"` // 同时在途的文件数，>1 即并发复制中
	Files     FileCounts `json:"files"`        // 文件清单计数；Total=0 表示这个任务没有清单
	Err       string     `json:"error"`
	CreatedAt string     `json:"created_at"`

	mu       sync.Mutex
	cancel   context.CancelFunc
	fn       Func
	lastT    time.Time
	lastDone int64
	files    []*FileProgress // 规划出的完整清单，下标即上报用的 i
	active   []int           // 在途文件下标，按开始先后排列；派生 CurFile 与 Active
}

// SetTotal / SetFiles / FileStart / FileSkip / FileDone / AddFile 结构化满足
// fs.Progress 接口（鸭子类型，两边只共用 model.TransferFile 一个 DTO）。

// SetTotal 记任务总字节；单文件任务（离线下载）顺带补全那一个文件的大小——
// 它的大小要等响应头才知道，规划时给不出。
func (t *Task) SetTotal(n int64) {
	t.mu.Lock()
	t.Total = n
	if len(t.files) == 1 && t.files[0].Size == 0 {
		t.files[0].Size = n
	}
	t.mu.Unlock()
}

// SetFiles 交出本任务的完整文件清单（转存规划阶段一次给全），下标即后续上报用的 i。
func (t *Task) SetFiles(items []model.TransferFile) {
	list := make([]*FileProgress, len(items))
	for i, it := range items {
		list[i] = &FileProgress{Path: it.Path, Size: it.Size, State: FilePending}
	}
	t.mu.Lock()
	t.files = list
	t.active = nil
	t.Files = FileCounts{Total: len(list), Pending: len(list)}
	t.mu.Unlock()
}

// FileStart / FileDone 标记单个文件进出在途集合。并发复制时多个文件同时在途，
// 展示取 active[0]——最早开始且仍未完成的那个：它只在自己完成时前进，
// 不会因别的文件开始而被顶掉，所以抽屉里的文件名单调推进、不跳动。
func (t *Task) FileStart(i int) {
	t.mu.Lock()
	if f := t.at(i); f != nil {
		t.setStateLocked(f, FileRunning)
		f.Err = ""
		t.active = append(t.active, i)
	}
	t.mu.Unlock()
}

// FileSkip 记为「已跳过」：目标已有同名同大小，断点续传直接略过。
// 字节数由调用方经 AddFile 计入总进度，这里只收口状态。
func (t *Task) FileSkip(i int) {
	t.mu.Lock()
	if f := t.at(i); f != nil {
		t.setStateLocked(f, FileSkipped)
		f.Done = f.Size
	}
	t.mu.Unlock()
}

// FileDone 结束第 i 个文件：成功记完成；被取消回落「等待」（进度作废，重试要重传）；
// 其余记失败并留下人话原因。
func (t *Task) FileDone(i int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.at(i)
	if f == nil {
		return
	}
	t.dropActiveLocked(i)
	switch {
	case err == nil:
		t.setStateLocked(f, FileDone)
		f.Done = f.Size
	case errors.Is(err, context.Canceled):
		t.setStateLocked(f, FilePending)
		f.Done = 0
	default:
		t.setStateLocked(f, FileError)
		f.Err = util.Humanize(err)
	}
}

// SetFile 把清单置为唯一一个在途文件——离线下载等全程单文件的任务用。
func (t *Task) SetFile(name string) {
	t.SetFiles([]model.TransferFile{{Path: name}})
	t.FileStart(0)
}

// AddFile 第 i 个文件新增 n 字节（负数=失败重试前回退），同时累加任务总进度。
func (t *Task) AddFile(i int, n int64) {
	t.mu.Lock()
	if f := t.at(i); f != nil {
		f.Done += n
		if f.Done < 0 {
			f.Done = 0
		}
	}
	t.addLocked(n)
	t.mu.Unlock()
}

// Add 累加已完成字节（可为负：文件重试时回退进度）。有在途文件时一并记到最早那个，
// 让单文件任务（离线下载）的清单也有进度。
func (t *Task) Add(n int64) {
	t.mu.Lock()
	if len(t.active) > 0 {
		if f := t.at(t.active[0]); f != nil {
			f.Done += n
		}
	}
	t.addLocked(n)
	t.mu.Unlock()
}

// addLocked 累加总进度并每 ≥500ms 重算一次速度，须持 t.mu 调用。
func (t *Task) addLocked(n int64) {
	t.Done += n
	now := time.Now()
	if t.lastT.IsZero() {
		t.lastT, t.lastDone = now, t.Done
	} else if d := now.Sub(t.lastT); d >= 500*time.Millisecond {
		t.Speed = (t.Done - t.lastDone) * int64(time.Second) / int64(d)
		t.lastT, t.lastDone = now, t.Done
	}
}

// at 取清单第 i 项，越界返回 nil（上报方与清单错位时不至于 panic）；须持 t.mu。
func (t *Task) at(i int) *FileProgress {
	if i < 0 || i >= len(t.files) {
		return nil
	}
	return t.files[i]
}

// setStateLocked 迁移文件状态并同步计数，须持 t.mu。
func (t *Task) setStateLocked(f *FileProgress, st FileState) {
	t.Files.add(f.State, -1)
	f.State = st
	t.Files.add(st, 1)
}

// dropActiveLocked 把下标 i 移出在途集合，须持 t.mu。
func (t *Task) dropActiveLocked(i int) {
	for k, v := range t.active {
		if v == i {
			t.active = append(t.active[:k], t.active[k+1:]...)
			return
		}
	}
}

// endFilesLocked 收口清单：任务结束时仍挂着「传输中」的文件按任务结局归位，
// 不留一个永远在传的僵尸行（正常路径由 fs 逐文件上报，这里是兜底）。须持 t.mu。
func (t *Task) endFilesLocked(st FileState) {
	for _, f := range t.files {
		if f.State == FileRunning {
			t.setStateLocked(f, st)
			switch {
			case st == FileDone:
				f.Done = f.Size
			case st == FileError && f.Err == "": // 清单里不留「失败」却没原因的行
				f.Err = "任务已中止，该文件未传完"
			}
		}
	}
	t.active = nil
}

// snapshot 返回公开字段的深拷贝（避免 handler 序列化时与 worker 竞态）。
// 清单本身不进快照——几万条不该每轮轮询都拷一遍，要看走 Manager.Files 分页取。
func (t *Task) snapshot() *Task {
	t.mu.Lock()
	defer t.mu.Unlock()
	cur := ""
	if len(t.active) > 0 {
		if f := t.at(t.active[0]); f != nil {
			cur = path.Base(f.Path)
		}
	}
	return &Task{
		ID: t.ID, Name: t.Name, Group: t.Group, Owner: t.Owner, State: t.State,
		Total: t.Total, Done: t.Done, Speed: t.Speed, Files: t.Files,
		CurFile: cur, Active: len(t.active), Err: t.Err, CreatedAt: t.CreatedAt,
	}
}

// group 一组任务的独立队列与 worker 池；quits 每 worker 一个，收缩=关最后一个。
type group struct {
	queue chan *Task
	quits []chan struct{}
}

type Manager struct {
	mu     sync.Mutex
	tasks  map[string]*Task
	groups map[string]*group
}

// New 创建管理器并初始化 copy 组；其他组按需 SetWorkers/SubmitIn 时自动建立。
func New(copyWorkers int) *Manager {
	m := &Manager{tasks: map[string]*Task{}, groups: map[string]*group{}}
	m.SetWorkers(GroupCopy, copyWorkers)
	return m
}

// SetWorkers 把组的 worker 数调到 n（钳到 1..32），组不存在则创建。
// 收缩时多余 worker 做完手头任务才退出，不打断运行中任务；可运行时随时调用。
func (m *Manager) SetWorkers(groupName string, n int) {
	if n < 1 {
		n = 1
	}
	if n > maxWorkers {
		n = maxWorkers
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resizeLocked(groupName, n)
}

// Workers 返回组当前 worker 数（组不存在为 0）。
func (m *Manager) Workers(groupName string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g := m.groups[groupName]; g != nil {
		return len(g.quits)
	}
	return 0
}

// resizeLocked 建组/扩缩 worker，须持有 m.mu 调用。
func (m *Manager) resizeLocked(groupName string, n int) *group {
	g := m.groups[groupName]
	if g == nil {
		g = &group{queue: make(chan *Task, 256)}
		m.groups[groupName] = g
	}
	for len(g.quits) < n {
		quit := make(chan struct{})
		g.quits = append(g.quits, quit)
		go m.worker(g, quit)
	}
	for len(g.quits) > n {
		last := len(g.quits) - 1
		close(g.quits[last])
		g.quits = g.quits[:last]
	}
	return g
}

// worker 先查 quit 再取任务：保证收缩后多余 worker 即使队列积压也会退出。
func (m *Manager) worker(g *group, quit chan struct{}) {
	for {
		select {
		case <-quit:
			return
		default:
		}
		select {
		case <-quit:
			return
		case t := <-g.queue:
			m.run(t)
		}
	}
}

func (m *Manager) run(t *Task) {
	t.mu.Lock()
	if t.State != StatePending { // 入队后被取消
		t.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.State = StateRunning
	t.cancel = cancel
	fn := t.fn
	t.mu.Unlock()
	defer cancel()

	err := runTaskFn(ctx, t, fn)

	t.mu.Lock()
	t.cancel = nil
	t.Speed = 0
	switch {
	case err == nil:
		t.State = StateDone
		t.Done = t.Total
		t.endFilesLocked(FileDone)
	case errors.Is(err, context.Canceled) || ctx.Err() != nil:
		t.State = StateCanceled
		t.endFilesLocked(FilePending) // 取消=没传完，回落等待，重试重传
	default:
		t.State = StateError
		t.Err = util.Humanize(err)
		t.endFilesLocked(FileError)
		log.Printf("[task] 任务 %s 失败: %v", t.ID, err)
	}
	t.mu.Unlock()
}

// runTaskFn 执行任务体并捕获 panic，转为普通错误——后台 worker goroutine 的 panic
// 不受 gin.Recovery 覆盖，未捕获会整进程崩溃（单个任务 bug = 全站 DoS）。
func runTaskFn(ctx context.Context, t *Task, fn Func) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("任务内部错误: %v", r)
			log.Printf("[task] 任务 %s panic: %v\n%s", t.ID, r, debug.Stack())
		}
	}()
	return fn(ctx, t)
}

// enqueue 非阻塞入队：队列满（积压 >256）时不阻塞提交请求，直接把任务标记为失败，
// 由用户重试；避免离线一次提交大量 URL 时 HTTP goroutine 卡在 channel 发送上。
func enqueue(g *group, t *Task) {
	select {
	case g.queue <- t:
	default:
		t.mu.Lock()
		t.State = StateError
		t.Err = "任务队列已满，请稍后重试"
		t.mu.Unlock()
	}
}

// Submit 提交到 copy 组（兼容旧调用方）。
func (m *Manager) Submit(owner int64, name string, fn Func) *Task {
	return m.SubmitIn(GroupCopy, owner, name, fn)
}

// SubmitIn 提交到指定组；组若从未配置过，按 1 worker 兜底创建。
func (m *Manager) SubmitIn(groupName string, owner int64, name string, fn Func) *Task {
	t := &Task{
		ID:        uuid.NewString(),
		Name:      name,
		Group:     groupName,
		Owner:     owner,
		State:     StatePending,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		fn:        fn,
	}
	m.mu.Lock()
	g := m.groups[groupName]
	if g == nil {
		g = m.resizeLocked(groupName, 1)
	}
	m.tasks[t.ID] = t
	m.mu.Unlock()
	enqueue(g, t)
	return t
}

// List 返回快照数组：admin 全量，否则仅本人；按创建时间倒序（同秒按 ID 稳定排序）。
func (m *Manager) List(owner int64, isAdmin bool) []*Task {
	m.mu.Lock()
	all := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		all = append(all, t)
	}
	m.mu.Unlock()
	out := make([]*Task, 0, len(all))
	for _, t := range all {
		s := t.snapshot()
		if isAdmin || s.Owner == owner {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// FilesQuery 是文件清单的查询条件：按状态筛、按路径搜、分页取。
// 一个文件夹转存动辄几万条，接口只按需给一页。
type FilesQuery struct {
	State  FileState // 空 = 全部状态
	Q      string    // 路径子串，不区分大小写；空 = 不过滤
	Offset int
	Limit  int // ≤0 取默认 200，上限 1000
}

// FilesPage 是一页文件清单：Items 为过滤+分页后的条目，Total 为过滤后总数，
// Counts 是不受过滤影响的全量计数（界面上那排「等待/传输中/完成/跳过/失败」）。
type FilesPage struct {
	Items  []FileProgress `json:"items"`
	Total  int            `json:"total"`
	Counts FileCounts     `json:"counts"`
}

const (
	filesDefaultLimit = 200
	filesMaxLimit     = 1000
)

// Files 取任务的文件清单一页（admin 或任务所有者可查）。
func (m *Manager) Files(id string, owner int64, isAdmin bool, q FilesQuery) (*FilesPage, error) {
	t, err := m.find(id, owner, isAdmin)
	if err != nil {
		return nil, err
	}
	if q.Limit <= 0 {
		q.Limit = filesDefaultLimit
	}
	if q.Limit > filesMaxLimit {
		q.Limit = filesMaxLimit
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	needle := strings.ToLower(q.Q)

	t.mu.Lock()
	defer t.mu.Unlock()
	page := &FilesPage{Items: []FileProgress{}, Counts: t.Files}
	for _, f := range t.files {
		if q.State != "" && f.State != q.State {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(f.Path), needle) {
			continue
		}
		page.Total++
		if page.Total <= q.Offset || len(page.Items) >= q.Limit {
			continue // 仍要数完总数，用于分页器
		}
		page.Items = append(page.Items, *f)
	}
	return page, nil
}

func (m *Manager) Get(id string) (*Task, bool) {
	m.mu.Lock()
	t, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	return t.snapshot(), true
}

// find 带权限检查取原始任务对象。
func (m *Manager) find(id string, owner int64, isAdmin bool) (*Task, error) {
	m.mu.Lock()
	t, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	t.mu.Lock()
	own := t.Owner
	t.mu.Unlock()
	if !isAdmin && own != owner {
		return nil, ErrForbidden
	}
	return t, nil
}

func (m *Manager) Cancel(id string, owner int64, isAdmin bool) error {
	t, err := m.find(id, owner, isAdmin)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch t.State {
	case StateRunning:
		if t.cancel != nil {
			t.cancel()
		}
	case StatePending:
		t.State = StateCanceled // run() 取到后见非 pending 直接跳过
	default:
		return ErrBadState
	}
	return nil
}

func (m *Manager) Retry(id string, owner int64, isAdmin bool) error {
	t, err := m.find(id, owner, isAdmin)
	if err != nil {
		return err
	}
	t.mu.Lock()
	if t.State != StateError && t.State != StateCanceled {
		t.mu.Unlock()
		return ErrBadState
	}
	t.State = StatePending
	t.Done, t.Speed, t.Total = 0, 0, 0
	t.Err, t.active = "", nil
	t.files, t.Files = nil, FileCounts{} // 清单由重跑的任务体重新规划
	t.lastT, t.lastDone = time.Time{}, 0
	t.mu.Unlock()
	m.mu.Lock()
	g := m.groups[t.Group] // Group 创建后不变，组必然存在
	m.mu.Unlock()
	enqueue(g, t)
	return nil
}

// ClearDone 只删除已成功（done）任务：admin 清全部用户，否则仅本人名下。
// 失败/已取消任务刻意保留，便于「重试」；要移除单个非成功任务用 Remove。
func (m *Manager) ClearDone(owner int64, isAdmin bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, t := range m.tasks {
		t.mu.Lock()
		succeeded := t.State == StateDone
		own := t.Owner
		t.mu.Unlock()
		if succeeded && (isAdmin || own == owner) {
			delete(m.tasks, id)
		}
	}
}

// Remove 删除单个终态任务（done/error/canceled）；运行中/等待中须先取消。
func (m *Manager) Remove(id string, owner int64, isAdmin bool) error {
	t, err := m.find(id, owner, isAdmin)
	if err != nil {
		return err
	}
	t.mu.Lock()
	terminal := t.State == StateDone || t.State == StateError || t.State == StateCanceled
	t.mu.Unlock()
	if !terminal {
		return ErrBadState
	}
	m.mu.Lock()
	delete(m.tasks, id)
	m.mu.Unlock()
	return nil
}
