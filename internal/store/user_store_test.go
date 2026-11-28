package store

import (
	"database/sql"
	"errors"
	"testing"
)

func TestUserCreateGetAndPassword(t *testing.T) {
	s := openTestStore(t)

	u, err := s.CreateUser("alice", "爱丽丝", "secret123", RoleViewer)
	if err != nil {
		t.Fatalf("CreateUser 失败: %v", err)
	}
	if u.ID <= 0 || u.Role != RoleViewer || u.TokenVersion != 1 {
		t.Fatalf("新建用户字段错误: %+v", u)
	}
	if !u.CheckPassword("secret123") {
		t.Fatal("正确密码应校验通过")
	}
	if u.CheckPassword("wrong") {
		t.Fatal("错误密码不应通过")
	}

	got, err := s.GetUserByUsername("alice")
	if err != nil || got == nil || got.ID != u.ID {
		t.Fatalf("GetUserByUsername 错误: %+v err=%v", got, err)
	}
	byID, err := s.GetUserByID(u.ID)
	if err != nil || byID == nil || byID.Username != "alice" {
		t.Fatalf("GetUserByID 错误: %+v err=%v", byID, err)
	}
}

func TestUserDuplicateAndValidation(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateUser("bob", "", "pw123456", RolePM); err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	if _, err := s.CreateUser("bob", "", "pw123456", RolePM); !errors.Is(err, ErrDuplicateUser) {
		t.Fatalf("重名应返回 ErrDuplicateUser，got %v", err)
	}
	if _, err := s.CreateUser("", "", "pw", RoleViewer); err == nil {
		t.Fatal("空用户名应报错")
	}
	if _, err := s.CreateUser("carol", "", "pw", "root") ; err == nil {
		t.Fatal("非法角色应报错")
	}
	if !ValidRole(RoleAdmin) || ValidRole("root") {
		t.Fatal("角色白名单判定错误")
	}
}

func TestUserUpdatePasswordBumpsVersion(t *testing.T) {
	s := openTestStore(t)
	u, _ := s.CreateUser("dave", "", "oldpw", RoleViewer)

	if err := s.UpdatePassword(u.ID, "newpw"); err != nil {
		t.Fatalf("UpdatePassword 失败: %v", err)
	}
	got, _ := s.GetUserByID(u.ID)
	if got.CheckPassword("oldpw") || !got.CheckPassword("newpw") {
		t.Fatal("改密后密码校验不符")
	}
	if got.TokenVersion != 2 {
		t.Fatalf("改密后 token_version 应递增为 2，got %d", got.TokenVersion)
	}
}

func TestLastAdminProtection(t *testing.T) {
	s := openTestStore(t)
	admin, _ := s.CreateUser("root", "", "pw", RoleAdmin)
	other, _ := s.CreateUser("eve", "", "pw", RoleViewer)

	// 唯一管理员不能降级、不能删除
	if err := s.UpdateRole(admin.ID, RoleViewer); err == nil {
		t.Fatal("降级最后一个 admin 应被拒绝")
	}
	if err := s.DeleteUser(admin.ID); err == nil {
		t.Fatal("删除最后一个 admin 应被拒绝")
	}

	// 增加第二个管理员后，原 admin 可降级/删除
	if err := s.UpdateRole(other.ID, RoleAdmin); err != nil {
		t.Fatalf("提升管理员失败: %v", err)
	}
	if err := s.UpdateRole(admin.ID, RoleViewer); err != nil {
		t.Fatalf("有两个 admin 时降级应成功: %v", err)
	}
	admins, _ := s.CountAdmins()
	if admins != 1 {
		t.Fatalf("剩余管理员应为 1，got %d", admins)
	}
}

func TestDeleteUserAndList(t *testing.T) {
	s := openTestStore(t)
	admin, _ := s.CreateUser("root", "", "pw", RoleAdmin)
	u, _ := s.CreateUser("frank", "", "pw", RoleViewer)

	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatalf("删除普通用户失败: %v", err)
	}
	if got, _ := s.GetUserByID(u.ID); got != nil {
		t.Fatal("删除后应查不到")
	}
	if err := s.DeleteUser(999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("删除不存在用户应返回 sql.ErrNoRows，got %v", err)
	}
	list, err := s.ListUsers()
	if err != nil || len(list) != 1 || list[0].ID != admin.ID {
		t.Fatalf("ListUsers 错误: %+v err=%v", list, err)
	}
	if n, _ := s.CountUsers(); n != 1 {
		t.Fatalf("CountUsers 应为 1，got %d", n)
	}
}
