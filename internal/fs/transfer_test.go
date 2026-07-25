package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newlist/internal/driver"
	"newlist/internal/driver/local"
	"newlist/internal/model"
	"newlist/internal/user"
)

// fakeProgress 记录进度回调（测试断言用）。
type fakeProgress struct {
	mu     sync.Mutex
	total  int64
	done   int64
	files  []string // 曾开始复制的文件，按开始先后
	active int      // 当前在途数，任务收尾必须归零
}

func (p *fakeProgress) SetTotal(n int64) { p.mu.Lock(); p.total = n; p.mu.Unlock() }

func (p *fakeProgress) FileStart(s string) {
	p.mu.Lock()
	p.files = append(p.files, s)
	p.active++
	p.mu.Unlock()
}

func (p *fakeProgress) FileDone(string) { p.mu.Lock(); p.active--; p.mu.Unlock() }
func (p *fakeProgress) Add(n int64)     { p.mu.Lock(); p.done += n; p.mu.Unlock() }

func adminUser() *user.User {
	return &user.User{ID: 1, Username: "admin", Role: "admin", BasePath: "/", CanWrite: true, Enabled: true}
}

// newLocalMount 起一个真实 local 驱动挂载。
func newLocalMount(t *testing.T, id int64, mountPath, rootDir string) *Mount {
	t.Helper()
	d := &local.Local{}
	if err := d.Init(context.Background(), driver.Config{"root_path": rootDir}); err != nil {
		t.Fatalf("local Init: %v", err)
	}
	t.Cleanup(func() { d.Drop() })
	return &Mount{ID: id, Path: mountPath, Driver: "local", Enabled: true, drv: d}
}

// newTestFS 组装带指定挂载的 FS（不经数据库）。
func newTestFS(mounts ...*Mount) *FS {
	f := &FS{}
	f.mounts = mounts
	return f
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSameStorage(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	f := newTestFS(
		newLocalMount(t, 1, "/存储A", dirA),
		newLocalMount(t, 2, "/存储B", dirB),
	)
	u := adminUser()

	same, up, err := f.SameStorage(u, "/存储A/x.txt", "/存储A")
	if err != nil || !same || !up {
		t.Fatalf("同存储判定错: same=%v up=%v err=%v", same, up, err)
	}
	same, up, err = f.SameStorage(u, "/存储A/x.txt", "/存储B")
	if err != nil || same || !up {
		t.Fatalf("跨存储判定错: same=%v up=%v err=%v", same, up, err)
	}
	if _, _, err = f.SameStorage(u, "/不存在/x", "/存储B"); err == nil {
		t.Fatal("未挂载路径应报错")
	}
}

func TestTransferSingleFileCopy(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(dirA, "说明.md"), "hello 转存")
	f := newTestFS(
		newLocalMount(t, 1, "/存储A", dirA),
		newLocalMount(t, 2, "/存储B", dirB),
	)
	pr := &fakeProgress{}
	err := f.Transfer(context.Background(), adminUser(), "/存储A/说明.md", "/存储B", false, pr)
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dirB, "说明.md"))
	if err != nil || string(got) != "hello 转存" {
		t.Fatalf("目标内容不符: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dirA, "说明.md")); err != nil {
		t.Fatal("copy 后源文件应保留")
	}
	if pr.total != int64(len("hello 转存")) || pr.done != pr.total {
		t.Fatalf("进度不符: total=%d done=%d", pr.total, pr.done)
	}
}

func TestTransferDirTreeCopy(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(dirA, "相册", "a.jpg"), "AAAA")
	writeFile(t, filepath.Join(dirA, "相册", "子集", "b.jpg"), "BBBBBB")
	writeFile(t, filepath.Join(dirA, "相册", "子集", "c.txt"), "C")
	f := newTestFS(
		newLocalMount(t, 1, "/存储A", dirA),
		newLocalMount(t, 2, "/存储B", dirB),
	)
	if mkErr := os.MkdirAll(filepath.Join(dirB, "备份"), 0o755); mkErr != nil {
		t.Fatal(mkErr)
	}
	pr := &fakeProgress{}
	if err := f.Transfer(context.Background(), adminUser(), "/存储A/相册", "/存储B/备份", false, pr); err != nil {
		t.Fatalf("Transfer 目录树: %v", err)
	}
	for p, want := range map[string]string{
		filepath.Join(dirB, "备份", "相册", "a.jpg"):       "AAAA",
		filepath.Join(dirB, "备份", "相册", "子集", "b.jpg"): "BBBBBB",
		filepath.Join(dirB, "备份", "相册", "子集", "c.txt"): "C",
	} {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Fatalf("%s 内容不符: %q err=%v", p, got, err)
		}
	}
	if pr.total != 11 || pr.done != 11 {
		t.Fatalf("目录树进度不符: total=%d done=%d", pr.total, pr.done)
	}
}

