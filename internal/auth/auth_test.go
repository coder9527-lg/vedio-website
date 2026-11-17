package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestAuth(t *testing.T, opts ...Option) *Authenticator {
	t.Helper()
	a, err := New("s3cret", "fixed-secret-key-for-test", time.Hour, opts...)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return a
}

func TestIssueAndVerify(t *testing.T) {
	a := newTestAuth(t)
	now := time.Now()
	token := a.Issue(now)
	if err := a.Verify(token, now); err != nil {
		t.Fatalf("合法令牌校验失败: %v", err)
	}
	// 超过 TTL 后令牌应失效
	if err := a.Verify(token, now.Add(a.ttl+time.Second)); err == nil {
		t.Fatal("过期令牌应当校验失败")
	}
}

func TestVerifyRejectsTamperedToken(t *testing.T) {
	a := newTestAuth(t)
	token := a.Issue(time.Now())

	// 篡改签名
	tampered := token[:len(token)-1]
	if token[len(token)-1] == 'A' {
		tampered += "B"
	} else {
		tampered += "A"
	}
	if err := a.Verify(tampered, time.Now()); err == nil {
		t.Fatal("被篡改的令牌应当校验失败")
	}

	// 不同密钥签发的令牌不被接受
	other, _ := New("s3cret", "another-secret", time.Hour)
	if err := a.Verify(other.Issue(time.Now()), time.Now()); err == nil {
		t.Fatal("其他密钥签发的令牌应当校验失败")
	}

	// 格式错误
	for _, bad := range []string{"", "nodot", "abc.def.extra"} {
		if err := a.Verify(bad, time.Now()); err == nil {
			t.Fatalf("非法令牌 %q 应当校验失败", bad)
		}
	}
}

func TestCheckPasswordConstantTime(t *testing.T) {
	a := newTestAuth(t)
	if !a.CheckPassword("s3cret") {
		t.Error("正确密码应通过")
	}
	if a.CheckPassword("wrong") {
		t.Error("错误密码应拒绝")
	}
}

func TestCookieRoundTrip(t *testing.T) {
	a := newTestAuth(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	a.SetCookie(rec, req, false)

	resp := rec.Result()
	defer resp.Body.Close()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("期望 1 个 Cookie，实际 %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != CookieName || !c.HttpOnly || c.MaxAge <= 0 {
		t.Fatalf("Cookie 属性不符合预期: %+v", c)
	}

	// 用回传的 Cookie 构造请求，应通过校验
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(c)
	if err := a.CheckRequest(req2); err != nil {
		t.Fatalf("携带合法 Cookie 应通过: %v", err)
	}

	// 无 Cookie 的请求应失败
	if err := a.CheckRequest(httptest.NewRequest(http.MethodGet, "/", nil)); err == nil {
		t.Fatal("无 Cookie 请求应被拒绝")
	}
}

func TestClearCookieExpires(t *testing.T) {
	a := newTestAuth(t)
	rec := httptest.NewRecorder()
	a.ClearCookie(rec, false)
	c := rec.Result().Cookies()[0]
	if c.MaxAge != -1 {
		t.Fatalf("清除 Cookie 的 MaxAge 应为 -1，实际 %d", c.MaxAge)
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := newTestAuth(t, WithLimit(3, time.Minute))
	ip := "10.0.0.8"
	now := time.Now()

	for i := 0; i < 3; i++ {
		if !a.AllowLogin(ip, now) {
			t.Fatalf("第 %d 次失败前应允许尝试", i+1)
		}
		a.RecordFailure(ip, now)
	}
	if a.AllowLogin(ip, now.Add(time.Second)) {
		t.Fatal("达到失败上限后应被限流")
	}

	// 窗口过后自动恢复
	later := now.Add(time.Minute + time.Second)
	if !a.AllowLogin(ip, later) {
		t.Fatal("限流窗口过后应恢复尝试")
	}

	// 成功登录后重置计数
	a.RecordFailure(ip, later)
	a.ResetFailures(ip)
	if !a.AllowLogin(ip, later) {
		t.Fatal("重置失败记录后应允许尝试")
	}
}

func TestRandomSecretOnEmpty(t *testing.T) {
	a1, err := New("pw", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := New("pw", "", time.Hour)
	if err := a1.Verify(a2.Issue(time.Now()), time.Now()); err == nil {
		t.Fatal("随机密钥实例之间令牌不应互通")
	}
}

func TestNewRejectsEmptyPassword(t *testing.T) {
	if _, err := New("", "secret", time.Hour); err == nil {
		t.Fatal("空密码应返回错误")
	}
}
