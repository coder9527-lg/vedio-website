package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestProgressUpsertAndGet(t *testing.T) {
	s := openTestStore(t)

	if got, err := s.GetProgress("v1"); err != nil || got != nil {
		t.Fatalf("空库查询应返回 nil, got=%v err=%v", got, err)
	}

	if err := s.SaveProgress("v1", ProgressUpdate{Position: 12.5, Duration: 100}); err != nil {
		t.Fatalf("SaveProgress 失败: %v", err)
	}
	// 再次上报应 upsert 而非重复插入
	if err := s.SaveProgress("v1", ProgressUpdate{Position: 40, Duration: 100}); err != nil {
		t.Fatalf("二次 SaveProgress 失败: %v", err)
	}

	p, err := s.GetProgress("v1")
	if err != nil || p == nil {
		t.Fatalf("GetProgress 失败: %v", err)
	}
	if p.Position != 40 || p.Duration != 100 || p.VideoID != "v1" {
		t.Fatalf("进度数据不符: %+v", p)
	}
}

func TestProgressNegativeClamped(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveProgress("v1", ProgressUpdate{Position: -5, Duration: -9}); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetProgress("v1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Position != 0 || p.Duration != 0 {
		t.Fatalf("负数应归零, got %+v", p)
	}
}

func TestSaveProgressEmptyID(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveProgress("", ProgressUpdate{Position: 1, Duration: 1}); err == nil {
		t.Fatal("空 videoID 应报错")
	}
}

func TestProgressMapAndRecent(t *testing.T) {
	s := openTestStore(t)
	for i, id := range []string{"a", "b", "c"} {
		if err := s.SaveProgress(id, ProgressUpdate{Position: float64(i), Duration: 10}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond) // 保证 updated_at 可区分
	}

	m, err := s.ProgressMap()
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 {
		t.Fatalf("ProgressMap 应有 3 条, got %d", len(m))
	}
	if m["b"].Position != 1 {
		t.Fatalf("ProgressMap 数据错误: %+v", m["b"])
	}

	recent, err := s.RecentProgress(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].VideoID != "c" || recent[1].VideoID != "b" {
		t.Fatalf("Recent 顺序/数量错误: %+v", recent)
	}
}

