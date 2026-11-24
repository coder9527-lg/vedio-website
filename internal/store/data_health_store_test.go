package store

import (
	"testing"
	"time"
)

// seedHealthFixture 向七张表各插入一条指向 ghostID 的孤儿行，
// 并向 progress/favorites 各插一条存活视频 liveID 的对照行，返回孤儿报告期望值。
// 标签关联额外建立「独占标签」（仅 ghost 引用）与「共享标签」（ghost 与 live 同时引用）。
func seedHealthFixture(t *testing.T, s *Store, ghostID, liveID string) OrphanReport {
	t.Helper()
	now := time.Now().UTC()
	db := s.DB()

	// 存活对照行：清理孤儿后这些行必须保留
	if err := s.SaveProgress(liveID, ProgressUpdate{Position: 10, Duration: 100}); err != nil {
		t.Fatalf("写入存活进度失败: %v", err)
	}
	if err := s.SetFavorite(liveID, true); err != nil {
		t.Fatalf("写入存活收藏失败: %v", err)
	}

	// progress / favorites
	if _, err := db.Exec(
		`INSERT INTO progress(video_id, position, duration, watch_time_seconds, play_count, completed, updated_at)
		 VALUES(?, 0, 0, 0, 0, 0, ?)`, ghostID, now); err != nil {
		t.Fatalf("插入孤儿进度失败: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO favorites(video_id, added_at) VALUES(?, ?)`, ghostID, now); err != nil {
		t.Fatalf("插入孤儿收藏失败: %v", err)
	}

	// playlist_item：先建播放列表表头（清理后表头必须保留），再挂孤儿与存活条目
	pl := mustCreatePlaylist(t, s, "体检测试列表")
	if _, err := db.Exec(
		`INSERT INTO playlist_item(playlist_id, video_id, sort_order, added_at) VALUES(?, ?, 0, ?)`,
		pl.ID, ghostID, now); err != nil {
		t.Fatalf("插入孤儿播放列表条目失败: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO playlist_item(playlist_id, video_id, sort_order, added_at) VALUES(?, ?, 1, ?)`,
		pl.ID, liveID, now); err != nil {
		t.Fatalf("插入存活播放列表条目失败: %v", err)
	}

	// bookmarks：两条孤儿 + 一条存活，验证计数不是固定 1
	if _, err := db.Exec(
		`INSERT INTO bookmarks(video_id, position, note, created_at) VALUES(?, 30, '', ?)`, ghostID, now); err != nil {
		t.Fatalf("插入孤儿书签失败: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO bookmarks(video_id, position, note, created_at) VALUES(?, 60, '', ?)`, ghostID, now); err != nil {
		t.Fatalf("插入第二条孤儿书签失败: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO bookmarks(video_id, position, note, created_at) VALUES(?, 12, '', ?)`, liveID, now); err != nil {
		t.Fatalf("插入存活书签失败: %v", err)
	}

	// skip_settings
	if _, err := db.Exec(
		`INSERT INTO skip_settings(video_id, intro_seconds, outro_seconds, enabled, updated_at)
		 VALUES(?, 15, 20, 1, ?)`, ghostID, now); err != nil {
		t.Fatalf("插入孤儿片头片尾设置失败: %v", err)
	}

	// watch_events：两条孤儿会话
	if _, err := db.Exec(
		`INSERT INTO watch_events(video_id, started_at, updated_at, watch_seconds, position, duration)
		 VALUES(?, ?, ?, 100, 10, 100)`, ghostID, now, now); err != nil {
		t.Fatalf("插入孤儿观看历史失败: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO watch_events(video_id, started_at, updated_at, watch_seconds, position, duration)
		 VALUES(?, ?, ?, 50, 5, 100)`, ghostID, now, now); err != nil {
		t.Fatalf("插入第二条孤儿观看历史失败: %v", err)
	}

	// video_tag：独占标签只挂 ghost；共享标签同时挂 ghost 与 live
	if _, _, err := s.SetVideoTag(ghostID, "独占标签"); err != nil {
		t.Fatalf("写入孤儿标签关联失败: %v", err)
	}
	if _, _, err := s.SetVideoTag(ghostID, "共享标签"); err != nil {
		t.Fatalf("写入共享标签关联失败: %v", err)
	}
	if _, _, err := s.SetVideoTag(liveID, "共享标签"); err != nil {
		t.Fatalf("写入存活标签关联失败: %v", err)
	}

	return OrphanReport{
		Progress:     1,
		Favorites:    1,
		PlaylistItem: 1,
		Bookmarks:    2,
		SkipSettings: 1,
		WatchEvents:  2,
		VideoTag:     2,
	}
}

func TestOrphanReportHealthyLibrary(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveProgress("v1", ProgressUpdate{Position: 1, Duration: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFavorite("v1", true); err != nil {
		t.Fatal(err)
	}
	report, err := s.OrphanReport(map[string]bool{"v1": true})
	if err != nil {
		t.Fatalf("OrphanReport 失败: %v", err)
	}
	if report.Total() != 0 {
		t.Fatalf("健康库不应有孤儿，实际 %+v", report)
	}
}

func TestOrphanReportCounts(t *testing.T) {
	s := openTestStore(t)
	want := seedHealthFixture(t, s, "ghost-id", "live-id")
	got, err := s.OrphanReport(map[string]bool{"live-id": true})
	if err != nil {
		t.Fatalf("OrphanReport 失败: %v", err)
	}
	if got != want {
		t.Fatalf("孤儿计数不符:\n got  %+v\n want %+v", got, want)
	}
	if got.Total() != want.Total() {
		t.Fatalf("总数不符: got %d want %d", got.Total(), want.Total())
	}
}

func TestDeleteOrphansByTableIsolation(t *testing.T) {
	s := openTestStore(t)
	want := seedHealthFixture(t, s, "ghost-id", "live-id")
	existing := map[string]bool{"live-id": true}

	// 逐表清理，每次只能影响目标表；表名与报告字段一一对应
	cases := []struct {
		table string
		count int64
	}{
		{"progress", want.Progress},
		{"favorites", want.Favorites},
		{"playlist_item", want.PlaylistItem},
		{"bookmarks", want.Bookmarks},
		{"skip_settings", want.SkipSettings},
		{"watch_events", want.WatchEvents},
		{"video_tag", want.VideoTag},
	}
	for _, c := range cases {
		removed, err := s.DeleteOrphansByTable(c.table, existing)
		if err != nil {
			t.Fatalf("清理 %s 失败: %v", c.table, err)
		}
		if removed != c.count {
			t.Fatalf("%s 删除行数不符: got %d want %d", c.table, removed, c.count)
		}
		report, err := s.OrphanReport(existing)
		if err != nil {
			t.Fatalf("清理后体检失败: %v", err)
		}
		if *report.orphanField(c.table) != 0 {
			t.Fatalf("%s 清理后仍有孤儿: %+v", c.table, report)
		}
	}

	// 存活对照行保留
	if p, err := s.GetProgress("live-id"); err != nil || p == nil {
		t.Fatalf("存活进度应保留: p=%v err=%v", p, err)
	}
	if fav, err := s.IsFavorite("live-id"); err != nil || !fav {
		t.Fatalf("存活收藏应保留: fav=%v err=%v", fav, err)
	}
	// 播放列表表头保留、存活条目保留
	pls, err := s.ListPlaylists()
	if err != nil || len(pls) != 1 {
		t.Fatalf("播放列表表头应保留: %+v err=%v", pls, err)
	}
	pl, err := s.GetPlaylist(pls[0].ID)
	if err != nil || len(pl.Items) != 1 || pl.Items[0].VideoID != "live-id" {
		t.Fatalf("存活播放列表条目应保留: %+v err=%v", pl, err)
	}
}

func TestDeleteOrphansReclaimsEmptyTags(t *testing.T) {
	s := openTestStore(t)
	seedHealthFixture(t, s, "ghost-id", "live-id")

	tagsBefore, err := s.ListTags()
	if err != nil {
		t.Fatal(err)
	}
	if len(tagsBefore) != 2 {
		t.Fatalf("造数后应有两个标签，实际 %d", len(tagsBefore))
	}

	if _, err := s.DeleteOrphansByTable("video_tag", map[string]bool{"live-id": true}); err != nil {
		t.Fatalf("清理标签关联失败: %v", err)
	}
	tagsAfter, err := s.ListTags()
	if err != nil {
		t.Fatal(err)
	}
	if len(tagsAfter) != 1 || tagsAfter[0].Name != "共享标签" {
		t.Fatalf("独占标签应被回收、共享标签保留，实际 %+v", tagsAfter)
	}
}

func TestDeleteAllOrphans(t *testing.T) {
	s := openTestStore(t)
	want := seedHealthFixture(t, s, "ghost-id", "live-id")
	existing := map[string]bool{"live-id": true}

	report, err := s.DeleteAllOrphans(existing)
	if err != nil {
		t.Fatalf("一键清理失败: %v", err)
	}
	if report != want {
		t.Fatalf("清理行数不符:\n got  %+v\n want %+v", report, want)
	}
	after, err := s.OrphanReport(existing)
	if err != nil {
		t.Fatalf("清理后体检失败: %v", err)
	}
	if after.Total() != 0 {
		t.Fatalf("一键清理后应无孤儿，实际 %+v", after)
	}
	// 存活数据与播放列表表头仍在
	if p, _ := s.GetProgress("live-id"); p == nil {
		t.Fatal("存活进度应保留")
	}
	pls, _ := s.ListPlaylists()
	if len(pls) != 1 {
		t.Fatalf("播放列表表头应保留，实际 %d 个", len(pls))
	}
}

func TestOrphanEmptyLibraryGuard(t *testing.T) {
	s := openTestStore(t)
	// existing 为空：体检计数应等于各表总行数，而不是 0
	seedHealthFixture(t, s, "ghost-id", "live-id")
	report, err := s.OrphanReport(nil)
	if err != nil {
		t.Fatalf("空库体检失败: %v", err)
	}
	// live-id 的对照行在空库口径下同样算孤儿
	if report.Progress != 2 || report.Favorites != 2 || report.PlaylistItem != 2 ||
		report.Bookmarks != 3 || report.SkipSettings != 1 || report.WatchEvents != 2 ||
		report.VideoTag != 3 {
		t.Fatalf("空库计数应为整表行数，实际 %+v", report)
	}

	cleaned, err := s.DeleteAllOrphans(nil)
	if err != nil {
		t.Fatalf("空库一键清理失败: %v", err)
	}
	if cleaned.Total() != report.Total() {
		t.Fatalf("空库清理行数应等于总行数: cleaned=%d total=%d", cleaned.Total(), report.Total())
	}
	after, err := s.OrphanReport(nil)
	if err != nil {
		t.Fatalf("清理后体检失败: %v", err)
	}
	if after.Total() != 0 {
		t.Fatalf("清理后应全空，实际 %+v", after)
	}
	// 播放列表表头依旧保留
	pls, _ := s.ListPlaylists()
	if len(pls) != 1 {
		t.Fatalf("播放列表表头应保留，实际 %d 个", len(pls))
	}
	// 标签被全部回收（video_tag 已空）
	tags, _ := s.ListTags()
	if len(tags) != 0 {
		t.Fatalf("空库清理后标签应全部回收，实际 %+v", tags)
	}
}

func TestDeleteOrphansInvalidTable(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.DeleteOrphansByTable("users; DROP TABLE progress--", nil); err == nil {
		t.Fatal("非法表名应报错")
	}
	if _, err := s.DeleteOrphansByTable("", nil); err == nil {
		t.Fatal("空表名应报错")
	}
	if _, err := s.OrphanSampleIDs("not-a-table", nil, 5); err == nil {
		t.Fatal("样例查询的非法表名应报错")
	}
	// 库表完好且可正常查询
	report, err := s.OrphanReport(nil)
	if err != nil || report.Total() != 0 {
		t.Fatalf("非法调用不应影响数据库: report=%+v err=%v", report, err)
	}
}

func TestOrphanSampleIDs(t *testing.T) {
	s := openTestStore(t)
	db := s.DB()
	now := time.Now().UTC()
	// 同一表插入 7 个不同悬空 video_id，limit 5 应只返回 5 个去重值
	for i := 0; i < 7; i++ {
		id := "ghost-" + string(rune('a'+i))
		if _, err := db.Exec(
			`INSERT INTO favorites(video_id, added_at) VALUES(?, ?)`, id, now); err != nil {
			t.Fatalf("插入样例数据失败: %v", err)
		}
	}
	ids, err := s.OrphanSampleIDs("favorites", map[string]bool{"live": true}, 5)
	if err != nil {
		t.Fatalf("OrphanSampleIDs 失败: %v", err)
	}
	if len(ids) != 5 {
		t.Fatalf("应受 limit 约束返回 5 个，实际 %d", len(ids))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("样例应去重，出现重复 %s", id)
		}
		seen[id] = true
	}

	// 全部视频均在库中时无孤儿，返回空切片
	existingAll := map[string]bool{"live": true}
	for i := 0; i < 7; i++ {
		existingAll["ghost-"+string(rune('a'+i))] = true
	}
	healthy, err := s.OrphanSampleIDs("favorites", existingAll, 5)
	if err != nil {
		t.Fatalf("健康库样例查询失败: %v", err)
	}
	if len(healthy) != 0 {
		t.Fatalf("健康库应返回空结果，实际 %+v", healthy)
	}

	// 空库护栏：existing 为 nil 时回退为整表去重样例
	emptyLib, err := s.OrphanSampleIDs("favorites", nil, 5)
	if err != nil || len(emptyLib) != 5 {
		t.Fatalf("空库回退应返回 5 个整表样例: ids=%+v err=%v", emptyLib, err)
	}
}
