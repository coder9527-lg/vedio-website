package media

import (
	"os"
	"path/filepath"
	"testing"
)

// showTestFiles 构造一个多季剧集 + 季目录 + 电影的混合库。
func showTestFiles() map[string]string {
	return map[string]string{
		"剧A/剧A.S01E01.mkv":          "1",
		"剧A/剧A.S01E02.mkv":          "2",
		"剧A/Season 2/剧A.S02E01.mkv": "3",
		"剧A/Specials/剧A.S00E01.mkv": "4",
		"剧B/剧B 第1季第1集.mp4":          "5",
		"电影 2024 1080p.mkv":         "6",
	}
}

func TestShowAggregation(t *testing.T) {
	lib, _ := newTestLibrary(t, showTestFiles())
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}

	shows := lib.ListShows()
	if len(shows) != 2 {
		t.Fatalf("期望聚合出 2 部剧，实际 %d", len(shows))
	}

	var showA *Show
	for _, s := range shows {
		if s.Title == "剧A" {
			showA = s
		}
	}
	if showA == nil {
		t.Fatal("未找到剧A")
	}
	if showA.EpisodeCount != 4 {
		t.Errorf("剧A 期望 4 集，实际 %d", showA.EpisodeCount)
	}
	if len(showA.Seasons) != 3 {
		t.Fatalf("剧A 期望 3 个季（含 S00），实际 %d", len(showA.Seasons))
	}
	// 季号升序：0、1、2
	wantSeasons := []int{0, 1, 2}
	for i, s := range showA.Seasons {
		if s.Number != wantSeasons[i] {
			t.Errorf("季顺序错误: got %v, want %v", seasonNumbers(showA), wantSeasons)
		}
	}
	// 第 1 季两集顺序
	s1 := showA.Seasons[1]
	if s1.Episodes[0].Number != 1 || s1.Episodes[1].Number != 2 {
		t.Errorf("第1季集序错误: %d %d", s1.Episodes[0].Number, s1.Episodes[1].Number)
	}
	// 季目录上提：S02E01 与 S01E01 必须在同一部剧
	if got := s1.Episodes[0].Video.RelPath; got != "剧A/剧A.S01E01.mkv" {
		t.Errorf("首集路径异常: %s", got)
	}

	// 电影不属于任何剧集
	movie, ok := lib.Get(makeID("电影 2024 1080p.mkv"))
	if !ok {
		t.Fatal("电影未被索引")
	}
	if lib.IsShowEpisode(movie.ID) {
		t.Error("电影不应被归入剧集")
	}
	if len(lib.ShowVideos(showA.ID)) != 4 {
		t.Error("ShowVideos 成员数量不符")
	}
}

func TestShowIDStableAcrossRescan(t *testing.T) {
	lib, _ := newTestLibrary(t, showTestFiles())
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, s := range lib.ListShows() {
		before[s.Title] = s.ID
	}

	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	for _, s := range lib.ListShows() {
		if before[s.Title] != s.ID {
			t.Errorf("剧集 %s 重扫后 ID 漂移: %s → %s", s.Title, before[s.Title], s.ID)
		}
	}
}