func TestDeleteProgress(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveProgress("v1", ProgressUpdate{Position: 10, Duration: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProgress("v1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetProgress("v1"); got != nil {
		t.Fatalf("删除后应查不到, got %+v", got)
	}
}

func TestFavoriteLifecycle(t *testing.T) {
	s := openTestStore(t)

	ok, err := s.IsFavorite("v1")
	if err != nil || ok {
		t.Fatalf("初始应未收藏")
	}

	if err := s.SetFavorite("v1", true); err != nil {
		t.Fatal(err)
	}
	// 重复收藏不应报错也不应产生重复行
	if err := s.SetFavorite("v1", true); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.IsFavorite("v1"); !ok {
		t.Fatal("收藏后 IsFavorite 应为 true")
	}

	favs, err := s.ListFavorites()
	if err != nil || len(favs) != 1 || favs[0].VideoID != "v1" {
		t.Fatalf("ListFavorites 错误: %+v err=%v", favs, err)
	}

	set, err := s.FavoriteSet()
	if err != nil || len(set) != 1 || !set["v1"] {
		t.Fatalf("FavoriteSet 错误: %+v", set)
	}

	if err := s.SetFavorite("v1", false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.IsFavorite("v1"); ok {
		t.Fatal("取消收藏后应为 false")
	}
}

func TestSetFavoriteEmptyID(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetFavorite("", true); err == nil {
		t.Fatal("空 videoID 应报错")
	}
}

func TestPruneOrphans(t *testing.T) {
	s := openTestStore(t)
	_ = s.SaveProgress("keep", ProgressUpdate{Position: 1, Duration: 10})
	_ = s.SaveProgress("gone", ProgressUpdate{Position: 2, Duration: 10})
	_ = s.SetFavorite("keep", true)
	_ = s.SetFavorite("gone", true)

	if err := s.Prune(map[string]bool{"keep": true}); err != nil {
		t.Fatalf("Prune 失败: %v", err)
	}

	if p, _ := s.GetProgress("gone"); p != nil {
		t.Fatal("孤儿进度应被删除")
	}
	if ok, _ := s.IsFavorite("gone"); ok {
		t.Fatal("孤儿收藏应被删除")
	}
	if p, _ := s.GetProgress("keep"); p == nil {
		t.Fatal("保留的进度不应被删除")
	}
	if ok, _ := s.IsFavorite("keep"); !ok {
		t.Fatal("保留的收藏不应被删除")
	}
}

func TestPruneEmptyExistingClearsAll(t *testing.T) {
	s := openTestStore(t)
	_ = s.SaveProgress("v1", ProgressUpdate{Position: 1, Duration: 10})
	_ = s.SetFavorite("v1", true)

	if err := s.Prune(map[string]bool{}); err != nil {
		t.Fatalf("Prune 失败: %v", err)
	}
	m, _ := s.ProgressMap()
	favs, _ := s.ListFavorites()
	if len(m) != 0 || len(favs) != 0 {
		t.Fatal("existing 为空时应清空全部记录")
	}
}

func TestReopenPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProgress("v1", ProgressUpdate{Position: 99, Duration: 200}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFavorite("v1", true); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	p, err := s2.GetProgress("v1")
	if err != nil || p == nil || p.Position != 99 {
		t.Fatalf("重开后进度丢失: %+v err=%v", p, err)
	}
	if ok, _ := s2.IsFavorite("v1"); !ok {
		t.Fatal("重开后收藏丢失")
	}
}

func TestSaveProgressAccumulates(t *testing.T) {
	s := openTestStore(t)

	// 首次上报（会话开始）：watch 增量 5，播放次数 1
	if err := s.SaveProgress("v1", ProgressUpdate{
		Position: 5, Duration: 100, WatchDelta: 5, SessionStart: true,
	}); err != nil {
		t.Fatal(err)
	}
	// 同一会话后续上报：再累计 7，播放次数不变
	if err := s.SaveProgress("v1", ProgressUpdate{
		Position: 12, Duration: 100, WatchDelta: 7, SessionStart: false,
	}); err != nil {
		t.Fatal(err)
	}
	// 新会话（刷新页面）：再累计 3，播放次数 +1
	if err := s.SaveProgress("v1", ProgressUpdate{
		Position: 15, Duration: 100, WatchDelta: 3, SessionStart: true,
	}); err != nil {
		t.Fatal(err)
	}

	p, err := s.GetProgress("v1")
	if err != nil || p == nil {
		t.Fatalf("查询失败: %v", err)
	}
	if p.WatchTimeSeconds != 15 {
		t.Fatalf("累计观看时长应为 15，got %v", p.WatchTimeSeconds)
	}
	if p.PlayCount != 2 {
		t.Fatalf("播放次数应为 2，got %d", p.PlayCount)
	}
	// 位置仍为覆盖式更新
	if p.Position != 15 {
		t.Fatalf("位置应为 15，got %v", p.Position)
	}
}

func TestCompletedSticky(t *testing.T) {
	s := openTestStore(t)

	// 达到看完阈值
	if err := s.SaveProgress("v1", ProgressUpdate{
		Position: 96, Duration: 100, WatchDelta: 1, Completed: true,
	}); err != nil {
		t.Fatal(err)
	}
	// 后续上报未达阈值（用户回看），completed 不应回退
	if err := s.SaveProgress("v1", ProgressUpdate{
		Position: 20, Duration: 100, WatchDelta: 1, Completed: false,
	}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetProgress("v1")
	if p == nil || !p.Completed {
		t.Fatalf("看完标记应黏滞为 true，got %+v", p)
	}

	// 未达阈值的另一条记录应保持 false
	if err := s.SaveProgress("v2", ProgressUpdate{Position: 10, Duration: 100}); err != nil {
		t.Fatal(err)
	}
	p2, _ := s.GetProgress("v2")
	if p2 == nil || p2.Completed {
		t.Fatalf("未看完应为 false，got %+v", p2)
	}
}

func TestWatchStats(t *testing.T) {
	s := openTestStore(t)

	_ = s.SaveProgress("v1", ProgressUpdate{Position: 96, Duration: 100, WatchDelta: 50, SessionStart: true, Completed: true})
	_ = s.SaveProgress("v2", ProgressUpdate{Position: 30, Duration: 100, WatchDelta: 30, SessionStart: true})
	_ = s.SaveProgress("v3", ProgressUpdate{Position: 10, Duration: 100, WatchDelta: 20, SessionStart: true})
	// v3 再补一次会话开始，使总播放次数 = 4
	_ = s.SaveProgress("v3", ProgressUpdate{Position: 12, Duration: 100, WatchDelta: 5, SessionStart: true})
	_ = s.SetFavorite("v1", true)
	_ = s.SetFavorite("v2", true)

	st, err := s.WatchStats()
	if err != nil {
		t.Fatalf("WatchStats 失败: %v", err)
	}
	if st.WatchedVideos != 3 {
		t.Fatalf("观看影片数应为 3，got %d", st.WatchedVideos)
	}
	if st.CompletedVideos != 1 {
		t.Fatalf("看完数应为 1，got %d", st.CompletedVideos)
	}
	if st.TotalWatchSeconds != 105 {
		t.Fatalf("总观看时长应为 105，got %v", st.TotalWatchSeconds)
	}
	if st.TotalPlays != 4 {
		t.Fatalf("总播放次数应为 4，got %d", st.TotalPlays)
	}
	if st.FavoriteCount != 2 {
		t.Fatalf("收藏数应为 2，got %d", st.FavoriteCount)
	}
}

func TestCompletedProgress(t *testing.T) {
	s := openTestStore(t)

	_ = s.SaveProgress("done1", ProgressUpdate{Position: 96, Duration: 100, Completed: true})
	time.Sleep(2 * time.Millisecond)
	_ = s.SaveProgress("done2", ProgressUpdate{Position: 97, Duration: 100, Completed: true})
	_ = s.SaveProgress("watching", ProgressUpdate{Position: 10, Duration: 100, Completed: false})

	list, err := s.CompletedProgress(0)
	if err != nil {
		t.Fatalf("CompletedProgress 失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("应只返回 2 条看完记录，got %d", len(list))
	}
	// 按 updated_at 倒序：后写入的 done2 在前
	if list[0].VideoID != "done2" || list[1].VideoID != "done1" {
		t.Fatalf("倒序错误: %s, %s", list[0].VideoID, list[1].VideoID)
	}
	for _, p := range list {
		if !p.Completed {
			t.Fatalf("返回了未看完记录: %s", p.VideoID)
		}
	}
}

func TestProgressMigrationAddsColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// 新库迁移后应含三个新列，且旧风格（不带统计字段）写入得到默认零值
	if err := s.SaveProgress("v1", ProgressUpdate{Position: 10, Duration: 100}); err != nil {
		t.Fatal(err)
	}
	var cols int
	if err := s.db.QueryRow(
		`SELECT COUNT(1) FROM pragma_table_info('progress')
		 WHERE name IN ('watch_time_seconds','play_count','completed')`,
	).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 3 {
		t.Fatalf("progress 应含 3 个新列，实际 %d", cols)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// 重开同一库：迁移幂等，数据保留且新列为默认值
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	p, err := s2.GetProgress("v1")
	if err != nil || p == nil {
		t.Fatalf("重开查询失败: %v", err)
	}
	if p.WatchTimeSeconds != 0 || p.PlayCount != 0 || p.Completed {
		t.Fatalf("新列默认值错误: %+v", p)
	}
}

// TestWatchEventLifecycle 验证观看历史：会话建一行、同会话累加、倒序查询、清空。
func TestWatchEventLifecycle(t *testing.T) {
	st := openTestStore(t)
	base := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

	id1, err := st.StartWatchEvent("vid-a", base)
	if err != nil {
		t.Fatalf("StartWatchEvent 失败: %v", err)
	}
	if err := st.UpdateWatchEvent(id1, 30, 30, 100, base.Add(30*time.Second)); err != nil {
		t.Fatalf("UpdateWatchEvent 失败: %v", err)
	}
	if err := st.UpdateWatchEvent(id1, 45, 75, 100, base.Add(75*time.Second)); err != nil {
		t.Fatalf("二次更新失败: %v", err)
	}

	// 第二个会话开始时间更早，验证倒序
	id2, err := st.StartWatchEvent("vid-b", base.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("第二个会话失败: %v", err)
	}
	if err := st.UpdateWatchEvent(id2, 10, 10, 0, base.Add(-2*time.Hour+10*time.Second)); err != nil {
		t.Fatalf("更新失败: %v", err)
	}

	events, err := st.ListWatchEvents(0)
	if err != nil {
		t.Fatalf("ListWatchEvents 失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("期望 2 条事件，实际 %d", len(events))
	}
	if events[0].ID != id1 {
		t.Errorf("期望按开始时间倒序，首条 id=%d，实际 %d", id1, events[0].ID)
	}
	if events[0].WatchSeconds != 75 || events[0].Position != 75 {
		t.Errorf("vid-a 累计错误: %+v", events[0])
	}
	if events[1].WatchSeconds != 10 {
		t.Errorf("vid-b 累计错误: %+v", events[1])
	}

	if err := st.ClearWatchEvents(); err != nil {
		t.Fatalf("ClearWatchEvents 失败: %v", err)
	}
	events, _ = st.ListWatchEvents(0)
	if len(events) != 0 {
		t.Errorf("清空后期望 0 条，实际 %d", len(events))
	}
}
