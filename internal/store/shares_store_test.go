package store

import (
	"errors"
	"testing"
	"time"
)

func TestShareCreateAndGet(t *testing.T) {
	s := openTestStore(t)

	// 无码分享：哈希应为空串
	sh, err := s.CreateShare("v1", 168, "")
	if err != nil {
		t.Fatalf("CreateShare 失败: %v", err)
	}
	if len(sh.Token) != 32 {
		t.Fatalf("token 应为 32 位 hex，实际 %d 位", len(sh.Token))
	}
	if sh.HasPasscode() {
		t.Fatalf("空提取码不应标记为有码")
	}
	if !sh.VerifyPasscode("") || !sh.VerifyPasscode("任意输入") {
		t.Fatalf("无码分享应恒校验通过")
	}
	got, err := s.GetShare(sh.Token)
	if err != nil || got == nil {
		t.Fatalf("GetShare 失败: %v %v", got, err)
	}
	if got.VideoID != "v1" || got.ExpiresAt != sh.ExpiresAt {
		t.Fatalf("回读字段不一致: %+v", got)
	}

	// 带码分享：只存哈希，且校验正确
	sh2, err := s.CreateShare("v1", 24, "secret123")
	if err != nil {
		t.Fatalf("CreateShare 带码失败: %v", err)
	}
	if !sh2.HasPasscode() || sh2.PasscodeHash == "secret123" {
		t.Fatalf("提取码必须哈希存储，实际 %q", sh2.PasscodeHash)
	}
	if !sh2.VerifyPasscode("secret123") {
		t.Fatalf("正确提取码应校验通过")
	}
	if sh2.VerifyPasscode("wrong") {
		t.Fatalf("错误提取码应校验失败")
	}
}

func TestShareGetMissing(t *testing.T) {
	s := openTestStore(t)
	got, err := s.GetShare("nonexistent-token")
	if err != nil || got != nil {
		t.Fatalf("不存在的 token 应返回 (nil, nil)，got=%v err=%v", got, err)
	}
}

func TestShareListByVideo(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateShare("v1", 24, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // 保证 created_at 可区分顺序
	if _, err := s.CreateShare("v1", 168, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateShare("v2", 720, ""); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSharesByVideo("v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("v1 应有 2 条分享，实际 %d", len(list))
	}
	// 创建时间倒序：后建的排前面
	if !list[0].CreatedAt.After(list[1].CreatedAt) {
		t.Fatalf("列表应按创建时间倒序，got %v", list)
	}
	if list, _ := s.ListSharesByVideo("empty"); len(list) != 0 {
		t.Fatalf("无分享的视频应返回空切片，实际 %d", len(list))
	}
}

func TestShareDelete(t *testing.T) {
	s := openTestStore(t)
	sh, err := s.CreateShare("v1", 24, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteShare(sh.Token); err != nil {
		t.Fatalf("DeleteShare 失败: %v", err)
	}
	if got, _ := s.GetShare(sh.Token); got != nil {
		t.Fatalf("撤销后不应再查到")
	}
	// 重复撤销返回 ErrNotFound
	if err := s.DeleteShare(sh.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复撤销应返回 ErrNotFound，got %v", err)
	}
}

func TestShareDeleteExpired(t *testing.T) {
	s := openTestStore(t)
	// 直接插入一条已过期与一条未过期记录，避免依赖时钟
	now := time.Now().UTC()
	if _, err := s.db.Exec(
		`INSERT INTO shares(token, video_id, passcode_hash, expires_at, created_at) VALUES(?, ?, '', ?, ?)`,
		"expired1", "v1", now.Add(-time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO shares(token, video_id, passcode_hash, expires_at, created_at) VALUES(?, ?, '', ?, ?)`,
		"valid1", "v1", now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	n, err := s.DeleteExpiredShares(now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应删除 1 条过期分享，实际 %d", n)
	}
	if got, _ := s.GetShare("expired1"); got != nil {
		t.Fatalf("过期记录应已删除")
	}
	if got, _ := s.GetShare("valid1"); got == nil {
		t.Fatalf("未过期记录应保留")
	}
	// 再清一次应为 0 条
	if n, err := s.DeleteExpiredShares(now); err != nil || n != 0 {
		t.Fatalf("重复清理应删除 0 条，got %d err=%v", n, err)
	}
}

func TestShareCreateValidation(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateShare("", 24, ""); err == nil {
		t.Fatal("空 videoID 应报错")
	}
	if _, err := s.CreateShare("v1", 0, ""); err == nil {
		t.Fatal("非正有效期应报错")
	}
}
