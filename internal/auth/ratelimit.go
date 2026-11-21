package auth

import (
	"sync"
	"time"
)

// failState 描述单个限流键在当前窗口内的失败计数。
type failState struct {
	count   int
	firstAt time.Time
}

// FailLimiter 是按字符串键（通常是 IP，或 IP+资源标识）统计失败次数的通用限流器：
// 滑动窗口内失败达到 maxFails 次后，该键被锁定到当前窗口结束；窗口过期后自动放行并重新计数。
// 零值不可用，请用 NewFailLimiter 构造。
type FailLimiter struct {
	maxFails int
	window   time.Duration

	mu       sync.Mutex
	failures map[string]*failState
}

// NewFailLimiter 创建限流器。maxFails<=0 或 window<=0 时回退到保守默认值（10 次 / 10 分钟）。
func NewFailLimiter(maxFails int, window time.Duration) *FailLimiter {
	if maxFails <= 0 {
		maxFails = 10
	}
	if window <= 0 {
		window = 10 * time.Minute
	}
	return &FailLimiter{
		maxFails: maxFails,
		window:   window,
		failures: make(map[string]*failState),
	}
}

// Allow 判断 key 当前是否还可以尝试：从未失败或窗口已过期返回 true，窗口内达到上限返回 false。
func (l *FailLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.failures[key]
	if !ok {
		return true
	}
	if now.Sub(st.firstAt) > l.window {
		delete(l.failures, key)
		return true
	}
	return st.count < l.maxFails
}

// RecordFailure 记录 key 的一次失败；窗口外的历史计数视为新窗口的首次失败。
func (l *FailLimiter) RecordFailure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.failures[key]
	if !ok || now.Sub(st.firstAt) > l.window {
		l.failures[key] = &failState{count: 1, firstAt: now}
		return
	}
	st.count++
}

// Reset 清空 key 的失败记录（成功鉴权后调用）。
func (l *FailLimiter) Reset(key string) {
	l.mu.Lock()
	delete(l.failures, key)
	l.mu.Unlock()
}
