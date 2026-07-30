package googledrive

import (
	"testing"

	"newlist/internal/driver"
)

// 编译期断言：GDrive 必须满足 driver.Thumber。不满足的话 thumb 层根本不进云盘缩略图
// 那条分支，会静默退回 ffmpeg 经回环抽帧——实测在 gdrive 上一小时跑不出几张，不可用。
var _ driver.Thumber = (*GDrive)(nil)

// TestSizedThumb 缩略图链尺寸改写：认得出的后缀换成目标宽度，认不出的原样返回。
// 改坏一个签名链的代价是一张 404 封面，所以「宁可维持默认尺寸」这条得有测试兜着。
func TestSizedThumb(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"Drive 默认 s220", "https://lh3.googleusercontent.com/abc=s220", "https://lh3.googleusercontent.com/abc=s640"},
		{"宽高两段", "https://lh3.googleusercontent.com/abc=w400-h300", "https://lh3.googleusercontent.com/abc=s640"},
		{"已是 w 宽", "https://lh3.googleusercontent.com/abc=w800", "https://lh3.googleusercontent.com/abc=s640"},
		{"无尺寸后缀原样返回", "https://lh3.googleusercontent.com/abc", "https://lh3.googleusercontent.com/abc"},
		{"尺寸不在末尾不动", "https://x/=s220/tail", "https://x/=s220/tail"},
		{"空串", "", ""},
	}
	for _, c := range cases {
		if got := sizedThumb(c.in, thumbWidth); got != c.want {
			t.Errorf("%s: sizedThumb(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}
