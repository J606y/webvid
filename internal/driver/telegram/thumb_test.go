package telegram

import (
	"testing"

	"github.com/gotd/td/tg"

	"newlist/internal/driver"
)

// 编译期断言：Telegram 必须满足 driver.ThumbFetcher。它给不出 HTTP 直链，所以走不了
// driver.Thumber 那条路；不满足这个接口，thumb 层就没有分支能为 TG 文件出封面。
var _ driver.ThumbFetcher = (*Telegram)(nil)

// TestPickThumb 档位挑选：取最大的一档，内联字节优先直接用，
// 去掉 JPEG 头的 stripped 与矢量轮廓 path 一律不认。
func TestPickThumb(t *testing.T) {
	t.Run("多档取最大", func(t *testing.T) {
		typ, cached, ok := pickThumb([]tg.PhotoSizeClass{
			&tg.PhotoSize{Type: "s", W: 90, H: 90},
			&tg.PhotoSize{Type: "y", W: 640, H: 360},
			&tg.PhotoSize{Type: "m", W: 320, H: 180},
		})
		if !ok || typ != "y" || cached != nil {
			t.Fatalf("应取宽 640 的 y 档且无内联字节，实际 typ=%q cached=%v ok=%v", typ, cached, ok)
		}
	})

	t.Run("progressive 也算常规档", func(t *testing.T) {
		typ, _, ok := pickThumb([]tg.PhotoSizeClass{
			&tg.PhotoSizeProgressive{Type: "x", W: 800, H: 450, Sizes: []int{1, 2}},
		})
		if !ok || typ != "x" {
			t.Fatalf("progressive 档应被采纳，实际 typ=%q ok=%v", typ, ok)
		}
	})

	t.Run("cached 带回内联字节", func(t *testing.T) {
		want := []byte{0xFF, 0xD8, 0xFF}
		_, cached, ok := pickThumb([]tg.PhotoSizeClass{
			&tg.PhotoCachedSize{Type: "m", W: 320, H: 180, Bytes: want},
		})
		if !ok || string(cached) != string(want) {
			t.Fatalf("应带回内联字节，实际 cached=%v ok=%v", cached, ok)
		}
	})

	t.Run("只有 stripped 与 path 视作没有缩略图", func(t *testing.T) {
		if _, _, ok := pickThumb([]tg.PhotoSizeClass{
			&tg.PhotoStrippedSize{Type: "i", Bytes: []byte{1, 2, 3}},
			&tg.PhotoPathSize{Type: "j", Bytes: []byte{4, 5}},
		}); ok {
			t.Fatal("stripped/path 不该被当成可用缩略图")
		}
	})

	t.Run("空清单", func(t *testing.T) {
		if _, _, ok := pickThumb(nil); ok {
			t.Fatal("没有档位时应报不可用")
		}
	})
}
