package fs

import (
	"errors"
	"strings"
	"testing"
)

func TestNormPath(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "already normalized", in: "/a/b", want: "/a/b"},
		{name: "relative gets rooted", in: "a/b", want: "/a/b"},
		{name: "trailing slash stripped", in: "/a/b/", want: "/a/b"},
		{name: "root", in: "/", want: "/"},
		{name: "empty string", in: "", want: "/"},
		{name: "dot", in: ".", want: "/"},
		{name: "dot dot dot traversal collapses to root", in: "..", want: "/"},
		{name: "leading dotdot traversal", in: "/../../etc/passwd", want: "/etc/passwd"},
		{name: "embedded dotdot traversal", in: "/a/../../b", want: "/b"},
		{name: "relative dotdot traversal", in: "../../a/b", want: "/a/b"},
		{name: "double slashes collapsed", in: "/a//b///c", want: "/a/b/c"},
		{name: "backslash rejected", in: "a\\b", wantErr: true},
		{name: "leading backslash rejected", in: "\\a\\b", wantErr: true},
		{name: "nul byte rejected", in: "a\x00b", wantErr: true},
		{name: "nul only rejected", in: "\x00", wantErr: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormPath(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("NormPath(%q) 应返回 error，实际 got=%q err=nil", c.in, got)
				}
				if !errors.Is(err, ErrBadPath) {
					t.Fatalf("NormPath(%q) err 应为 ErrBadPath，实际 %v", c.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormPath(%q) 不应报错: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("NormPath(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestNormPathTraversalStaysInRoot 额外断言任意穿越构造都不会跳出根目录：
// 结果必须以 / 开头且不含 ".." 段。
func TestNormPathTraversalStaysInRoot(t *testing.T) {
	inputs := []string{
		"../../../../../../etc/passwd",
		"/a/b/../../../../c",
		"....//....//etc",
		"/./../.",
	}
	for _, in := range inputs {
		got, err := NormPath(in)
		if err != nil {
			t.Fatalf("NormPath(%q) 不应报错: %v", in, err)
		}
		if !strings.HasPrefix(got, "/") {
			t.Fatalf("NormPath(%q) = %q 应以 / 开头", in, got)
		}
		for _, seg := range strings.Split(got, "/") {
			if seg == ".." {
				t.Fatalf("NormPath(%q) = %q 不应包含 .. 段（逃出根目录）", in, got)
			}
		}
	}
}
