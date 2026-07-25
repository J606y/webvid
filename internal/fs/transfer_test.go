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
	"newlist/internal/util"
)

// fakeProgress 记录进度回调（测试断言用）。plan 是规划清单，下标即上报用的 i。
type fakeProgress struct {
	mu      sync.Mutex
	total   int64
	done    int64
	plan    []model.TransferFile
	files   []string // 曾开始复制的文件（展示路径），按开始先后
	skipped []string // 断点续传跳过的文件
	errs    []string // 上报失败的文件
	active  int      // 当前在途数，任务收尾必须归零
}

func (p *fakeProgress) SetTotal(n int64) { p.mu.Lock(); p.total = n; p.mu.Unlock() }

func (p *fakeProgress) SetFiles(items []model.TransferFile) {
	p.mu.Lock()
	p.plan = items
	p.mu.Unlock()
}

// path 取第 i 项的展示路径，须持锁。
func (p *fakeProgress) path(i int) string {
	if i < 0 || i >= len(p.plan) {
		return ""
	}
	return p.plan[i].Path
}

func (p *fakeProgress) FileStart(i int) {
	p.mu.Lock()
	p.files = append(p.files, p.path(i))
	p.active++
	p.mu.Unlock()
}

func (p *fakeProgress) FileSkip(i int) {
	p.mu.Lock()
	p.skipped = append(p.skipped, p.path(i))
	p.mu.Unlock()
}

func (p *fakeProgress) FileDone(i int, err error) {
	p.mu.Lock()
	p.active--
	if err != nil {
		p.errs = append(p.errs, p.path(i))
	}
	p.mu.Unlock()
}

func (p *fakeProgress) AddFile(_ int, n int64) { p.mu.Lock(); p.done += n; p.mu.Unlock() }

// countingLocal 包装 local 驱动统计 Stat/List 次数，用来盯住断点续传的请求量——
// 云盘上每次 Stat 都可能是一整轮目录列举，回归时必须能看出请求数没被打回原形。
type countingLocal struct {
	*local.Local
	stats atomic.Int64
	lists atomic.Int64
}

func (d *countingLocal) Stat(ctx context.Context, rel string) (model.FileInfo, error) {
	d.stats.Add(1)
	return d.Local.Stat(ctx, rel)
}

func (d *countingLocal) List(ctx context.Context, rel string) ([]model.FileInfo, error) {
	d.lists.Add(1)
	return d.Local.List(ctx, rel)
}

// newCountingMount 与 newLocalMount 相同，但驱动会计数。
func newCountingMount(t *testing.T, id int64, mountPath, rootDir string) (*Mount, *countingLocal) {
	t.Helper()
	inner := &local.Local{}
	if err := inner.Init(context.Background(), driver.Config{"root_path": rootDir}); err != nil {
		t.Fatalf("local Init: %v", err)
	}
	t.Cleanup(func() { inner.Drop() })
	d := &countingLocal{Local: inner}
	return &Mount{ID: id, Path: mountPath, Driver: "local", Enabled: true, drv: d}, d
}

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
	// 测试不等真实退避，否则每个重试用例都要白等几秒；
	// 退避策略本身由 TestCopyRetryWait 单独盯着。
	f.retryBackoff = func(int, error) time.Duration { return 0 }
	return f
}

