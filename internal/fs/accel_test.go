package fs

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"newlist/internal/driver"
	"newlist/internal/model"
)

func TestAccelOpts(t *testing.T) {
	cases := []struct {
		cfg  driver.Config
		want AccelOpts
	}{
		{nil, AccelOpts{false, 4, 4 << 20}},
		{driver.Config{"proxy": "true", "threads": "8", "chunk_mb": "16"}, AccelOpts{true, 8, 16 << 20}},
		{driver.Config{"proxy": "false", "threads": "0", "chunk_mb": "999"}, AccelOpts{false, 1, 64 << 20}},
		{driver.Config{"threads": "100"}, AccelOpts{false, 32, 4 << 20}},
		{driver.Config{"threads": "abc", "chunk_mb": ""}, AccelOpts{false, 4, 4 << 20}},
	}
	for i, c := range cases {
		m := &Mount{Cfg: c.cfg}
		if got := m.accelOpts(); got != c.want {
			t.Fatalf("case %d: got %+v want %+v", i, got, c.want)
		}
	}
}

// accelPattern 确定性内容（与 stream 包相同规则，此处独立生成避免跨包引测试代码）。
func accelPattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + 7)
	}
	return b
}

// urlSrcDriver 源驱动：Link 返回 mock 直链 URL（模拟云盘）。
type urlSrcDriver struct {
	name    string
	size    int64
	linkURL func() string
	mu      sync.Mutex
	calls   int
}

func (d *urlSrcDriver) Init(context.Context, driver.Config) error { return nil }
func (d *urlSrcDriver) Drop() error                               { return nil }
func (d *urlSrcDriver) List(context.Context, string) ([]model.FileInfo, error) {
	return nil, driver.ErrNotFound
}
func (d *urlSrcDriver) Stat(context.Context, string) (model.FileInfo, error) {
	return model.FileInfo{Name: d.name, Size: d.size}, nil
}
func (d *urlSrcDriver) Link(context.Context, string) (*driver.Link, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	return &driver.Link{URL: d.linkURL()}, nil
}
func (d *urlSrcDriver) linkCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// newAccelRangeSrv 支持 Range 的 mock 直链源；reject 返回 true 时以 403 拒绝本次请求。
func newAccelRangeSrv(t *testing.T, content []byte,
	reject func(path string, reqN int) bool) (*httptest.Server, func() []int64) {
	t.Helper()
	var mu sync.Mutex
	var starts []int64
	reqN := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqN++
		n := reqN
		mu.Unlock()
		if reject != nil && reject(r.URL.Path, n) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var a, b int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &a, &b); err != nil {
			http.Error(w, "bad range", 400)
			return
		}
		if b > int64(len(content)-1) {
			b = int64(len(content) - 1)
		}
		mu.Lock()
		starts = append(starts, a)
		mu.Unlock()
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[a : b+1])
	}))
	t.Cleanup(srv.Close)
	return srv, func() []int64 {
		mu.Lock()
		defer mu.Unlock()
		out := make([]int64, len(starts))
		copy(out, starts)
		return out
	}
}

// 跨存储转存：源为 URL 直链且配置多线程 → 分块并发拉源，内容一致、进度相符。
func TestTransferAccelURLSource(t *testing.T) {
	content := accelPattern(3<<20 + 4321) // 3MB+，chunk 1MB → 4 块
	srv, getStarts := newAccelRangeSrv(t, content, nil)

	src := &urlSrcDriver{name: "big.bin", size: int64(len(content)), linkURL: func() string { return srv.URL }}
	dirB := t.TempDir()
	f := newTestFS(
		&Mount{ID: 1, Path: "/云源", Driver: "urltest", Enabled: true, drv: src,
			Cfg: driver.Config{"threads": "3", "chunk_mb": "1"}},
		newLocalMount(t, 2, "/存储B", dirB),
	)
	pr := &fakeProgress{}
	if err := f.Transfer(context.Background(), adminUser(), "/云源/big.bin", "/存储B", false, pr); err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dirB, "big.bin"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("目标内容不一致: len=%d want=%d err=%v", len(got), len(content), err)
	}
	if pr.done != int64(len(content)) {
		t.Fatalf("进度不符: done=%d want=%d", pr.done, len(content))
	}
	if starts := getStarts(); len(starts) < 2 {
		t.Fatalf("应分块并发拉源（多个 Range 请求），实际 %d 个: %v", len(starts), starts)
	}
}

// 直链过期：旧链 403 → MultiReader 经驱动 Link 重取新链后转存成功。
func TestTransferAccelLinkRefresh(t *testing.T) {
	content := accelPattern(3 << 20)
	gen := 0
	var mu sync.Mutex
	srv, _ := newAccelRangeSrv(t, content, func(path string, reqN int) bool {
		return reqN >= 2 && strings.HasSuffix(path, "/g1") // 第 2 个请求起 g1 判过期
	})
	src := &urlSrcDriver{name: "f.bin", size: int64(len(content)), linkURL: func() string {
		mu.Lock()
		defer mu.Unlock()
		gen++
		return fmt.Sprintf("%s/g%d", srv.URL, gen)
	}}
	dirB := t.TempDir()
	f := newTestFS(
		&Mount{ID: 1, Path: "/云源", Driver: "urltest", Enabled: true, drv: src,
			Cfg: driver.Config{"threads": "2", "chunk_mb": "1"}},
		newLocalMount(t, 2, "/存储B", dirB),
	)
	if err := f.Transfer(context.Background(), adminUser(), "/云源/f.bin", "/存储B", false, &fakeProgress{}); err != nil {
		t.Fatalf("换链后应成功: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dirB, "f.bin"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("目标内容不一致 err=%v", err)
	}
	if src.linkCalls() < 2 {
		t.Fatalf("驱动 Link 应被重调（换链），实际 %d 次", src.linkCalls())
	}
}
