package store

import (
	"strings"
	"testing"
)

// TestBookmarkCreateAndList 验证创建、排序（位置升序、同位置 id 升序）与视频隔离。
func TestBookmarkCreateAndList(t *testing.T) {
	st := openTestStore(t)

	// 空库查询返回 nil、无报错
	got, err := st.ListBookmarks("vid-1")
	if err != nil || got != nil {
		t.Fatalf("空库 List 应返回 nil, got=%v err=%v", got, err)
	}

	// vid-1 先打 120s 再打 30s，回读应按位置升序
	bm1, err := st.CreateBookmark("vid-1", 120, "后面的点")
	if err != nil {
		t.Fatalf("创建 120s 书签失败: %v", err)
	}
	bm2, err := st.CreateBookmark("vid-1", 30, "前面的点")
	if err != nil {
		t.Fatalf("创建 30s 书签失败: %v", err)
	}
	if bm1.ID == 0 || bm2.ID == 0 || bm1.ID == bm2.ID {
		t.Fatalf("书签 ID 应为非零且互不相同: %d %d", bm1.ID, bm2.ID)
	}
	list, err := st.ListBookmarks("vid-1")
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(list) != 2 || list[0].Position != 30 || list[1].Position != 120 {
		t.Fatalf("应按位置升序返回两条，实际 %+v", list)
	}

	// 同位置两条按 id 升序
	bm3, err := st.CreateBookmark("vid-1", 30, "同位置")
	if err != nil {
		t.Fatalf("创建同位置书签失败: %v", err)
	}
	list, _ = st.ListBookmarks("vid-1")
	if len(list) != 3 || list[0].ID != bm2.ID || list[1].ID != bm3.ID {
		t.Fatalf("同位置应按 id 升序，实际 %+v", list)
	}

	// 视频隔离：vid-2 的书签不出现在 vid-1 列表
	if _, err := st.CreateBookmark("vid-2", 10, "别的视频"); err != nil {
		t.Fatalf("创建 vid-2 书签失败: %v", err)
	}
	list, _ = st.ListBookmarks("vid-1")
	for _, bm := range list {
		if bm.VideoID != "vid-1" {
			t.Fatalf("vid-1 列表混入他视频数据: %+v", bm)
		}
	}
	if list2, _ := st.ListBookmarks("vid-2"); len(list2) != 1 {
		t.Fatalf("vid-2 应有 1 条书签，实际 %d", len(list2))
	}
}

// TestBookmarkClampAndEmptyVideo 验证负位置钳零与空 videoID 报错。
func TestBookmarkClampAndEmptyVideo(t *testing.T) {
	st := openTestStore(t)

	bm, err := st.CreateBookmark("vid-1", -8.5, "")
	if err != nil {
		t.Fatalf("负位置创建失败: %v", err)
	}
	if bm.Position != 0 {
		t.Fatalf("负位置应钳为 0，实际 %f", bm.Position)
	}

	if _, err := st.CreateBookmark("", 10, ""); err == nil {
		t.Fatal("空 videoID 应返回错误")
	}
}

// TestBookmarkNoteNormalization 验证备注 trim、空串与超长 rune 截断。
func TestBookmarkNoteNormalization(t *testing.T) {
	st := openTestStore(t)

	// 首尾空白被 trim
	bm, err := st.CreateBookmark("vid-1", 10, "  精彩片段  ")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if bm.Note != "精彩片段" {
		t.Fatalf("备注应 trim，实际 %q", bm.Note)
	}

	// 空备注存为空字符串
	bm, err = st.CreateBookmark("vid-1", 20, "   ")
	if err != nil {
		t.Fatalf("空备注创建失败: %v", err)
	}
	if bm.Note != "" {
		t.Fatalf("纯空白备注应存为空串，实际 %q", bm.Note)
	}

	// 超长备注按 rune 截断到上限，中文不被切坏
	long := strings.Repeat("中", maxBookmarkNoteRunes+100)
	bm, err = st.CreateBookmark("vid-1", 30, long)
	if err != nil {
		t.Fatalf("超长备注创建失败: %v", err)
	}
	if runes := []rune(bm.Note); len(runes) != maxBookmarkNoteRunes {
		t.Fatalf("备注应截断为 %d 个 rune，实际 %d", maxBookmarkNoteRunes, len(runes))
	}
}

