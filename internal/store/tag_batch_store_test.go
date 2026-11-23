package store

import (
	"errors"
	"testing"
)

// TestBatchSetVideoTags 覆盖批量打标的新增计数、幂等跳过、入参去重与空切片。
func TestBatchSetVideoTags(t *testing.T) {
	s := openTestStore(t)

	// 首次批量：3 个 ID（含一个重复项与空串），去重后 2 个，全部新增
	added, skipped, total, err := s.BatchSetVideoTags("经典", []string{"v1", "v2", "v1", ""})
	if err != nil {
		t.Fatalf("批量打标失败: %v", err)
	}
	if added != 2 || skipped != 0 || total != 2 {
		t.Fatalf("首次批量应 added=2 skipped=0 total=2，got %d/%d/%d", added, skipped, total)
	}

	// 再次批量：v1/v2 已存在计 skipped，v3 新增
	added, skipped, total, err = s.BatchSetVideoTags("经典", []string{"v1", "v2", "v3"})
	if err != nil {
		t.Fatalf("二次批量打标失败: %v", err)
	}
	if added != 1 || skipped != 2 || total != 3 {
		t.Fatalf("二次批量应 added=1 skipped=2 total=3，got %d/%d/%d", added, skipped, total)
	}

	// 空切片：零值且不报错
	if added, skipped, total, err := s.BatchSetVideoTags("经典", nil); err != nil || added != 0 || skipped != 0 || total != 0 {
		t.Fatalf("空切片应全零无错，got %d/%d/%d err=%v", added, skipped, total, err)
	}

	// 非法标签名：哨兵错误
	if _, _, _, err := s.BatchSetVideoTags("   ", []string{"v1"}); !errors.Is(err, ErrInvalidTag) {
		t.Fatalf("空标签名应返回 ErrInvalidTag，got %v", err)
	}

	// 三个视频都应能查到该标签
	tg, err := s.TagByName("经典")
	if err != nil || tg == nil {
		t.Fatalf("标签应存在: %v", err)
	}
	set, err := s.TagVideoSet(tg.ID)
	if err != nil || len(set) != 3 {
		t.Fatalf("标签应关联 3 个视频，got %d err=%v", len(set), err)
	}
}

// TestBatchRemoveVideoTags 覆盖批量取消的计数、空标签回收与未知标签报错。
func TestBatchRemoveVideoTags(t *testing.T) {
	s := openTestStore(t)

	if _, _, _, err := s.BatchSetVideoTags("待重看", []string{"v1", "v2", "v3"}); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	tg, _ := s.TagByName("待重看")

	// 移除两个（含一个本就无关联的 v9 计 skipped）
	removed, skipped, total, err := s.BatchRemoveVideoTags(tg.ID, []string{"v1", "v2", "v9"})
	if err != nil {
		t.Fatalf("批量取消失败: %v", err)
	}
	if removed != 2 || skipped != 1 || total != 3 {
		t.Fatalf("应 removed=2 skipped=1 total=3，got %d/%d/%d", removed, skipped, total)
	}

	// 移除最后一个关联后标签应被自动回收
	if _, _, _, err := s.BatchRemoveVideoTags(tg.ID, []string{"v3"}); err != nil {
		t.Fatalf("移除最后关联失败: %v", err)
	}
	if got, _ := s.TagByName("待重看"); got != nil {
		t.Fatalf("引用归零后标签应被回收，got %+v", got)
	}

	// 未知标签 → ErrNotFound
	if _, _, _, err := s.BatchRemoveVideoTags(9999, []string{"v1"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知标签应返回 ErrNotFound，got %v", err)
	}
}

// TestRenameTag 覆盖普通改名、仅改大小写、重名 409 语义与错误分支。
func TestRenameTag(t *testing.T) {
	s := openTestStore(t)

	if _, _, err := s.SetVideoTag("v1", "经典"); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	if _, _, err := s.SetVideoTag("v2", "佳作"); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	a, _ := s.TagByName("经典")
	b, _ := s.TagByName("佳作")

	// 普通改名成功，关联保留
	renamed, err := s.RenameTag(a.ID, "经典片单")
	if err != nil || renamed.Name != "经典片单" {
		t.Fatalf("普通改名应成功，got %+v err=%v", renamed, err)
	}
	if set, err := s.TagVideoSet(a.ID); err != nil || !set["v1"] {
		t.Fatalf("改名后关联应保留，set=%v err=%v", set, err)
	}

	// 仅改大小写：命中自身，放行
	if _, err := s.RenameTag(a.ID, "经典片单"); err != nil {
		t.Fatalf("同名（仅大小写归一）应允许，got %v", err)
	}

	// 改成另一个已有标签名（含大小写折叠）→ ErrTagConflict，且两边都不变
	if _, err := s.RenameTag(a.ID, "佳作"); !errors.Is(err, ErrTagConflict) {
		t.Fatalf("重名应返回 ErrTagConflict，got %v", err)
	}
	if _, err := s.RenameTag(a.ID, " 佳作 "); !errors.Is(err, ErrTagConflict) {
		t.Fatalf("带空白的重名应返回 ErrTagConflict，got %v", err)
	}
	if cur, _ := s.TagByName("经典片单"); cur == nil || cur.ID != a.ID {
		t.Fatalf("冲突后原标签应保持不变")
	}
	if cur, _ := s.TagByName("佳作"); cur == nil || cur.ID != b.ID {
		t.Fatalf("冲突后目标标签应保持不变")
	}

	// 未知 ID → ErrNotFound
	if _, err := s.RenameTag(9999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知标签应返回 ErrNotFound，got %v", err)
	}
	// 非法名 → ErrInvalidTag
	if _, err := s.RenameTag(a.ID, "  "); !errors.Is(err, ErrInvalidTag) {
		t.Fatalf("非法名应返回 ErrInvalidTag，got %v", err)
	}
}

// TestDeleteTag 验证全局删除级联清理关联，以及重复删除报 ErrNotFound。
func TestDeleteTag(t *testing.T) {
	s := openTestStore(t)

	if _, _, err := s.SetVideoTag("v1", "周末看"); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	if _, _, err := s.SetVideoTag("v2", "周末看"); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	tg, _ := s.TagByName("周末看")

	if err := s.DeleteTag(tg.ID); err != nil {
		t.Fatalf("删除标签失败: %v", err)
	}
	if got, _ := s.TagByName("周末看"); got != nil {
		t.Fatalf("删除后标签应不存在，got %+v", got)
	}
	if tags, _ := s.VideoTags("v1"); len(tags) != 0 {
		t.Fatalf("级联删除后视频不应再有标签，got %+v", tags)
	}
	// 重复删除 → ErrNotFound
	if err := s.DeleteTag(tg.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应返回 ErrNotFound，got %v", err)
	}
}
