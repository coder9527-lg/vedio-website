package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/auth"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// newRBACTestServer 构造仅用于鉴权测试的服务：无媒体依赖，带 store 与访问密码。
func newRBACTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rbac.db"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	authn, err := auth.New("pass-123", "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("创建认证器失败: %v", err)
	}
	srv, err := New(nil, nil, nil, st, authn, nil, false)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return srv
}

// probeChain 组装 认证闸口 + 权限闸口 的最小链路，返回供 httptest 调用的 handler。
func probeChain(s *Server, perm string) http.Handler {
	h := s.requirePerm(perm, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return s.requireAuth(http.HandlerFunc(h))
}

func (s *Server) issueUserCookie(t *testing.T, r *http.Request, w http.ResponseWriter, c auth.Claims) {
	t.Helper()
	s.authenticator.SetUserCookie(w, r, false, c)
}

// TestRBACSinglePasswordTreatedAsAdmin 单密码模式：旧格式令牌视同内置 admin，全部放行。
func TestRBACSinglePasswordTreatedAsAdmin(t *testing.T) {
	srv := newRBACTestServer(t)
	if srv.multiUserEnabled() {
		t.Fatal("无账号时不应进入多用户模式")
	}
	handler := probeChain(srv, auth.PermUserManage)

	// 未携带 Cookie：API 返回 401
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 API 应 401，实际 %d", rec.Code)
	}

	// 旧格式令牌（单密码登录产物）：视同 admin，200
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	loginW := httptest.NewRecorder()
	srv.authenticator.SetCookie(loginW, req, false)
	req.AddCookie(loginW.Result().Cookies()[0])
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("单密码内置 admin 应放行，实际 %d", rec.Code)
	}
}

// TestRBACMultiUserRoleEnforcement 多用户模式：viewer/pm/admin 按矩阵鉴权。
func TestRBACMultiUserRoleEnforcement(t *testing.T) {
	srv := newRBACTestServer(t)
	st := srv.store

	admin, err := st.CreateUser("root", "超管", "pw", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	pm, err := st.CreateUser("alice", "内容管理员", "pw", store.RolePM)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := st.CreateUser("bob", "观看者", "pw", store.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	srv.refreshMultiUser()
	if !srv.multiUserEnabled() {
		t.Fatal("存在账号后应进入多用户模式")
	}

	loginCookie := func(u *store.User) *http.Cookie {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		srv.issueUserCookie(t, req, w, auth.Claims{
			UserID: u.ID, Role: u.Role, TokenVersion: u.TokenVersion,
		})
		return w.Result().Cookies()[0]
	}

	cases := []struct {
		name string
		user *store.User
		perm string
		want int
	}{
		{"viewer 可读", viewer, auth.PermLibraryRead, http.StatusOK},
		{"viewer 禁扫描", viewer, auth.PermLibraryScan, http.StatusForbidden},
		{"viewer 禁编辑元信息", viewer, auth.PermMetadataEdit, http.StatusForbidden},
		{"viewer 禁用户管理", viewer, auth.PermUserManage, http.StatusForbidden},
		{"pm 可编辑元信息", pm, auth.PermMetadataEdit, http.StatusOK},
		{"pm 可管分享", pm, auth.PermShareManage, http.StatusOK},
		{"pm 禁扫描", pm, auth.PermLibraryScan, http.StatusForbidden},
		{"pm 禁转码任务", pm, auth.PermTranscodeAdmin, http.StatusForbidden},
		{"pm 禁系统健康", pm, auth.PermSystemHealth, http.StatusForbidden},
		{"pm 禁用户管理", pm, auth.PermUserManage, http.StatusForbidden},
		{"admin 全权", admin, auth.PermUserManage, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := probeChain(srv, tc.perm)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
			req.AddCookie(loginCookie(tc.user))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s 期望 %d，实际 %d", tc.name, tc.want, rec.Code)
			}
		})
	}
}

// TestRBACPageForbiddenRendersHTML 页面请求权限不足返回 403 页面而非 JSON。
func TestRBACPageForbiddenRendersHTML(t *testing.T) {
	srv := newRBACTestServer(t)
	viewer, err := srv.store.CreateUser("bob", "观看者", "pw", store.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	srv.refreshMultiUser()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data/health", nil)
	srv.issueUserCookie(t, req, w, auth.Claims{
		UserID: viewer.ID, Role: viewer.Role, TokenVersion: viewer.TokenVersion,
	})
	req.AddCookie(w.Result().Cookies()[0])

	handler := probeChain(srv, auth.PermSystemHealth)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("页面无权限应 403，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !contains(ct, "text/html") {
		t.Fatalf("页面 403 应渲染 HTML，实际 Content-Type=%s", ct)
	}
}

// TestRBACTokenVersionBumpInvalidatesCookie 改角色/改密递增 token_version，旧 Cookie 立即失效。
func TestRBACTokenVersionBumpInvalidatesCookie(t *testing.T) {
	srv := newRBACTestServer(t)
	pm, err := srv.store.CreateUser("alice", "内容管理员", "pw", store.RolePM)
	if err != nil {
		t.Fatal(err)
	}
	srv.refreshMultiUser()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.issueUserCookie(t, req, w, auth.Claims{
		UserID: pm.ID, Role: pm.Role, TokenVersion: pm.TokenVersion,
	})
	cookie := w.Result().Cookies()[0]

	// 提升为 admin，token_version 自增，旧 Cookie 携带的版本过期
	if err := srv.store.UpdateRole(pm.ID, store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	handler := probeChain(srv, auth.PermUserManage)
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	req2.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req2)
	// 身份解析失败按未登录处理：API 401
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("旧版本 Cookie 应失效并 401，实际 %d", rec.Code)
	}
}

// TestRBACNoAuthModeAllowsAll 未开启访问密码时维持全站开放，权限闸口不拦截。
func TestRBACNoAuthModeAllowsAll(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "noauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(nil, nil, nil, st, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := probeChain(srv, auth.PermUserManage)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("无认证模式应直接放行，实际 %d", rec.Code)
	}
}