// TestBookmarkUpdateNote 验证备注修改的命中、归属隔离与不存在 id。
func TestBookmarkUpdateNote(t *testing.T) {
	st := openTestStore(t)

	bm, err := st.CreateBookmark("vid-1", 10, "旧备注")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 命中修改并回读
	ok, err := st.UpdateBookmarkNote(bm.ID, "vid-1", "新备注")
	if err != nil || !ok {
		t.Fatalf("命中修改应返回 true，ok=%v err=%v", ok, err)
	}
	got, err := st.GetBookmark(bm.ID, "vid-1")
	if err != nil || got == nil || got.Note != "新备注" {
		t.Fatalf("回读备注不符: %+v err=%v", got, err)
	}

	// 跨视频归属不命中：vid-2 改不了 vid-1 的书签
	ok, err = st.UpdateBookmarkNote(bm.ID, "vid-2", "越权备注")
	if err != nil || ok {
		t.Fatalf("跨视频修改应返回 (false,nil)，ok=%v err=%v", ok, err)
	}
	got, _ = st.GetBookmark(bm.ID, "vid-1")
	if got.Note != "新备注" {
		t.Fatal("跨视频修改不应落库")
	}

	// 不存在的 id 返回 false 且不报错
	ok, err = st.UpdateBookmarkNote(999999, "vid-1", "x")
	if err != nil || ok {
		t.Fatalf("不存在 id 应返回 (false,nil)，ok=%v err=%v", ok, err)
	}

	// GetBookmark 归属隔离：vid-2 查 vid-1 的书签返回 nil
	if cross, err := st.GetBookmark(bm.ID, "vid-2"); err != nil || cross != nil {
		t.Fatalf("跨视频查询应返回 nil，got=%v err=%v", cross, err)
	}
}

// TestBookmarkDelete 验证删除幂等与归属条件。
func TestBookmarkDelete(t *testing.T) {
	st := openTestStore(t)

	bm, err := st.CreateBookmark("vid-1", 10, "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	// 带错归属条件删不掉
	if err := st.DeleteBookmark(bm.ID, "vid-2"); err != nil {
		t.Fatalf("跨视频删除不应报错: %v", err)
	}
	if got, _ := st.GetBookmark(bm.ID, "vid-1"); got == nil {
		t.Fatal("跨视频删除不应删掉书签")
	}
	// 正确归属删除
	if err := st.DeleteBookmark(bm.ID, "vid-1"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if got, _ := st.GetBookmark(bm.ID, "vid-1"); got != nil {
		t.Fatal("删除后应查不到")
	}
	// 重复删除幂等
	if err := st.DeleteBookmark(bm.ID, "vid-1"); err != nil {
		t.Fatalf("重复删除应幂等，实际报错: %v", err)
	}
}

// TestBookmarkListAll 验证全库查询：跨视频汇总、按创建时间倒序、空库返回 nil。
func TestBookmarkListAll(t *testing.T) {
	st := openTestStore(t)

	// 空库返回 nil、无报错
	got, err := st.ListAllBookmarks()
	if err != nil || got != nil {
		t.Fatalf("空库 ListAll 应返回 nil, got=%v err=%v", got, err)
	}

	first, err := st.CreateBookmark("vid-1", 10, "最早")
	if err != nil {
		t.Fatalf("创建第一条失败: %v", err)
	}
	second, err := st.CreateBookmark("vid-2", 20, "第二条")
	if err != nil {
		t.Fatalf("创建第二条失败: %v", err)
	}
	third, err := st.CreateBookmark("vid-1", 30, "最新")
	if err != nil {
		t.Fatalf("创建第三条失败: %v", err)
	}

	all, err := st.ListAllBookmarks()
	if err != nil {
		t.Fatalf("ListAll 失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("应汇总跨视频全部 3 条，实际 %d", len(all))
	}
	// created_at 倒序：最新创建的排在最前
	wantIDs := []int64{third.ID, second.ID, first.ID}
	for i, want := range wantIDs {
		if all[i].ID != want {
			t.Fatalf("第 %d 条期望书签 id=%d，实际 id=%d（未按创建时间倒序）", i, want, all[i].ID)
		}
	}
}

// TestBookmarkPrune 验证重扫清理：孤儿书签被删、在库视频保留、空合集全清。
func TestBookmarkPrune(t *testing.T) {
	st := openTestStore(t)

	if _, err := st.CreateBookmark("real", 10, ""); err != nil {
		t.Fatalf("写入 real 失败: %v", err)
	}
	if _, err := st.CreateBookmark("ghost", 20, ""); err != nil {
		t.Fatalf("写入 ghost 失败: %v", err)
	}

	// 非空合集：ghost 被清，real 保留
	if err := st.Prune(map[string]bool{"real": true}); err != nil {
		t.Fatalf("Prune 失败: %v", err)
	}
	if list, _ := st.ListBookmarks("real"); len(list) != 1 {
		t.Error("real 的书签应保留")
	}
	if list, _ := st.ListBookmarks("ghost"); len(list) != 0 {
		t.Error("ghost 的孤儿书签应被清理")
	}

	// 空合集：全表清空
	if err := st.Prune(map[string]bool{}); err != nil {
		t.Fatalf("空合集 Prune 失败: %v", err)
	}
	if list, _ := st.ListBookmarks("real"); len(list) != 0 {
		t.Error("空合集清理后应无残留书签")
	}
}
