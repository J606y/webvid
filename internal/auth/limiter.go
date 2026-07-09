package auth

import (
	"sync"
	"time"
)

// LoginLimiter 按客户端 IP 分桶的登录防爆破：单个 IP 连续失败 maxFails 次后锁定 lockFor，
// 不影响其他 IP——防攻击者用失败请求锁死唯一管理员的登录（全局单桶的 DoS 面）。零值可用。
type LoginLimiter struct {
	mu      sync.Mutex
	buckets map[string]*loginBucket
}

type loginBucket struct {
	fails    int
	lockedTo time.Time
	seen     time.Time // 最后活动时间，惰性清理用
}

const (
	maxFails = 5
	lockFor  = 5 * time.Minute
)

func (l *LoginLimiter) bucket(key string) *loginBucket {
	if l.buckets == nil {
		l.buckets = map[string]*loginBucket{}
	}
	b := l.buckets[key]
	if b == nil {
		b = &loginBucket{}
		l.buckets[key] = b
	}
	return b
}

// Allow 返回该 IP 当前是否允许尝试登录。
func (l *LoginLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gc()
	b := l.bucket(key)
	b.seen = time.Now()
	return time.Now().After(b.lockedTo)
}

// Fail 记该 IP 一次失败：达 maxFails 即锁定。
func (l *LoginLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.bucket(key)
	b.seen = time.Now()
	b.fails++
	if b.fails >= maxFails {
		b.lockedTo = time.Now().Add(lockFor)
		b.fails = 0
	}
}

// Success 登录成功，清除该 IP 的失败计数。
func (l *LoginLimiter) Success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// gc 惰性清理过期空闲桶，防 map 无界增长（桶数超阈值时于 Allow 顺带执行）。
func (l *LoginLimiter) gc() {
	if len(l.buckets) < 1024 {
		return
	}
	now := time.Now()
	for k, b := range l.buckets {
		if now.After(b.lockedTo) && now.Sub(b.seen) > lockFor {
			delete(l.buckets, k)
		}
	}
}
