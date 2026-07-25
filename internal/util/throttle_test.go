package util

import (
	"errors"
	"testing"
	"time"
)

func TestThrottleWait(t *testing.T) {
	cases := []struct {
		name       string
		attempt    int
		retryAfter string
		want       time.Duration
	}{
		{"服务端给了 Retry-After 就听它的", 1, "30", 30 * time.Second},
		{"没给则指数退避", 1, "", 2 * time.Second},
		{"指数按次数递增", 3, "", 8 * time.Second},
		{"Retry-After 过大钳到上限", 1, "600", 60 * time.Second},
		{"指数过大钳到上限", 9, "", 60 * time.Second},
		{"Retry-After 非法则回落指数", 2, "abc", 4 * time.Second},
		{"Retry-After 为 0 视作没给", 1, "0", 2 * time.Second},
		{"Retry-After 为 1 秒不被抬高", 1, "1", 1 * time.Second},
		{"attempt 越界有兜底", 0, "", 2 * time.Second},
	}
	for _, c := range cases {
		if got := ThrottleWait(c.attempt, c.retryAfter); got != c.want {
			t.Errorf("%s: ThrottleWait(%d, %q) = %v，应为 %v", c.name, c.attempt, c.retryAfter, got, c.want)
		}
	}
}

// TestIsThrottledMatchesHumanize 判定与文案必须同源：翻译成「请求过于频繁」的错误，
// 重试逻辑也必须当限流处理，否则会出现「提示让你等，代码却在猛冲」。
func TestIsThrottledMatchesHumanize(t *testing.T) {
	throttled := []error{
		errors.New("上游错误：rateLimitExceeded(HTTP 429) Rate Limit Exceeded"),
		errors.New("Too Many Requests"),
		errors.New("操作过于频繁"),
		errors.New("FLOOD_WAIT_30"),
	}
	for _, err := range throttled {
		if !IsThrottled(err) {
			t.Errorf("IsThrottled(%v) 应为 true", err)
		}
		if got := Humanize(err); got != "请求过于频繁，请稍后重试。" {
			t.Errorf("Humanize(%v) = %q，应翻成限流文案", err, got)
		}
	}
	for _, err := range []error{nil, errors.New("HTTP 404 not found"), errors.New("connection reset")} {
		if IsThrottled(err) {
			t.Errorf("IsThrottled(%v) 应为 false", err)
		}
	}
}

// TestContextfSurvivesHumanize 上下文必须活着到界面上——否则用户只看到
// 「请求过于频繁」，不知道是哪个文件、也不知道是源端还是目标端。
func TestContextfSurvivesHumanize(t *testing.T) {
	inner := errors.New("上游错误：rateLimitExceeded(HTTP 429) Rate Limit Exceeded")
	err := Contextf(inner, "复制 相册/2024/a.mp4 失败（写入目标，已重试 2 次）")
	const want = "复制 相册/2024/a.mp4 失败（写入目标，已重试 2 次）：请求过于频繁，请稍后重试。"
	if got := Humanize(err); got != want {
		t.Errorf("Humanize = %q，应为 %q", got, want)
	}
	if !errors.Is(err, inner) {
		t.Error("Contextf 应保留错误链，errors.Is 要能找到内层")
	}
	if Contextf(nil, "任何上下文") != nil {
		t.Error("Contextf(nil) 应返回 nil，才能直接包在返回值上")
	}
}

// TestMessagefOverridesHumanize 调用方写好的说明必须原样出去，不能被通用翻译盖掉——
// 「反复退避都过不去的限流」和「等一下就好的限流」错误码相同，只有调用方知道区别。
func TestMessagefSurvivesHumanize(t *testing.T) {
	inner := errors.New("上游错误：userRateLimitExceeded(HTTP 403) User Rate Limit Exceeded")
	const want = "复制 相册/a.mp4 失败（写入目标）：反复被限流，明天再继续。"
	err := Messagef(inner, want)
	if got := Humanize(err); got != want {
		t.Errorf("Humanize = %q，应原样返回调用方的说明 %q", got, want)
	}
	// 通用翻译本会把它变成「请求过于频繁，请稍后重试。」，这里必须没发生
	if got := Humanize(inner); got == want {
		t.Error("对照组失效：内层错误本应被翻成通用限流文案")
	}
	if !errors.Is(err, inner) {
		t.Error("Messagef 应保留错误链，errors.Is 要能找到内层")
	}
	if Messagef(nil, "任何说明") != nil {
		t.Error("Messagef(nil) 应返回 nil")
	}
}
