package server

import (
	"net/http"
	"testing"
)

// TestIsHLS 覆盖 HLS 识别：mpegurl 系 Content-Type 命中，或 URL 路径 .m3u8 结尾命中。
func TestIsHLS(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		url         string
		want        bool
	}{
		{"apple-mpegurl", "application/vnd.apple.mpegurl", "https://cdn.x/live/stream", true},
		{"x-mpegurl", "application/x-mpegurl", "https://cdn.x/live/stream", true},
		{"audio-x-mpegurl", "audio/x-mpegurl; charset=utf-8", "https://cdn.x/live/stream", true},
		{"suffix-m3u8", "application/octet-stream", "https://cdn.x/1080p/video.m3u8", true},
		{"suffix-uppercase", "text/plain", "https://cdn.x/a/PLAYLIST.M3U8?t=1", true},
		{"suffix-with-query", "", "https://cdn.x/a/index.m3u8?token=abc", true},
		{"plain-zip", "application/zip", "https://cdn.x/pkg/resource.zip", false},
		{"plain-jpg", "image/jpeg", "https://cdn.x/img/photo.jpg", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			if c.contentType != "" {
				resp.Header.Set("Content-Type", c.contentType)
			}
			if got := isHLS(resp, c.url); got != c.want {
				t.Fatalf("isHLS(ct=%q, url=%q) = %v, 期望 %v", c.contentType, c.url, got, c.want)
			}
		})
	}
}

// TestHLSOutName 覆盖落地命名：自定义名优先并强制补 .mp4，否则 URL 末段去扩展名，兜底 video；结果恒 .mp4。
func TestHLSOutName(t *testing.T) {
	cases := []struct {
		name   string
		custom string
		url    string
		want   string
	}{
		{"custom-no-ext", "abc", "https://cdn.x/a/video.m3u8", "abc.mp4"},
		{"custom-with-mp4", "abc.mp4", "https://cdn.x/a/video.m3u8", "abc.mp4"},
		{"custom-mp4-uppercase", "abc.MP4", "https://cdn.x/a/video.m3u8", "abc.MP4"},
		{"custom-other-ext", "abc.mkv", "https://cdn.x/a/video.m3u8", "abc.mkv.mp4"},
		{"custom-strip-slash", "a/b", "https://cdn.x/a/video.m3u8", "ab.mp4"},
		{"derive-base", "", "https://cdn.x/1080p/playlist.m3u8", "playlist.mp4"},
		{"derive-escaped", "", "https://cdn.x/a/%E7%94%B5%E5%BD%B1.m3u8", "电影.mp4"},
		{"derive-fallback-root", "", "https://cdn.x/", "video.mp4"},
		// 空白自定义名被 sanitize 掉 → 回落 URL 派生；末尾斜杠时 path.Base 取目录名。
		{"blank-custom-derives-dir", "   ", "https://cdn.x/live/", "live.mp4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hlsOutName(c.custom, c.url); got != c.want {
				t.Fatalf("hlsOutName(%q, %q) = %q, 期望 %q", c.custom, c.url, got, c.want)
			}
		})
	}
}
