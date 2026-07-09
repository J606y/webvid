package fs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"newlist/internal/driver"
	"newlist/internal/driver/local"
	"newlist/internal/model"
	"newlist/internal/user"
)

// fakeProgress 记录进度回调（测试断言用）。
type fakeProgress struct {
	mu    sync.Mutex
	total int64
	done  int64
	files []string
}

func (p *fakeProgress) SetTotal(n int64) { p.mu.Lock(); p.total = n; p.mu.Unlock() }
func (p *fakeProgress) SetFile(s string) { p.mu.Lock(); p.files = append(p.files, s); p.mu.Unlock() }
func (p *fakeProgress) Add(n int64)      { p.mu.Lock(); p.done += n; p.mu.Unlock() }

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
