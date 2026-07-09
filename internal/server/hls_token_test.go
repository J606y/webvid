package server

import (
	"strings"
	"testing"
)

func TestInjectToken(t *testing.T) {
	in := "#EXTM3U\n" +
		"#EXT-X-VERSION:7\n" +
		"#EXT-X-MAP:URI=\"init.mp4\"\n" +
		"#EXTINF:4.000,\n" +
		"seg_0.m4s\n" +
		"#EXTINF:2.500,\n" +
		"seg_1.m4s\n" +
		"#EXT-X-ENDLIST\n"
	out := string(injectToken([]byte(in), "a+b/c"))
	for _, want := range []string{
		`#EXT-X-MAP:URI="init.mp4?token=a%2Bb%2Fc"`,
		"seg_0.m4s?token=a%2Bb%2Fc",
		"seg_1.m4s?token=a%2Bb%2Fc",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "#EXT-X-ENDLIST?") || strings.Contains(out, "#EXTM3U?") {
		t.Fatalf("注释行不应被追加 token:\n%s", out)
	}
	if got := string(injectToken([]byte(in), "")); got != in {
		t.Fatal("无 token 应原样返回")
	}
}

func TestSegNameRe(t *testing.T) {
	for name, want := range map[string]bool{
		"seg_0.m4s": true, "seg_123.m4s": true,
		"seg_.m4s": false, "seg_1.mp4": false, "../x": false, "seg_1.m4s.tmp": false,
	} {
		if segNameRe.MatchString(name) != want {
			t.Fatalf("segNameRe(%q) 期望 %v", name, want)
		}
	}
}
