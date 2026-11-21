package auth

import (
	"testing"
	"time"
)

func TestFailLimiterLocksAfterMaxFails(t *testing.T) {
	l := NewFailLimiter(3, time.Minute)
	key := "203.0.113.7|token-abc"
	now := time.Now()

	// 前 maxFails 次：允许尝试，逐次记录失败
	for i := 0; i < 3; i++ {
		if !l.Allow(key, now) {
			t.Fatalf("第 %d 次失败前应允许尝试", i+1)
		}
		l.RecordFailure(key, now)
	}
	if l.Allow(key, now.Add(time.Second)) {
		t.Fatal("窗口内达到失败上限后应被限流")
	}

	// 不同 key 互不影响
	other := "203.0.113.8|token-abc"
	if !l.Allow(other, now) {
		t.Fatal("不同限流键不应被连带锁定")
	}
}

func TestFailLimiterWindowExpiry(t *testing.T) {
	now := time.Now()
	l := NewFailLimiter(2, time.Minute)
	key := "198.51.100.9|t"

	l.RecordFailure(key, now)
	l.RecordFailure(key, now)
	if l.Allow(key, now.Add(time.Second)) {
		t.Fatal("达到上限后应被限流")
	}
	// 窗口过期：历史计数作废，重新放行
	if !l.Allow(key, now.Add(time.Minute+time.Second)) {
		t.Fatal("窗口过期后应恢复尝试")
	}
}

func TestFailLimiterReset(t *testing.T) {
	now := time.Now()
	l := NewFailLimiter(1, time.Minute)
	key := "k"
	l.RecordFailure(key, now)
	if l.Allow(key, now) {
		t.Fatal("记录失败后应立即锁定")
	}
	l.Reset(key)
	if !l.Allow(key, now) {
		t.Fatal("Reset 后应清空失败记录")
	}
}

func TestFailLimiterDefaultsOnBadParams(t *testing.T) {
	// 非法构造参数回退保守默认值，限流器仍可正常工作
	l := NewFailLimiter(0, 0)
	now := time.Now()
	for i := 0; i < 10; i++ {
		l.RecordFailure("k", now)
	}
	if l.Allow("k", now) {
		t.Fatal("默认上限 10 次后应被限流")
	}
}