// TestCopyRetryWait 重试退避策略：限流要按限流的节奏等，网络抖动短暂让一下即可。
// 两者混为一谈的话，要么撞限流后等不够又去撞，要么普通抖动白等几十秒。
func TestCopyRetryWait(t *testing.T) {
	f := &FS{} // 不设 retryBackoff，走默认策略
	throttled := errors.New("上游错误：rateLimitExceeded(HTTP 429) Rate Limit Exceeded")
	ordinary := errors.New("connection reset by peer")

	cases := []struct {
		name    string
		attempt int
		err     error
		want    time.Duration
	}{
		{"限流首次", 1, throttled, 2 * time.Second},
		{"限流二次翻倍", 2, throttled, 4 * time.Second},
		{"普通错误首次", 1, ordinary, 1 * time.Second},
		{"普通错误二次", 2, ordinary, 2 * time.Second},
	}
	for _, c := range cases {
		if got := f.copyRetryWait(c.attempt, c.err); got != c.want {
			t.Errorf("%s: copyRetryWait = %v，应为 %v", c.name, got, c.want)
		}
	}
	if f.copyRetryWait(1, throttled) <= f.copyRetryWait(1, ordinary) {
		t.Error("限流退避必须明显长于普通错误，否则等于没区分")
	}
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

// TestTransferResumeScansOncePerDir 盯住断点续传的探测开销：目标目录只列一次，
// 之后判断全走内存。旧实现是每文件一次 Stat——在 googledrive/pikpak 上每次 Stat
// 都可能是一整轮目录列举，文件一多就慢到不可用。
func TestTransferResumeScansOncePerDir(t *testing.T) {
	const n, size = 12, 16
	dirA, dirB := t.TempDir(), t.TempDir()
	for i := 0; i < n; i++ {
		writeFile(t, filepath.Join(dirA, "相册", fmt.Sprintf("p%02d.jpg", i)), strings.Repeat("x", size))
	}
	dst, cnt := newCountingMount(t, 2, "/存储B", dirB)
	f := newTestFS(newLocalMount(t, 1, "/存储A", dirA), dst)

	if err := f.Transfer(context.Background(), adminUser(), "/存储A/相册", "/存储B", false, &fakeProgress{}); err != nil {
		t.Fatalf("首次复制: %v", err)
	}
	baseStats, baseLists := cnt.stats.Load(), cnt.lists.Load()

	// 第二轮 = 重试场景：12 个文件应全部命中断点续传
	pr := &fakeProgress{}
	if err := f.Transfer(context.Background(), adminUser(), "/存储A/相册", "/存储B", false, pr); err != nil {
		t.Fatalf("重试复制: %v", err)
	}
	if len(pr.files) != 0 {
		t.Fatalf("应全部跳过，实际 %d 个走了复制: %v", len(pr.files), pr.files)
	}
	if pr.done != int64(n*size) {
		t.Fatalf("跳过的文件也要计进度: done=%d 应为 %d", pr.done, n*size)
	}
	// 清单要如实标出「跳过」，后台才能看出这一轮哪些是真传了、哪些是命中断点续传
	if len(pr.skipped) != n {
		t.Fatalf("应上报 %d 个跳过，实际 %d: %v", n, len(pr.skipped), pr.skipped)
	}
	if len(pr.plan) != n || pr.plan[0].Path != "p00.jpg" || pr.plan[0].Size != size {
		t.Fatalf("清单应含 %d 项且带路径与大小，实际 %d: %+v", n, len(pr.plan), pr.plan)
	}
	if stats := cnt.stats.Load() - baseStats; stats != 0 {
		t.Fatalf("预扫描后不应再逐文件 Stat，实际 %d 次（每文件一次=打回旧实现）", stats)
	}
	if lists := cnt.lists.Load() - baseLists; lists != 1 {
		t.Fatalf("目标目录应只列 1 次，实际 %d 次", lists)
	}
}

// TestTransferSingleFileSkipsPrescan 单文件转存不预扫：为一个文件去列一个可能很大的
// 目标目录是净亏，一次 Stat 才是最省的。
func TestTransferSingleFileSkipsPrescan(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(dirA, "one.bin"), "12345678")
	dst, cnt := newCountingMount(t, 2, "/存储B", dirB)
	f := newTestFS(newLocalMount(t, 1, "/存储A", dirA), dst)

	if err := f.Transfer(context.Background(), adminUser(), "/存储A/one.bin", "/存储B", false, &fakeProgress{}); err != nil {
		t.Fatalf("单文件复制: %v", err)
	}
	if lists := cnt.lists.Load(); lists != 0 {
		t.Fatalf("单文件转存不应预扫目标目录，实际 List %d 次", lists)
	}
	if stats := cnt.stats.Load(); stats != 1 {
		t.Fatalf("单文件应恰好 1 次 Stat 探测，实际 %d 次", stats)
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
	linkErr error // 非 nil 时 Link 恒定返回它，用来模拟持续性故障（如一直被限流）
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
	if d.linkErr != nil {
		return nil, d.linkErr
	}
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
	// 失败信息要能定位：哪个文件、哪一侧。只丢一句技术错误出去，用户无从下手。
	// 这里源流读到一半就断，锅在源端——即便失败是从 Put 里冒出来的也不能算在目标头上。
	msg := util.Humanize(err)
	for _, want := range []string{"f.txt", stageSource, "已重试 2 次"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应含 %q，实际为 %q", want, msg)
		}
	}
	if strings.Contains(msg, stageDest) {
		t.Fatalf("源端读失败不该归因到写入目标，实际为 %q", msg)
	}
}

// TestTransferThrottledExhaustedMessage 退避全用完还在被限流时，不能再劝「稍后重试」——
// 那会让用户以为等几分钟就好，而实际要么并发开太大，要么每日上传额度已用尽（得等明天）。
func TestTransferThrottledExhaustedMessage(t *testing.T) {
	dirB := t.TempDir()
	src := &flakyDriver{
		content: "内容",
		linkErr: errors.New("上游错误：userRateLimitExceeded(HTTP 403) User Rate Limit Exceeded"),
	}
	f := newTestFS(
		&Mount{ID: 1, Path: "/限流源", Driver: "flaky", Enabled: true, drv: src},
		newLocalMount(t, 2, "/存储B", dirB),
	)
	err := f.Transfer(context.Background(), adminUser(), "/限流源/f.txt", "/存储B", false, &fakeProgress{})
	if err == nil {
		t.Fatal("持续限流应导致失败")
	}
	msg := util.Humanize(err)
	for _, want := range []string{"f.txt", "反复被限流", "并发", "不会重传"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("限流耗尽的提示应含 %q，实际为 %q", want, msg)
		}
	}
	if strings.Contains(msg, "请稍后重试") {
		t.Fatalf("退避都用完了就不该再劝「稍后重试」，实际为 %q", msg)
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
	plan   []model.TransferFile
	active []string
	seq    []string // 展示值每次变化后的取值，连续相同只记一次
	peak   int
}

func (p *stableProgress) SetTotal(int64)     {}
func (p *stableProgress) AddFile(int, int64) {}
func (p *stableProgress) FileSkip(int)       {}

func (p *stableProgress) SetFiles(items []model.TransferFile) {
	p.mu.Lock()
	p.plan = items
	p.mu.Unlock()
}

// path 取第 i 项的展示路径，须持锁。
func (p *stableProgress) path(i int) string {
	if i < 0 || i >= len(p.plan) {
		return ""
	}
	return p.plan[i].Path
}

func (p *stableProgress) FileStart(i int) {
	p.mu.Lock()
	p.active = append(p.active, p.path(i))
	p.record()
	p.mu.Unlock()
}

func (p *stableProgress) FileDone(i int, _ error) {
	p.mu.Lock()
	name := p.path(i)
	for k, n := range p.active {
		if n == name {
			p.active = append(p.active[:k], p.active[k+1:]...)
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
