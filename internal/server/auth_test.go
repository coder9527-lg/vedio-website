package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/auth"
	"github.com/coder9527-lg/vedio-website/internal/media"
)

// noRedirectClient 不自动跟随 302，便于直接断言重定向与 Set-Cookie。
var noRedirectClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func newTestServer(t *testing.T, password string) (*Server, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	lib := media.NewLibrary(root, media.DefaultExts)
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatalf("NewThumbnailer 失败: %v", err)
	}
	var authenticator *auth.Authenticator
	if password != "" {
		authenticator, err = auth.New(password, "test-secret", time.Hour,
			auth.WithLimit(3, time.Minute))
		if err != nil {
			t.Fatalf("auth.New 失败: %v", err)
		}
	}
	srv, err := New(lib, thumbs, authenticator, false)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func login(t *testing.T, ts *httptest.Server, password, next string) *http.Response {
	t.Helper()
	form := url.Values{"password": {password}, "next": {next}}
	resp, err := noRedirectClient.Post(ts.URL+"/login", "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("登录请求失败: %v", err)
	}
	return resp
}

func get(t *testing.T, ts *httptest.Server, path string, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s 失败: %v", path, err)
	}
	return resp
}

func TestAuthDisabledAllowsAnonymous(t *testing.T) {
	_, ts := newTestServer(t, "")

	resp := get(t, ts, "/api/v1/videos")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("未开启认证时应直接访问，实际 %d", resp.StatusCode)
	}

	// 登录入口在关闭认证时回首页
	resp2 := get(t, ts, "/login")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusSeeOther {
		t.Fatalf("关闭认证时 /login 应重定向，实际 %d", resp2.StatusCode)
	}
}

func TestProtectedRoutesRequireLogin(t *testing.T) {
	_, ts := newTestServer(t, "pass123")

	// 页面请求 → 跳转登录
	resp := get(t, ts, "/watch/abc")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("未登录访问页面应跳转登录，实际 %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	// API 请求 → 401 JSON
	resp2 := get(t, ts, "/api/v1/videos")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录访问 API 应 401，实际 %d", resp2.StatusCode)
	}

	// 公共资源放行
	for _, p := range []string{"/login", "/thumb-placeholder.svg", "/static/css/style.css"} {
		r := get(t, ts, p)
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("公共路径 %s 应可匿名访问，实际 %d", p, r.StatusCode)
		}
	}
}

func TestLoginFlow(t *testing.T) {
	_, ts := newTestServer(t, "pass123")

	// 错误密码
	bad := login(t, ts, "wrong", "")
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401，实际 %d", bad.StatusCode)
	}
	if bad.Cookies() != nil {
		for _, c := range bad.Cookies() {
			if c.Name == auth.CookieName && c.Value != "" {
				t.Fatal("登录失败不应下发会话 Cookie")
			}
		}
	}

	// 正确密码 → 302 + 会话 Cookie
	ok := login(t, ts, "pass123", "")
	ok.Body.Close()
	if ok.StatusCode != http.StatusSeeOther {
		t.Fatalf("登录成功应 302，实际 %d", ok.StatusCode)
	}
	var session *http.Cookie
	for _, c := range ok.Cookies() {
		if c.Name == auth.CookieName {
			session = c
		}
	}
	if session == nil || session.HttpOnly != true {
		t.Fatal("登录成功应下发 HttpOnly 会话 Cookie")
	}

	// 携带 Cookie 后可访问受保护 API
	resp := get(t, ts, "/api/v1/videos", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("携带会话后应可访问，实际 %d", resp.StatusCode)
	}
}

func TestLoginOpenRedirectBlocked(t *testing.T) {
	_, ts := newTestServer(t, "pass123")
	resp := login(t, ts, "pass123", "//evil.com/phish")
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Fatalf("外部回跳地址应被拦截到 /，实际 %s", loc)
	}

	// 站内路径保留
	resp2 := login(t, ts, "pass123", "/watch/abc")
	resp2.Body.Close()
	if loc := resp2.Header.Get("Location"); loc != "/watch/abc" {
		t.Fatalf("站内回跳应保留，实际 %s", loc)
	}
}

func TestLogoutClearsSession(t *testing.T) {
	_, ts := newTestServer(t, "pass123")
	ok := login(t, ts, "pass123", "")
	ok.Body.Close()
	session := ok.Cookies()[0]

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/logout", nil)
	req.AddCookie(session)
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("登出应 302，实际 %d", resp.StatusCode)
	}
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("登出应下发过期 Cookie 清除会话")
	}
}

func TestLoginRateLimited(t *testing.T) {
	_, ts := newTestServer(t, "pass123")
	// 限流阈值 3：连续 3 次错误后，即使第 4 次密码正确也被拦截
	for i := 0; i < 3; i++ {
		r := login(t, ts, "bad", "")
		r.Body.Close()
	}
	blocked := login(t, ts, "pass123", "")
	blocked.Body.Close()
	if blocked.StatusCode != http.StatusUnauthorized {
		t.Fatalf("触发限流后登录应被拒绝，实际 %d", blocked.StatusCode)
	}
}
