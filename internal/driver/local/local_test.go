package local

import (
	"errors"
	"os"
	"testing"

	"newlist/internal/driver"
)

// TestPathEscapeSentinel 锁死对 os.Root 越界错误文案的依赖：isPathEscape 用字符串匹配
// "escapes from parent"（stdlib 未导出该 sentinel）。将来 Go 改文案时本测试先红，
// 提示同步更新 isPathEscape，避免越界访问静默降级为 500 而非 ErrNotFound。
func TestPathEscapeSentinel(t *testing.T) {
	dir := t.TempDir()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer r.Close()

	_, err = r.Open("../escape-target") // 越出根目录
	if err == nil {
		t.Fatal("期望 os.Root 拒绝越界访问，却成功了")
	}
	if !isPathEscape(err) {
		t.Fatalf("isPathEscape 未识别 os.Root 越界错误，stdlib 文案可能已变: %v", err)
	}
	// mapErr 须把越界统一映射为 ErrNotFound（不暴露路径细节）。
	if got := mapErr(err); !errors.Is(got, driver.ErrNotFound) {
		t.Fatalf("mapErr(越界) = %v, want ErrNotFound", got)
	}
}
