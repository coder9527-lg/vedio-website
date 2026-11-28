package auth

import (
	"testing"
	"time"
)

func newTestAuthenticator(t *testing.T) *Authenticator {
	t.Helper()
	a, err := New("pw", "fixed-secret-key-for-test", time.Hour)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return a
}

func TestRolePermissionMatrix(t *testing.T) {
	// admin 拥有全部权限
	for _, p := range []string{
		PermLibraryRead, PermLibraryScan, PermMetadataEdit, PermContentOrg,
		PermPersonal, PermShareManage, PermTranscodeAdmin, PermSystemHealth, PermUserManage,
	} {
		if !RoleHasPerm(RoleAdmin, p) {
			t.Fatalf("admin 应拥有 %s", p)
		}
	}

	// pm（内容管理员）边界：可整理内容/分享，不可系统/账号/转码任务/扫描
	pmAllowed := []string{PermLibraryRead, PermMetadataEdit, PermContentOrg, PermPersonal, PermShareManage}
	for _, p := range pmAllowed {
		if !RoleHasPerm(RolePM, p) {
			t.Fatalf("pm 应拥有 %s", p)
		}
	}
	for _, p := range []string{PermLibraryScan, PermTranscodeAdmin, PermSystemHealth, PermUserManage} {
		if RoleHasPerm(RolePM, p) {
			t.Fatalf("pm 不应拥有 %s", p)
		}
	}

	// viewer 仅基础观看与个人/内容数据
	for _, p := range []string{PermLibraryRead, PermContentOrg, PermPersonal} {
		if !RoleHasPerm(RoleViewer, p) {
			t.Fatalf("viewer 应拥有 %s", p)
		}
	}
	for _, p := range []string{PermMetadataEdit, PermShareManage, PermUserManage, PermSystemHealth} {
		if RoleHasPerm(RoleViewer, p) {
			t.Fatalf("viewer 不应拥有 %s", p)
		}
	}

	if RoleHasPerm("root", PermLibraryRead) || RoleHasPerm(RoleAdmin, "not.exist") {
		t.Fatal("未知角色或未知权限点应拒绝")
	}
}

func TestUserTokenRoundTrip(t *testing.T) {
	a := newTestAuthenticator(t)
	now := time.Now()
	want := Claims{UserID: 7, Role: RolePM, TokenVersion: 3}

	token := a.IssueUser(now, want)
	got, err := a.VerifyUser(token, now)
	if err != nil {
		t.Fatalf("VerifyUser 失败: %v", err)
	}
	if got != want {
		t.Fatalf("身份不符: got %+v want %+v", got, want)
	}

	// 过期令牌应失效
	if _, err := a.VerifyUser(token, now.Add(2*time.Hour)); err == nil {
		t.Fatal("过期令牌应校验失败")
	}
}

func TestUserTokenTamperRejected(t *testing.T) {
	a := newTestAuthenticator(t)
	token := a.IssueUser(time.Now(), Claims{UserID: 1, Role: RoleViewer, TokenVersion: 1})

	// 篡改角色段（viewer -> admin）后签名不匹配
	if _, err := a.VerifyUser(token+"x", time.Now()); err == nil {
		t.Fatal("被篡改的令牌应失败")
	}
	// 旧单密码格式 exp.sig 只有 2 段，多用户解析应报格式错误
	legacy := a.Issue(time.Now())
	if _, err := a.VerifyUser(legacy, time.Now()); err == nil {
		t.Fatal("旧格式令牌不应通过多用户校验")
	}
}

func TestLegacyTokenStillVerifies(t *testing.T) {
	// 单密码模式的旧令牌经 Verify 仍可用，保证向后兼容
	a := newTestAuthenticator(t)
	token := a.Issue(time.Now())
	if err := a.Verify(token, time.Now()); err != nil {
		t.Fatalf("旧令牌 Verify 失败: %v", err)
	}
}