// TestTransferResumeSkipsExisting 断点续传：目标已存在且大小一致的文件被跳过（不重复复制），
// 大小不一致或缺失的文件照常复制。模拟中途失败后点「重试」不重传已完成文件。
func TestTransferResumeSkipsExisting(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(dirA, "相册", "a.jpg"), "AAAA") // 4 字节
	writeFile(t, filepath.Join(dirA, "相册", "b.jpg"), "BBBB") // 4 字节
	writeFile(t, filepath.Join(dirA, "相册", "c.jpg"), "CCCC") // 4 字节
	// 预置目标：a.jpg 同大小(4)不同内容→应跳过；c.jpg 大小不符(2)→应重新复制覆盖。
	writeFile(t, filepath.Join(dirB, "相册", "a.jpg"), "ZZZZ")
	writeFile(t, filepath.Join(dirB, "相册", "c.jpg"), "ZZ")
	f := newTestFS(
		newLocalMount(t, 1, "/存储A", dirA),
		newLocalMount(t, 2, "/存储B", dirB),
	)
	pr := &fakeProgress{}
	if err := f.Transfer(context.Background(), adminUser(), "/存储A/相册", "/存储B", false, pr); err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	// a.jpg 跳过 → 仍是预置的 ZZZZ
	if got, _ := os.ReadFile(filepath.Join(dirB, "相册", "a.jpg")); string(got) != "ZZZZ" {
		t.Fatalf("a.jpg 应被跳过(保持 ZZZZ)，实际 %q", got)
	}
	// b.jpg 缺失 → 复制
	if got, _ := os.ReadFile(filepath.Join(dirB, "相册", "b.jpg")); string(got) != "BBBB" {
		t.Fatalf("b.jpg 应被复制为 BBBB，实际 %q", got)
	}
	// c.jpg 大小不符 → 重新复制覆盖
	if got, _ := os.ReadFile(filepath.Join(dirB, "相册", "c.jpg")); string(got) != "CCCC" {
		t.Fatalf("c.jpg 应被覆盖为 CCCC，实际 %q", got)
	}
	// 进度：三个文件各 4 字节都计入（跳过的也 Add 其大小），共 12
	if pr.total != 12 || pr.done != 12 {
		t.Fatalf("进度不符: total=%d done=%d（应 12/12）", pr.total, pr.done)
	}
	// 只有 b、c 真正走复制（FileStart 上报），a 被跳过不上报
	if len(pr.files) != 2 {
		t.Fatalf("应仅 2 个文件走复制(b/c)，FileStart 次数=%d: %v", len(pr.files), pr.files)
	}
	// 每个 FileStart 都有配对的 FileDone，收尾不留在途项
	if pr.active != 0 {
		t.Fatalf("收尾在途文件数应为 0，实际 %d", pr.active)
	}
}

func TestTransferMove(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(dirA, "搬家", "x.bin"), strings.Repeat("x", 1024))
	f := newTestFS(
		newLocalMount(t, 1, "/存储A", dirA),
		newLocalMount(t, 2, "/存储B", dirB),
	)
	pr := &fakeProgress{}
	if err := f.Transfer(context.Background(), adminUser(), "/存储A/搬家", "/存储B", true, pr); err != nil {
		t.Fatalf("Transfer move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirB, "搬家", "x.bin")); err != nil {
		t.Fatal("目标应出现搬家/x.bin")
	}
	if _, err := os.Stat(filepath.Join(dirA, "搬家")); !os.IsNotExist(err) {
		t.Fatal("move 后源目录应被删除")
	}
}

// cancelingDriver 的读取流在读出首块后触发取消，模拟"复制进行中用户点取消"。
type cancelingDriver struct {
	cancel context.CancelFunc
	size   int
}

func (d *cancelingDriver) Init(context.Context, driver.Config) error { return nil }
func (d *cancelingDriver) Drop() error                               { return nil }
func (d *cancelingDriver) List(context.Context, string) ([]model.FileInfo, error) {
	return nil, driver.ErrNotFound
}
func (d *cancelingDriver) Stat(context.Context, string) (model.FileInfo, error) {
	return model.FileInfo{Name: "f.bin", Size: int64(d.size)}, nil
}
func (d *cancelingDriver) Link(context.Context, string) (*driver.Link, error) {
	return &driver.Link{Local: &cancelOnReadReader{cancel: d.cancel, remain: d.size}}, nil
}

