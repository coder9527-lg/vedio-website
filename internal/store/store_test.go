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

	if err := s.SaveProgress("v1", 12.5, 100); err != nil {
		t.Fatalf("SaveProgress 失败: %v", err)
	}
	// 再次上报应 upsert 而非重复插入
	if err := s.SaveProgress("v1", 40, 100); err != nil {
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
	if err := s.SaveProgress("v1", -5, -9); err != nil {
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
	if err := s.SaveProgress("", 1, 1); err == nil {
		t.Fatal("空 videoID 应报错")
	}
}

func TestProgressMapAndRecent(t *testing.T) {
	s := openTestStore(t)
	for i, id := range []string{"a", "b", "c"} {
		if err := s.SaveProgress(id, float64(i), 10); err != nil {
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
	if err := s.SaveProgress("v1", 10, 100); err != nil {
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
	_ = s.SaveProgress("keep", 1, 10)
	_ = s.SaveProgress("gone", 2, 10)
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
	_ = s.SaveProgress("v1", 1, 10)
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
	if err := s.SaveProgress("v1", 99, 200); err != nil {
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
