package util

import (
	"strconv"
	"strings"
	"time"
)

// 限流退避的时长边界：下限避免抢着重试反而把配额烧得更快，
// 上限避免服务端给出一个离谱的 Retry-After 就把任务挂死几分钟。
const (
	throttleMin = 1 * time.Second
	throttleMax = 60 * time.Second
)

// ThrottleWait 算出撞到限流后该等多久：服务端给了 Retry-After 就听它的，
// 没给则按第 attempt 次重试指数退避（2s、4s、8s…），最后统一钳进 [1s, 60s]。
// attempt 从 1 起。
//
// 上限必须留够：OneDrive/SharePoint 的 Retry-After 常达数十秒，钳到几秒就重试
// 只会再撞一次限流，把本可自愈的任务拖成失败。
func ThrottleWait(attempt int, retryAfter string) time.Duration {
	var wait time.Duration
	if ra, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && ra > 0 {
		wait = time.Duration(ra) * time.Second
	} else {
		if attempt < 1 {
			attempt = 1
		}
		if attempt > 6 { // 1<<6=64s，再大也会被下面钳到上限，先挡住移位溢出
			attempt = 6
		}
		wait = time.Duration(int64(1)<<uint(attempt)) * time.Second
	}
	if wait < throttleMin {
		return throttleMin
	}
	if wait > throttleMax {
		return throttleMax
	}
	return wait
}

// IsThrottled 报告错误是否为上游限流，调用方据此决定退避多久再重试。
// 判定关键词与 Humanize 共用一份，避免「翻译成限流、重试却不当限流处理」的漂移。
func IsThrottled(err error) bool {
	if err == nil {
		return false
	}
	return has(strings.ToLower(err.Error()), throttleHints...)
}