type cancelOnReadReader struct {
	cancel context.CancelFunc
	remain int
}

func (r *cancelOnReadReader) Read(p []byte) (int, error) {
	if r.remain <= 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remain, 1024)
	r.remain -= n
	r.cancel() // 首块读出后取消，后续 Read 应被 countingReader 拦下
	return n, nil
}
func (r *cancelOnReadReader) Seek(int64, int) (int64, error) { return 0, nil }
func (r *cancelOnReadReader) Close() error                   { return nil }

func TestTransferCancelMidCopy(t *testing.T) {
	dirB := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &cancelingDriver{cancel: cancel, size: 1 << 20}
	f := newTestFS(
		&Mount{ID: 1, Path: "/慢源", Driver: "slow", Enabled: true, drv: src},
		newLocalMount(t, 2, "/存储B", dirB),
	)
	err := f.Transfer(ctx, adminUser(), "/慢源/f.bin", "/存储B", false, &fakeProgress{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("中途取消应返回 context.Canceled，得到 %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dirB, "f.bin")); !os.IsNotExist(statErr) {
		t.Fatal("取消后目标不应有成品文件")
	}
}

func TestTransferCancel(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(dirA, "big.bin"), strings.Repeat("z", 1<<20))
	f := newTestFS(
		newLocalMount(t, 1, "/存储A", dirA),
		newLocalMount(t, 2, "/存储B", dirB),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	err := f.Transfer(ctx, adminUser(), "/存储A/big.bin", "/存储B", false, &fakeProgress{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，得到 %v", err)
	}
}

// ---- 目标不可写 / 文件级重试：用最小 fake 驱动 ----

// roDriver 只读驱动（无 Writer/Uploader）。
type roDriver struct{}

func (roDriver) Init(context.Context, driver.Config) error { return nil }
func (roDriver) Drop() error                               { return nil }
func (roDriver) List(context.Context, string) ([]model.FileInfo, error) {
	return nil, driver.ErrNotFound
}
func (roDriver) Stat(context.Context, string) (model.FileInfo, error) {
	return model.FileInfo{}, driver.ErrNotFound
}
func (roDriver) Link(context.Context, string) (*driver.Link, error) {
	return nil, driver.ErrNotFound
}

func TestTransferDstNotUploadable(t *testing.T) {
	dirA := t.TempDir()
	writeFile(t, filepath.Join(dirA, "a.txt"), "A")
	f := newTestFS(
		newLocalMount(t, 1, "/存储A", dirA),
		&Mount{ID: 2, Path: "/只读", Driver: "ro", Enabled: true, drv: roDriver{}},
	)
	u := adminUser()
	same, up, err := f.SameStorage(u, "/存储A/a.txt", "/只读")
	if err != nil || same || up {
		t.Fatalf("只读目标判定错: same=%v up=%v err=%v", same, up, err)
	}
	err = f.Transfer(context.Background(), u, "/存储A/a.txt", "/只读", false, &fakeProgress{})
	if err == nil || !strings.Contains(err.Error(), "不支持写入") {
		t.Fatalf("期望不支持写入错误，得到 %v", err)
	}
}

// flakyDriver 源驱动：前 failN 次 Link 打开后读到一半失败，用于覆盖重试路径。
type flakyDriver struct {
	content string
	failN   int
	calls   int
}

func (d *flakyDriver) Init(context.Context, driver.Config) error { return nil }
func (d *flakyDriver) Drop() error                               { return nil }
func (d *flakyDriver) List(context.Context, string) ([]model.FileInfo, error) {
	return nil, driver.ErrNotFound
}
func (d *flakyDriver) Stat(_ context.Context, rel string) (model.FileInfo, error) {
	return model.FileInfo{Name: "f.txt", Size: int64(len(d.content))}, nil
}
func (d *flakyDriver) Link(_ context.Context, rel string) (*driver.Link, error) {
	d.calls++
	if d.calls <= d.failN {
		return &driver.Link{Local: &failingReader{data: d.content[:len(d.content)/2]}}, nil
	}
	return &driver.Link{Local: &fullReader{Reader: strings.NewReader(d.content)}}, nil
}

// failingReader 读出一半后返回错误。
type failingReader struct {
	data string
	off  int
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, errors.New("模拟网络中断")
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
func (r *failingReader) Seek(offset int64, whence int) (int64, error) { return 0, nil }
func (r *failingReader) Close() error                                 { return nil }

type fullReader struct {
	*strings.Reader
}

func (fullReader) Close() error { return nil }

func TestTransferFileRetry(t *testing.T) {
	dirB := t.TempDir()
	src := &flakyDriver{content: "重试后成功的内容", failN: 1}
	f := newTestFS(
		&Mount{ID: 1, Path: "/坏源", Driver: "flaky", Enabled: true, drv: src},
		newLocalMount(t, 2, "/存储B", dirB),
	)
	pr := &fakeProgress{}
	if err := f.Transfer(context.Background(), adminUser(), "/坏源/f.txt", "/存储B", false, pr); err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dirB, "f.txt"))
	if err != nil || string(got) != src.content {
		t.Fatalf("目标内容不符: %q err=%v", got, err)
	}
	// 进度应等于文件大小（失败尝试的字节被回退）
	if pr.done != int64(len(src.content)) {
		t.Fatalf("重试后进度未回退干净: done=%d want=%d", pr.done, len(src.content))
	}
	if src.calls != 2 {
		t.Fatalf("Link 应被调 2 次，实际 %d", src.calls)
	}
}

func TestTransferRetryExhausted(t *testing.T) {
	dirB := t.TempDir()
	src := &flakyDriver{content: "永远失败", failN: 99}
	f := newTestFS(
		&Mount{ID: 1, Path: "/坏源", Driver: "flaky", Enabled: true, drv: src},
		newLocalMount(t, 2, "/存储B", dirB),
	)
	pr := &fakeProgress{}
	err := f.Transfer(context.Background(), adminUser(), "/坏源/f.txt", "/存储B", false, pr)
	if err == nil || !strings.Contains(err.Error(), "已重试 2 次") {
		t.Fatalf("期望重试耗尽错误，得到 %v", err)
	}
	if src.calls != 3 {
		t.Fatalf("Link 应被调 3 次（1+2 重试），实际 %d", src.calls)
	}
	if pr.done != 0 {
		t.Fatalf("全部失败后进度应回退到 0，实际 %d", pr.done)
	}
}

var _ io.ReadSeekCloser = (*failingReader)(nil)

// ---- 文件夹内文件并发复制 ----

func probeContent(i int) string { return fmt.Sprintf("probe-data-%d-%s", i, strings.Repeat("x", i+1)) }

// concProbeDriver 源驱动：目录 "d" 下 n 个文件，每个 Link 出的读流首块 sleep 一小段，
// 期间记录当前并发数与峰值——用于验证「文件夹内文件并发复制」确有并行。
type concProbeDriver struct {
	n    int
	cur  atomic.Int32
	peak atomic.Int32
}

func (d *concProbeDriver) Init(context.Context, driver.Config) error { return nil }
func (d *concProbeDriver) Drop() error                               { return nil }
func (d *concProbeDriver) List(_ context.Context, rel string) ([]model.FileInfo, error) {
	if rel != "d" {
		return nil, driver.ErrNotFound
	}
	out := make([]model.FileInfo, d.n)
	for i := 0; i < d.n; i++ {
		out[i] = model.FileInfo{Name: fmt.Sprintf("f%d.bin", i), Size: int64(len(probeContent(i)))}
	}
	return out, nil
}
func (d *concProbeDriver) Stat(_ context.Context, rel string) (model.FileInfo, error) {
	if rel == "d" {
		return model.FileInfo{Name: "d", IsDir: true}, nil
	}
	return model.FileInfo{}, driver.ErrNotFound // 转存只对源根 Stat 一次；文件走 List 结果
}
func (d *concProbeDriver) Link(_ context.Context, rel string) (*driver.Link, error) {
	name := rel
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		name = rel[i+1:]
	}
	var idx int
	fmt.Sscanf(name, "f%d.bin", &idx)
	return &driver.Link{Local: &probeReader{d: d, data: probeContent(idx)}}, nil
}

type probeReader struct {
	d       *concProbeDriver
	data    string
	off     int
	entered bool
}

func (r *probeReader) Read(p []byte) (int, error) {
	if !r.entered {
		r.entered = true
		cur := r.d.cur.Add(1)
		for { // peak = max(peak, cur)
			peak := r.d.peak.Load()
			if cur <= peak || r.d.peak.CompareAndSwap(peak, cur) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond) // 拉长窗口让多文件重叠
	}
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
func (r *probeReader) Seek(int64, int) (int64, error) { return 0, nil }
func (r *probeReader) Close() error {
	if r.entered {
		r.d.cur.Add(-1)
	}
	return nil
}

func TestTransferParallelFiles(t *testing.T) {
	// 并行：SetCopyFileWorkers(4) 复制含 6 个文件的目录，并发峰值应 >1。
	dirB := t.TempDir()
	src := &concProbeDriver{n: 6}
	f := newTestFS(
		&Mount{ID: 1, Path: "/并发源", Driver: "probe", Enabled: true, drv: src},
		newLocalMount(t, 2, "/存储B", dirB),
	)
	f.SetCopyFileWorkers(4)
	pr := &fakeProgress{}
	if err := f.Transfer(context.Background(), adminUser(), "/并发源/d", "/存储B", false, pr); err != nil {
		t.Fatalf("Transfer 并发: %v", err)
	}
	for i := 0; i < src.n; i++ {
		name := fmt.Sprintf("f%d.bin", i)
		got, err := os.ReadFile(filepath.Join(dirB, "d", name))
		if err != nil || string(got) != probeContent(i) {
			t.Fatalf("%s 内容不符: %q err=%v", name, got, err)
		}
	}
	if peak := src.peak.Load(); peak < 2 {
		t.Fatalf("SetCopyFileWorkers(4) 下并发峰值应 >1，实际 %d（=未并行）", peak)
	}

	// 串行对照：SetCopyFileWorkers(1) 时峰值必为 1（等价旧行为）。
	dirC := t.TempDir()
	src2 := &concProbeDriver{n: 4}
	f2 := newTestFS(
		&Mount{ID: 1, Path: "/并发源", Driver: "probe", Enabled: true, drv: src2},
		newLocalMount(t, 2, "/存储C", dirC),
	)
	f2.SetCopyFileWorkers(1)
	if err := f2.Transfer(context.Background(), adminUser(), "/并发源/d", "/存储C", false, &fakeProgress{}); err != nil {
		t.Fatalf("Transfer 串行: %v", err)
	}
	if peak := src2.peak.Load(); peak != 1 {
		t.Fatalf("SetCopyFileWorkers(1) 下并发峰值应=1，实际 %d", peak)
	}
}

// stableProgress 按任务层的展示规则（取最早在途的文件）记录展示值的变化序列。
type stableProgress struct {
	mu     sync.Mutex
	active []string
	seq    []string // 展示值每次变化后的取值，连续相同只记一次
	peak   int
}

func (p *stableProgress) SetTotal(int64) {}
func (p *stableProgress) Add(int64)      {}

func (p *stableProgress) FileStart(name string) {
	p.mu.Lock()
	p.active = append(p.active, name)
	p.record()
	p.mu.Unlock()
}

func (p *stableProgress) FileDone(name string) {
	p.mu.Lock()
	for i, n := range p.active {
		if n == name {
			p.active = append(p.active[:i], p.active[i+1:]...)
			break
		}
	}
	p.record()
	p.mu.Unlock()
}

// record 调用方须持锁。
func (p *stableProgress) record() {
	if len(p.active) > p.peak {
		p.peak = len(p.active)
	}
	cur := ""
	if len(p.active) > 0 {
		cur = p.active[0]
	}
	if len(p.seq) == 0 || p.seq[len(p.seq)-1] != cur {
		p.seq = append(p.seq, cur)
	}
}

// TestTransferParallelCurFileStable 真实并发路径下的展示稳定性：
// 每个文件名在展示序列里最多出现一次——出现两次即意味着展示曾被别的文件顶掉又切回来，
// 也就是抽屉里看到的「文件名在几个文件之间跳」。
func TestTransferParallelCurFileStable(t *testing.T) {
	dirB := t.TempDir()
	src := &concProbeDriver{n: 6}
	f := newTestFS(
		&Mount{ID: 1, Path: "/并发源", Driver: "probe", Enabled: true, drv: src},
		newLocalMount(t, 2, "/存储B", dirB),
	)
	f.SetCopyFileWorkers(4)
	pr := &stableProgress{}
	if err := f.Transfer(context.Background(), adminUser(), "/并发源/d", "/存储B", false, pr); err != nil {
		t.Fatalf("Transfer 并发: %v", err)
	}
	if pr.peak < 2 {
		t.Fatalf("在途峰值应 >1（否则没测到并发），实际 %d", pr.peak)
	}
	if len(pr.active) != 0 {
		t.Fatalf("收尾在途应为空，实际 %v", pr.active)
	}
	seen := map[string]bool{}
	for _, name := range pr.seq {
		if name == "" { // 在途短暂清空是正常的
			continue
		}
		if seen[name] {
			t.Fatalf("%s 在展示序列里出现两次（展示被顶掉又切回=跳动）: %v", name, pr.seq)
		}
		seen[name] = true
	}
	if len(seen) == 0 {
		t.Fatalf("展示序列没有任何文件名: %v", pr.seq)
	}
}
