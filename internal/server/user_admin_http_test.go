package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/auth"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// newMultiUserServer 构造带 store + 访问密码的服务，并预置一个 admin 账号进入多用户模式。
func newMultiUserServer(t *testing.T) (*Server, *httptest.Server, *store.User) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/mu.db")
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	authn, err := auth.New("pass-123", "test-secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(nil, nil, nil, st, authn, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := st.CreateUser("root", "超管", "admin-pw", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	srv.refreshMultiUser()
	ts := newHTTPTestServer(t, srv.Handler())
	return srv, ts, admin
}

// userLogin 走真实 /login 表单登录，返回响应以便提取会话 Cookie。
func userLogin(t *testing.T, ts *httptest.Server, username, password string) *http.Response {
	t.Helper()
	form := url.Values{"username": {username}, "password": {password}}
	resp, err := noRedirectClient.Post(ts.URL+"/login", "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("登录请求失败: %v", err)
	}
	return resp
}

func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatal("响应未携带会话 Cookie")
	return nil
}

func jsonReq(t *testing.T, ts *httptest.Server, method, path string, body any, cookie *http.Cookie) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, _ := http.NewRequest(method, ts.URL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", method, path, err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestMultiUserLoginFlow 多用户登录：错误凭证 401，正确 302 且按角色鉴权。
func TestMultiUserLoginFlow(t *testing.T) {
	_, ts, admin := newMultiUserServer(t)

	// 登录页在多用户模式应出现用户名输入框
	resp := get(t, ts, "/login")
	body := readAllString(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `name="username"`) {
		t.Fatalf("多用户模式登录页应含用户名输入框，status=%d", resp.StatusCode)
	}

	// 错误密码
	bad := userLogin(t, ts, admin.Username, "wrong")
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401，实际 %d", bad.StatusCode)
	}

	// 不存在的用户名同样 401（防账号枚举，提示一致）
	badUser := userLogin(t, ts, "ghost", "x")
	badUser.Body.Close()
	if badUser.StatusCode != http.StatusUnauthorized {
		t.Fatalf("不存在用户应 401，实际 %d", badUser.StatusCode)
	}

	// 正确登录
	ok := userLogin(t, ts, admin.Username, "admin-pw")
	cookie := sessionCookie(t, ok)
	ok.Body.Close()
	if ok.StatusCode != http.StatusSeeOther {
		t.Fatalf("登录成功应 302，实际 %d", ok.StatusCode)
	}

	// admin 可访问用户管理页
	page := get(t, ts, "/admin/users", cookie)
	pageBody := readAllString(t, page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || !strings.Contains(pageBody, admin.Username) {
		t.Fatalf("admin 应能打开用户管理页，status=%d", page.StatusCode)
	}

	// 未登录访问管理写接口应 401
	code, _ := jsonReq(t, ts, http.MethodPost, "/api/v1/admin/users",
		map[string]string{"username": "x", "password": "y", "role": "viewer"}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("未登录写接口应 401，实际 %d", code)
	}
}

// TestUserAdminCRUDRBAC 覆盖创建、非法角色、重名、改角色、重置密码、删除与越权 403。
func TestUserAdminCRUDRBAC(t *testing.T) {
	_, ts, admin := newMultiUserServer(t)
	adminLogin := userLogin(t, ts, admin.Username, "admin-pw")
	cookie := sessionCookie(t, adminLogin)
	adminLogin.Body.Close()

	// 创建 pm
	code, body := jsonReq(t, ts, http.MethodPost, "/api/v1/admin/users",
		map[string]string{"username": "alice", "displayName": "PM", "password": "pw1", "role": "pm"}, cookie)
	if code != http.StatusCreated {
		t.Fatalf("创建 pm 应 201，实际 %d body=%v", code, body)
	}
	aliceID := int64(body["id"].(float64))

	// 重名 → 409
	if code, body = jsonReq(t, ts, http.MethodPost, "/api/v1/admin/users",
		map[string]string{"username": "alice", "password": "pw2", "role": "viewer"}, cookie); code != http.StatusConflict {
		t.Fatalf("重名应 409，实际 %d body=%v", code, body)
	}

	// 非法角色 → 400
	if code, _ = jsonReq(t, ts, http.MethodPost, "/api/v1/admin/users",
		map[string]string{"username": "bob", "password": "pw", "role": "superuser"}, cookie); code != http.StatusBadRequest {
		t.Fatalf("非法角色应 400，实际 %d", code)
	}

	// 创建 viewer
	code, body = jsonReq(t, ts, http.MethodPost, "/api/v1/admin/users",
		map[string]string{"username": "bob", "password": "pw", "role": "viewer"}, cookie)
	if code != http.StatusCreated {
		t.Fatalf("创建 viewer 应 201，实际 %d", code)
	}
	bobID := int64(body["id"].(float64))

	// viewer 登录后越权访问管理页 → 403
	bobLogin := userLogin(t, ts, "bob", "pw")
	bobCookie := sessionCookie(t, bobLogin)
	bobLogin.Body.Close()
	bobPage := get(t, ts, "/admin/users", bobCookie)
	bobPage.Body.Close()
	if bobPage.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer 访问用户管理应 403，实际 %d", bobPage.StatusCode)
	}
	// viewer 越权创建 → 403 JSON
	if code, _ = jsonReq(t, ts, http.MethodPost, "/api/v1/admin/users",
		map[string]string{"username": "x", "password": "y", "role": "viewer"}, bobCookie); code != http.StatusForbidden {
		t.Fatalf("viewer 越权写接口应 403，实际 %d", code)
	}

	// pm 同样无 user.manage → 403
	aliceLogin := userLogin(t, ts, "alice", "pw1")
	aliceCookie := sessionCookie(t, aliceLogin)
	aliceLogin.Body.Close()
	if code, _ = jsonReq(t, ts, http.MethodPost, "/api/v1/admin/users",
		map[string]string{"username": "y", "password": "z", "role": "viewer"}, aliceCookie); code != http.StatusForbidden {
		t.Fatalf("pm 越权用户管理应 403，实际 %d", code)
	}

	// 改角色 viewer→pm
	if code, _ = jsonReq(t, ts, http.MethodPatch,
		"/api/v1/admin/users/"+strconv.FormatInt(bobID, 10)+"/role",
		map[string]string{"role": "pm"}, cookie); code != http.StatusOK {
		t.Fatalf("改角色应 200，实际 %d", code)
	}

	// 改角色后 bob 旧 Cookie（角色与版本均过期）应失效
	probe := get(t, ts, "/api/v1/videos", bobCookie)
	probe.Body.Close()
	if probe.StatusCode != http.StatusUnauthorized {
		t.Fatalf("改角色后旧 Cookie 应失效 401，实际 %d", probe.StatusCode)
	}

	// 重置密码
	if code, _ = jsonReq(t, ts, http.MethodPost,
		"/api/v1/admin/users/"+strconv.FormatInt(aliceID, 10)+"/password",
		map[string]string{"password": "new-pw"}, cookie); code != http.StatusOK {
		t.Fatalf("重置密码应 200，实际 %d", code)
	}
	// 旧密码登录失败，新密码成功
	old := userLogin(t, ts, "alice", "pw1")
	old.Body.Close()
	if old.StatusCode != http.StatusUnauthorized {
		t.Fatalf("重置后旧密码应 401，实际 %d", old.StatusCode)
	}
	fresh := userLogin(t, ts, "alice", "new-pw")
	fresh.Body.Close()
	if fresh.StatusCode != http.StatusSeeOther {
		t.Fatalf("新密码应可登录 302，实际 %d", fresh.StatusCode)
	}

	// 删除普通账号
	if code, _ = jsonReq(t, ts, http.MethodDelete,
		"/api/v1/admin/users/"+strconv.FormatInt(bobID, 10), nil, cookie); code != http.StatusOK {
		t.Fatalf("删除普通账号应 200，实际 %d", code)
	}

	// 最后一个 admin 禁止降级与删除
	if code, body = jsonReq(t, ts, http.MethodPatch,
		"/api/v1/admin/users/"+strconv.FormatInt(admin.ID, 10)+"/role",
		map[string]string{"role": "viewer"}, cookie); code != http.StatusConflict {
		t.Fatalf("降级最后 admin 应 409，实际 %d body=%v", code, body)
	}
	if code, body = jsonReq(t, ts, http.MethodDelete,
		"/api/v1/admin/users/"+strconv.FormatInt(admin.ID, 10), nil, cookie); code != http.StatusConflict {
		t.Fatalf("删除最后 admin 应 409，实际 %d body=%v", code, body)
	}
}

