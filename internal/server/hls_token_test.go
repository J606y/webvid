package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
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
	out := string(injectQuery([]byte(in), "a+b/c", 0))
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
	if got := string(injectQuery([]byte(in), "", 0)); got != in {
		t.Fatal("无 token 也无能力标记时应原样返回")
	}

	// HEVC 能力必须跟着分片走：分片落到哪个会话由它决定，丢了就会把直出的列表
	// 接到重编码的会话上（或反之），播出来是另一套分片。
	cap10 := string(injectQuery([]byte(in), "tok", 10))
	for _, want := range []string{
		`#EXT-X-MAP:URI="init.mp4?token=tok&hevc=10"`,
		"seg_0.m4s?token=tok&hevc=10",
	} {
		if !strings.Contains(cap10, want) {
			t.Fatalf("缺少 %q:\n%s", want, cap10)
		}
	}
	// 无 token（Safari 原生 HLS 之外的路径）时能力标记自己起头
	if got := string(injectQuery([]byte(in), "", 8)); !strings.Contains(got, "seg_0.m4s?hevc=8") {
		t.Fatalf("无 token 时能力标记应以 ? 起头:\n%s", got)
	}
}

func TestHevcCapQuery(t *testing.T) {
	for q, want := range map[string]int{"10": 10, "8": 8, "": 0, "1": 0, "yes": 0, "12": 0} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/?hevc="+q, nil)
		if got := hevcCap(c); got != want {
			t.Fatalf("hevc=%q 期望 %d，得到 %d", q, want, got)
		}
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