func TestShowUpsertIncremental(t *testing.T) {
	lib, root := newTestLibrary(t, showTestFiles())
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	showA := findShow(t, lib, "剧A")

	// 新增一集：集数 4 → 5，映射即时生效
	newEp := filepath.Join(root, "剧A", "剧A.S01E03.mkv")
	if err := os.WriteFile(newEp, []byte("7"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, ok := lib.Upsert(newEp)
	if !ok {
		t.Fatal("Upsert 新集失败")
	}
	updated, ok := lib.GetShow(showA.ID)
	if !ok {
		t.Fatal("剧A 消失")
	}
	if updated.EpisodeCount != 5 {
		t.Errorf("新集后期望 5 集，实际 %d", updated.EpisodeCount)
	}
	if sid, ok := lib.ShowIDOf(v.ID); !ok || sid != showA.ID {
		t.Error("新集未映射到剧A")
	}
}

func TestShowRemoveLastEpisodeClearsShow(t *testing.T) {
	lib, root := newTestLibrary(t, map[string]string{
		"剧B/剧B 第1季第1集.mp4": "x",
		"剧B/剧B 第1季第2集.mp4": "y",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	showB := findShow(t, lib, "剧B")

	// 删一集：剧仍在，剩 1 集
	if n := lib.Remove(filepath.Join(root, "剧B", "剧B 第1季第2集.mp4")); n != 1 {
		t.Fatalf("删除数量异常: %d", n)
	}
	if got, ok := lib.GetShow(showB.ID); !ok || got.EpisodeCount != 1 {
		t.Fatalf("删一集后剧状态异常: ok=%v", ok)
	}

	// 删光最后一集：空剧自动清除，映射也清空
	last := filepath.Join(root, "剧B", "剧B 第1季第1集.mp4")
	v, _ := lib.Get(makeID("剧B/剧B 第1季第1集.mp4"))
	lib.Remove(last)
	if _, ok := lib.GetShow(showB.ID); ok {
		t.Error("成员清零后剧集应被清除")
	}
	if lib.IsShowEpisode(v.ID) {
		t.Error("视频删除后 videoShow 映射残留")
	}
}

func TestShowRenameMigrates(t *testing.T) {
	lib, root := newTestLibrary(t, map[string]string{
		"剧A/剧A.S01E01.mkv": "x",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	oldShow := findShow(t, lib, "剧A")
	v, _ := lib.Get(makeID("剧A/剧A.S01E01.mkv"))

	// 模拟改名：先删旧文件，再在新剧名目录 Upsert
	lib.Remove(filepath.Join(root, "剧A", "剧A.S01E01.mkv"))
	newPath := filepath.Join(root, "剧C", "剧C.S01E01.mkv")
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib.Upsert(newPath)

	if _, ok := lib.GetShow(oldShow.ID); ok {
		t.Error("旧剧应已随成员迁走而清除")
	}
	newShow := findShow(t, lib, "剧C")
	if newShow.ID == oldShow.ID {
		t.Error("不同剧名不应复用 show ID")
	}
	if sid, ok := lib.ShowIDOf(v.ID); ok {
		t.Errorf("旧视频 ID 不应残留映射，实际 → %s", sid)
	}
}

func TestNextEpisode(t *testing.T) {
	lib, _ := newTestLibrary(t, showTestFiles())
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	showA := findShow(t, lib, "剧A")
	ids := lib.ShowVideos(showA.ID)

	// 取 S01E01，下一集应是 S01E02
	var firstID string
	for id := range ids {
		v, _ := lib.Get(id)
		if v.RelPath == "剧A/剧A.S01E01.mkv" {
			firstID = id
		}
	}
	next, ok := lib.NextEpisode(firstID)
	if !ok {
		t.Fatal("S01E01 应有下一集")
	}
	if next.RelPath != "剧A/剧A.S01E02.mkv" {
		t.Errorf("连播目标错误: %s", next.RelPath)
	}

	// 末集（按季集序最后的 S02E01）无下一集
	last := showA.Seasons[2].Episodes[0].Video.ID
	if _, ok := lib.NextEpisode(last); ok {
		t.Error("末集不应有下一集")
	}

	// 电影无下一集
	movie, _ := lib.Get(makeID("电影 2024 1080p.mkv"))
	if _, ok := lib.NextEpisode(movie.ID); ok {
		t.Error("电影不应有下一集")
	}
}

func findShow(t *testing.T, lib *Library, title string) *Show {
	t.Helper()
	for _, s := range lib.ListShows() {
		if s.Title == title {
			return s
		}
	}
	t.Fatalf("未找到剧集 %s", title)
	return nil
}

func seasonNumbers(s *Show) []int {
	out := make([]int, len(s.Seasons))
	for i, season := range s.Seasons {
		out[i] = season.Number
	}
	return out
}