// TestAdminBootstrap 模拟 main 引导装配：users 表为空时按环境变量创建首个 admin，
// 服务随即进入多用户模式且该账号可登录；再次引导（表非空）必须忽略、不覆盖既有账号。
func TestAdminBootstrap(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/bootstrap.db")
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// 未显式配置 AUTH_PASSWORD 时，main 用占位口令兜底构造认证器（多用户下不参与登录）
	authn, err := auth.New("bootstrap-disabled-single-password", "test-secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(nil, nil, nil, st, authn, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	// 引导前：空表、单密码模式
	if n, _ := st.CountUsers(); n != 0 {
		t.Fatalf("引导前 users 表应为空，实际 %d", n)
	}
	if srv.multiUserEnabled() {
		t.Fatal("空表不应处于多用户模式")
	}

	// 复刻 main 引导逻辑：仅在空表且用户名/密码齐全时创建
	adminUser, adminPass := "boot-admin", "boot-secret"
	if n, _ := st.CountUsers(); n == 0 {
		if _, err := st.CreateUser(adminUser, "管理员", adminPass, store.RoleAdmin); err != nil {
			t.Fatalf("引导首个管理员失败: %v", err)
		}
	}
	srv.refreshMultiUser()
	if !srv.multiUserEnabled() {
		t.Fatal("引导出 admin 后应进入多用户模式")
	}

	ts := newHTTPTestServer(t, srv.Handler())

	// 登录页应展示用户名输入框；占位单密码无法登录，真实 admin 账号可以
	resp := get(t, ts, "/login")
	loginPage := readAllString(t, resp.Body)
	resp.Body.Close()
	if !strings.Contains(loginPage, `name="username"`) {
		t.Fatal("多用户模式登录页应含用户名输入框")
	}
	ok := userLogin(t, ts, adminUser, adminPass)
	cookie := sessionCookie(t, ok)
	ok.Body.Close()
	if ok.StatusCode != http.StatusSeeOther {
		t.Fatalf("引导 admin 应可登录 302，实际 %d", ok.StatusCode)
	}

	// 再次引导：表非空必须忽略，账号数保持 1
	if n, _ := st.CountUsers(); n != 1 {
		t.Fatalf("重复引导不应新增账号，实际 %d", n)
	}
	if _, err := st.CreateUser(adminUser, "被覆盖", "x", store.RoleAdmin); err != store.ErrDuplicateUser {
		t.Fatalf("同名账号应仍存在且拒绝重复创建，err=%v", err)
	}

	// 引导出的 admin 可访问用户管理页
	page := get(t, ts, "/admin/users", cookie)
	page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("引导 admin 应能打开用户管理页，实际 %d", page.StatusCode)
	}
}
