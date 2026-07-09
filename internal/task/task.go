// Package task 提供内存态后台任务管理：worker pool + 状态机 + 结构化进度。
// v1 为内存实现，重启后任务列表丢失（转存本身幂等，可重发起）。
package task

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
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

type Task struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Group     string `json:"group"` // 所属任务组，创建后不变
	Owner     int64  `json:"owner"`
	State     State  `json:"state"`
	Total     int64  `json:"total"`
	Done      int64  `json:"done"`
	Speed     int64  `json:"speed"` // B/s
	CurFile   string `json:"cur_file"`
	Err       string `json:"error"`
	CreatedAt string `json:"created_at"`

	mu       sync.Mutex
	cancel   context.CancelFunc
	fn       Func
	lastT    time.Time
	lastDone int64
}

// SetTotal / SetFile / Add 结构化满足 fs.Progress 接口（鸭子类型，无需包间 import）。

func (t *Task) SetTotal(n int64) {
	t.mu.Lock()
	t.Total = n
	t.mu.Unlock()
}

func (t *Task) SetFile(name string) {
	t.mu.Lock()
	t.CurFile = name
	t.mu.Unlock()
}

// Add 累加已完成字节（可为负：文件重试时回退进度），每 ≥500ms 重算一次速度。
func (t *Task) Add(n int64) {
	t.mu.Lock()
	t.Done += n
	now := time.Now()
	if t.lastT.IsZero() {
		t.lastT, t.lastDone = now, t.Done
	} else if d := now.Sub(t.lastT); d >= 500*time.Millisecond {
		t.Speed = (t.Done - t.lastDone) * int64(time.Second) / int64(d)
		t.lastT, t.lastDone = now, t.Done
	}
	t.mu.Unlock()
}

// snapshot 返回公开字段的深拷贝（避免 handler 序列化时与 worker 竞态）。
func (t *Task) snapshot() *Task {
	t.mu.Lock()
	defer t.mu.Unlock()
	return &Task{
		ID: t.ID, Name: t.Name, Group: t.Group, Owner: t.Owner, State: t.State,
		Total: t.Total, Done: t.Done, Speed: t.Speed,
		CurFile: t.CurFile, Err: t.Err, CreatedAt: t.CreatedAt,
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
	case errors.Is(err, context.Canceled) || ctx.Err() != nil:
		t.State = StateCanceled
	default:
		t.State = StateError
		t.Err = err.Error()
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
	t.Err, t.CurFile = "", ""
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
