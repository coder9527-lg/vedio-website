package store

import "testing"

// TestWatchLaterLifecycle 验证稍后再看：空库、加入幂等、集合/列表查询、移除幂等、空 ID 报错。
func TestWatchLaterLifecycle(t *testing.T) {
	st := openTestStore(t)

	// 空库：不在清单、列表为空、集合为空
	if ok, err := st.IsWatchLater("vid-1"); err != nil || ok {
		t.Fatalf("空库 IsWatchLater 应返回 false, ok=%v err=%v", ok, err)
	}
	if items, err := st.ListWatchLater(); err != nil || len(items) != 0 {
		t.Fatalf("空库列表应为空, items=%v err=%v", items, err)
	}
	if set, err := st.WatchLaterSet(); err != nil || len(set) != 0 {
		t.Fatalf("空库集合应为空, set=%v err=%v", set, err)
	}

	// 加入两个视频
	if err := st.AddWatchLater("vid-1"); err != nil {
		t.Fatalf("AddWatchLater vid-1 失败: %v", err)
	}
	if err := st.AddWatchLater("vid-2"); err != nil {
		t.Fatalf("AddWatchLater vid-2 失败: %v", err)
	}
	// 重复加入不报错且不产生重复行
	if err := st.AddWatchLater("vid-1"); err != nil {
		t.Fatalf("重复 AddWatchLater 应幂等: %v", err)
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(1) FROM watch_later`).Scan(&n); err != nil {
		t.Fatalf("统计行数失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("重复加入后应仍为 2 行，实际 %d", n)
	}

	if ok, _ := st.IsWatchLater("vid-1"); !ok {
		t.Error("vid-1 应在稍后再看清单中")
	}
	set, err := st.WatchLaterSet()
	if err != nil || len(set) != 2 || !set["vid-1"] || !set["vid-2"] {
		t.Fatalf("WatchLaterSet 不符: %v err=%v", set, err)
	}

	// 列表按加入时间倒序：vid-2 晚于 vid-1，应排首位
	items, err := st.ListWatchLater()
	if err != nil {
		t.Fatalf("ListWatchLater 失败: %v", err)
	}
	if len(items) != 2 || items[0].VideoID != "vid-2" || items[1].VideoID != "vid-1" {
		t.Fatalf("列表顺序应为 vid-2,vid-1，实际 %+v", items)
	}

	// 移除：存在则删除，再查为 false；移除不存在的 ID 幂等不报错
	if err := st.RemoveWatchLater("vid-1"); err != nil {
		t.Fatalf("RemoveWatchLater 失败: %v", err)
	}
	if ok, _ := st.IsWatchLater("vid-1"); ok {
		t.Error("vid-1 移除后不应在清单中")
	}
	if err := st.RemoveWatchLater("not-exist"); err != nil {
		t.Fatalf("移除不存在的记录应幂等，实际报错: %v", err)
	}

	// 空 videoID 报错
	if err := st.AddWatchLater(""); err == nil {
		t.Error("空 videoID 加入应报错")
	}
	if err := st.RemoveWatchLater(""); err == nil {
		t.Error("空 videoID 移除应报错")
	}
}

// TestWatchLaterBatchRemove 验证已看自动归档使用的批量移除与空切片短路。
func TestWatchLaterBatchRemove(t *testing.T) {
	st := openTestStore(t)

	for _, id := range []string{"a", "b", "c"} {
		if err := st.AddWatchLater(id); err != nil {
			t.Fatalf("加入 %s 失败: %v", id, err)
		}
	}
	// 空切片不应报错也不应清表
	if err := st.RemoveWatchLaterByIDs(nil); err != nil {
		t.Fatalf("空切片批量移除应短路: %v", err)
	}
	if n, _ := st.watchLaterCount(); n != 3 {
		t.Fatalf("空切片后应仍为 3 行，实际 %d", n)
	}

	if err := st.RemoveWatchLaterByIDs([]string{"a", "c"}); err != nil {
		t.Fatalf("批量移除失败: %v", err)
	}
	if n, _ := st.watchLaterCount(); n != 1 {
		t.Fatalf("批量移除后应剩 1 行，实际 %d", n)
	}
	if ok, _ := st.IsWatchLater("b"); !ok {
		t.Error("未在批量移除列表中的 b 应保留")
	}
}

// watchLaterCount 返回表内行数，仅供本测试断言使用。
func (s *Store) watchLaterCount() (int, error) {
	var n int
	err := s.DB().QueryRow(`SELECT COUNT(1) FROM watch_later`).Scan(&n)
	return n, err
}
