// 删除索引（Clear）：清表 + 进度归零 + 重建进行中拒绝；以及新建 Builder 时回填条数。
package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newlist/internal/db"
	_ "newlist/internal/driver/local" // 注册 local 驱动
	"newlist/internal/fs"
)

// mount 建库并挂载 /m -> root，返回 db 与 fs（清理由 t.Cleanup 负责）。
func mount(t *testing.T, root string) (*sql.DB, *fs.FS) {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	cfgJSON, _ := json.Marshal(map[string]string{"root_path": root})
	if _, err := d.Exec(
		`INSERT INTO storages(mount_path, driver, config, ord, enabled, status, created_at)
		 VALUES('/m', 'local', ?, 0, 1, '', '2026-07-28T00:00:00Z')`, string(cfgJSON)); err != nil {
		t.Fatalf("插入存储: %v", err)
	}
	f := fs.New(d)
	if err := f.Reload(context.Background()); err != nil {
		t.Fatalf("fs.Reload: %v", err)
	}
	t.Cleanup(func() {
		// local 驱动持有根目录句柄（os.OpenRoot），Windows 下不 Drop 则 TempDir 删不掉
		d.Exec(`DELETE FROM storages`)
		f.Reload(context.Background())
		d.Close()
	})
	return d, f
}

func waitDone(t *testing.T, b *Builder) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for b.Progress().Running {
		if time.Now().After(deadline) {
			t.Fatal("索引重建超时")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e := b.Progress().Err; e != "" {
		t.Fatalf("索引重建出错: %s", e)
	}
}

func count(t *testing.T, d *sql.DB) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatalf("查询 files: %v", err)
	}
	return n
}

// TestClearEmptiesIndex：Clear 删空 files 表并把进度归零，重建可再建回来。
func TestClearEmptiesIndex(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	d, f := mount(t, root)

	b := New(d, f)
	b.Rebuild()
	waitDone(t, b)
	if n := count(t, d); n == 0 {
		t.Fatal("重建后 files 表不应为空")
	}
	if b.Progress().Scanned == 0 {
		t.Fatal("重建后进度条数不应为 0")
	}

	if err := b.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if n := count(t, d); n != 0 {
		t.Fatalf("Clear 后 files 表应为空, got %d 行", n)
	}
	p := b.Progress()
	if p.Running || p.Scanned != 0 || p.Current != "" || p.Err != "" {
		t.Fatalf("Clear 后进度应归零, got %+v", p)
	}

	// 删完还能重建回来
	b.Rebuild()
	waitDone(t, b)
	if n := count(t, d); n == 0 {
		t.Fatal("Clear 后重建应重新建立索引")
	}
}

// TestClearBusyRejected：重建进行中 Clear 返回 ErrBusy，索引不动。
func TestClearBusyRejected(t *testing.T) {
	d, f := mount(t, t.TempDir())
	b := New(d, f)

	// 直接摆出「重建中」的状态：真重建在空目录上几毫秒就跑完，抢不到这个窗口
	b.mu.Lock()
	b.prog = Progress{Running: true, Scanned: 7}
	b.mu.Unlock()

	if err := b.Clear(); err != ErrBusy {
		t.Fatalf("重建中 Clear 应返回 ErrBusy, got %v", err)
	}
	if p := b.Progress(); !p.Running || p.Scanned != 7 {
		t.Fatalf("被拒的 Clear 不应改动进度, got %+v", p)
	}
}

// TestNewLoadsCount：新建 Builder 时从 files 表回填条数——否则重启后
// 已建好的索引会显示成「共 0 项」。
func TestNewLoadsCount(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	d, f := mount(t, root)

	b := New(d, f)
	b.Rebuild()
	waitDone(t, b)
	want := count(t, d)

	// 另起一个 Builder = 重启后的进程
	if got := New(d, f).Progress().Scanned; got != int64(want) {
		t.Fatalf("新 Builder 应回填索引条数 %d, got %d", want, got)
	}
}
