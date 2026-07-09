package auth

import "testing"

// TestLoginLimiterPerKeyLockout 是本次改动的核心：单个 key 连续失败达 maxFails 次后
// 应被锁定，但不影响其他 key（防攻击者用失败请求锁死唯一管理员的登录）。
func TestLoginLimiterPerKeyLockout(t *testing.T) {
	var l LoginLimiter // 零值可直接用

	if !l.Allow("a") {
		t.Fatal("初始状态 a 应允许登录")
	}

	for i := 0; i < maxFails; i++ {
		l.Fail("a")
	}

	if l.Allow("a") {
		t.Fatal("a 连续失败 maxFails 次后应被锁定")
	}
	if !l.Allow("b") {
		t.Fatal("b 未失败过，不应受 a 的锁定影响")
	}
}

func TestLoginLimiterFewerThanMaxFailsStillAllowed(t *testing.T) {
	var l LoginLimiter
	for i := 0; i < maxFails-1; i++ {
		l.Fail("a")
	}
	if !l.Allow("a") {
		t.Fatal("失败次数未达 maxFails 时应仍允许登录")
	}
}

func TestLoginLimiterSuccessClearsLockout(t *testing.T) {
	var l LoginLimiter
	for i := 0; i < maxFails; i++ {
		l.Fail("a")
	}
	if l.Allow("a") {
		t.Fatal("锁定后 Allow 应为 false（前置条件）")
	}

	l.Success("a")

	if !l.Allow("a") {
		t.Fatal("Success 后 a 的锁定与失败计数应被清零，Allow 应恢复 true")
	}
}

func TestLoginLimiterSuccessOnUnknownKeyIsNoop(t *testing.T) {
	var l LoginLimiter
	l.Success("never-seen") // 不应 panic
	if !l.Allow("never-seen") {
		t.Fatal("从未失败过的 key 应始终允许登录")
	}
}
