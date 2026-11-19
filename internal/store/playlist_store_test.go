package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// mustCreatePlaylist 创建列表，失败即终止测试。
func mustCreatePlaylist(t *testing.T, s *Store, name string) *Playlist {
	t.Helper()
	pl, err := s.CreatePlaylist(name)
	if err != nil {
		t.Fatalf("CreatePlaylist(%q) 失败: %v", name, err)
	}
	return pl
}

// assertItemOrder 断言列表条目视频 ID 的完整顺序。
func assertItemOrder(t *testing.T, s *Store, pid string, want []string) {
	t.Helper()
	pl, err := s.GetPlaylist(pid)
	if err != nil || pl == nil {
		t.Fatalf("GetPlaylist 失败: %v err=%v", pl, err)
	}
	if len(pl.Items) != len(want) {
		t.Fatalf("条目数不符: got %d want %d (%+v)", len(pl.Items), len(want), pl.Items)
	}
	for i, vid := range want {
		if pl.Items[i].VideoID != vid {
			t.Fatalf("位置 %d 顺序错误: got %s want %s（全序 %+v）", i, pl.Items[i].VideoID, vid, pl.Items)
		}
		if pl.Items[i].SortOrder != i {
			t.Fatalf("位置 %d 的 sort_order 应为 %d, got %d", i, i, pl.Items[i].SortOrder)
		}
	}
}

func TestCreatePlaylistValidationAndUniqueID(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreatePlaylist("   "); err == nil {
		t.Fatal("纯空白名称应报错")
	}
	if _, err := s.CreatePlaylist(strings.Repeat("长", maxPlaylistNameLen+1)); err == nil {
		t.Fatal("超长名称应报错")
	}
	pl1 := mustCreatePlaylist(t, s, "  周末追剧  ")
	pl2 := mustCreatePlaylist(t, s, "电影合集")
	if pl1.ID == "" || pl1.ID == pl2.ID {
		t.Fatalf("ID 应非空且唯一: %q %q", pl1.ID, pl2.ID)
	}
	if pl1.Name != "周末追剧" {
		t.Fatalf("名称应 TrimSpace, got %q", pl1.Name)
	}
	if pl1.Items == nil {
		t.Fatal("新列表 Items 应为非 nil 空切片")
	}
}

func TestRenamePlaylist(t *testing.T) {
	s := openTestStore(t)
	pl := mustCreatePlaylist(t, s, "旧名")

	if err := s.RenamePlaylist(pl.ID, " 新名 "); err != nil {
		t.Fatalf("RenamePlaylist 失败: %v", err)
	}
	got, err := s.GetPlaylist(pl.ID)
	if err != nil || got.Name != "新名" {
		t.Fatalf("改名后查询错误: %+v err=%v", got, err)
	}
	if err := s.RenamePlaylist(pl.ID, "  "); err == nil {
		t.Fatal("空名称应报错")
	}
	if err := s.RenamePlaylist("not-exist", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的列表应返回 ErrNotFound, got %v", err)
	}
}

func TestDeletePlaylistCascadesItems(t *testing.T) {
	s := openTestStore(t)
	pl := mustCreatePlaylist(t, s, "临时")
	if _, err := s.AddPlaylistItem(pl.ID, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePlaylist(pl.ID); err != nil {
		t.Fatalf("DeletePlaylist 失败: %v", err)
	}
	if got, _ := s.GetPlaylist(pl.ID); got != nil {
		t.Fatalf("删除后应查不到, got %+v", got)
	}
	if err := s.DeletePlaylist(pl.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应返回 ErrNotFound, got %v", err)
	}
}

func TestGetPlaylistMissingReturnsNil(t *testing.T) {
	s := openTestStore(t)
	got, err := s.GetPlaylist("missing")
	if err != nil || got != nil {
		t.Fatalf("缺失记录应返回 (nil, nil), got=%v err=%v", got, err)
	}
}

func TestListPlaylistsSummaryAndOrder(t *testing.T) {
	s := openTestStore(t)
	pl1 := mustCreatePlaylist(t, s, "第一个")
	mustCreatePlaylist(t, s, "空列表")
	pl3 := mustCreatePlaylist(t, s, "第三个")
	if _, err := s.AddPlaylistItem(pl1.ID, "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPlaylistItem(pl3.ID, "v2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPlaylistItem(pl3.ID, "v3"); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListPlaylists()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("应有 3 个摘要, got %d", len(list))
	}
	if list[0].ID != pl1.ID || list[1].ItemCount != 0 || list[2].ItemCount != 2 {
		t.Fatalf("摘要顺序/计数错误: %+v", list)
	}
}

func TestAddPlaylistItemOrderAndDedupe(t *testing.T) {
	s := openTestStore(t)
	pl := mustCreatePlaylist(t, s, "列表")

	added, err := s.AddPlaylistItem(pl.ID, "v1")
	if err != nil || !added {
		t.Fatalf("首次加入应新增, added=%v err=%v", added, err)
	}
	added, err = s.AddPlaylistItem(pl.ID, "v2")
	if err != nil || !added {
		t.Fatalf("第二个视频应新增, added=%v err=%v", added, err)
	}
	added, err = s.AddPlaylistItem(pl.ID, "v1")
	if err != nil || added {
		t.Fatalf("重复视频应幂等跳过, added=%v err=%v", added, err)
	}
	assertItemOrder(t, s, pl.ID, []string{"v1", "v2"})

	if _, err := s.AddPlaylistItem("missing", "v1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("向不存在的列表加入应返回 ErrNotFound, got %v", err)
	}
	if _, err := s.AddPlaylistItem(pl.ID, ""); err == nil {
		t.Fatal("空 videoID 应报错")
	}
}

func TestAddPlaylistItemsBatch(t *testing.T) {
	s := openTestStore(t)
	pl := mustCreatePlaylist(t, s, "整剧")

	// 先放入 v2，批量加入时它应被跳过且不改变位置
	if _, err := s.AddPlaylistItem(pl.ID, "v2"); err != nil {
		t.Fatal(err)
	}
	n, err := s.AddPlaylistItems(pl.ID, []string{"v1", "v2", "v3", "v4"})
	if err != nil {
		t.Fatalf("批量加入失败: %v", err)
	}
	if n != 3 {
		t.Fatalf("应新增 3 条, got %d", n)
	}
	// v2 保持位置 0，新条目按入参顺序追加
	assertItemOrder(t, s, pl.ID, []string{"v2", "v1", "v3", "v4"})

	// 全部已存在时新增 0 且不报错
	n, err = s.AddPlaylistItems(pl.ID, []string{"v1", "v3"})
	if err != nil || n != 0 {
		t.Fatalf("全重复应返回 0, n=%d err=%v", n, err)
	}
	if _, err := s.AddPlaylistItems("missing", []string{"v1"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的列表应返回 ErrNotFound, got %v", err)
	}
	// 空入参是 no-op
	if n, err := s.AddPlaylistItems(pl.ID, nil); err != nil || n != 0 {
		t.Fatalf("空入参应为 no-op, n=%d err=%v", n, err)
	}
}

func TestRemovePlaylistItemRenumbers(t *testing.T) {
	s := openTestStore(t)
	pl := mustCreatePlaylist(t, s, "列表")
	if _, err := s.AddPlaylistItems(pl.ID, []string{"v1", "v2", "v3"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePlaylistItem(pl.ID, "v2"); err != nil {
		t.Fatalf("移除失败: %v", err)
	}
	assertItemOrder(t, s, pl.ID, []string{"v1", "v3"})

	// 移除不存在的条目不报错（幂等）
	if err := s.RemovePlaylistItem(pl.ID, "ghost"); err != nil {
		t.Fatalf("移除不存在条目应幂等, got %v", err)
	}
}

func TestMovePlaylistItem(t *testing.T) {
	s := openTestStore(t)
	pl := mustCreatePlaylist(t, s, "列表")
	if _, err := s.AddPlaylistItems(pl.ID, []string{"v1", "v2", "v3", "v4"}); err != nil {
		t.Fatal(err)
	}

	// v1 从位置 0 下移到位置 2
	if err := s.MovePlaylistItem(pl.ID, "v1", 2); err != nil {
		t.Fatalf("Move 失败: %v", err)
	}
	assertItemOrder(t, s, pl.ID, []string{"v2", "v3", "v1", "v4"})

	// v4 从末尾上移到位置 1
	if err := s.MovePlaylistItem(pl.ID, "v4", 1); err != nil {
		t.Fatalf("Move 失败: %v", err)
	}
	assertItemOrder(t, s, pl.ID, []string{"v2", "v4", "v3", "v1"})

	// 移到当前位置：no-op 不报错
	if err := s.MovePlaylistItem(pl.ID, "v2", 0); err != nil {
		t.Fatalf("同位置移动应 no-op, got %v", err)
	}
	// 越界报错
	if err := s.MovePlaylistItem(pl.ID, "v2", 99); err == nil {
		t.Fatal("越界目标位置应报错")
	}
	if err := s.MovePlaylistItem(pl.ID, "v2", -1); err == nil {
		t.Fatal("负目标位置应报错")
	}
	// 不存在的条目报错
	if err := s.MovePlaylistItem(pl.ID, "ghost", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在条目应返回 ErrNotFound, got %v", err)
	}
}

func TestPlaylistContainsAndMemberships(t *testing.T) {
	s := openTestStore(t)
	pl1 := mustCreatePlaylist(t, s, "列表一")
	pl2 := mustCreatePlaylist(t, s, "列表二")
	if _, err := s.AddPlaylistItems(pl1.ID, []string{"v1", "v2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPlaylistItem(pl2.ID, "v1"); err != nil {
		t.Fatal(err)
	}

	ok, err := s.PlaylistContains(pl1.ID, "v2")
	if err != nil || !ok {
		t.Fatalf("PlaylistContains 应命中, ok=%v err=%v", ok, err)
	}
	ok, _ = s.PlaylistContains(pl1.ID, "v9")
	if ok {
		t.Fatal("未加入的视频不应命中")
	}

	m, err := s.VideoPlaylistMemberships()
	if err != nil {
		t.Fatal(err)
	}
	if len(m["v1"]) != 2 || len(m["v2"]) != 1 || m["v2"][0] != pl1.ID {
		t.Fatalf("memberships 内容错误: %+v", m)
	}
}

func TestPrunePlaylistItems(t *testing.T) {
	s := openTestStore(t)
	pl := mustCreatePlaylist(t, s, "列表")
	if _, err := s.AddPlaylistItems(pl.ID, []string{"keep", "ghost"}); err != nil {
		t.Fatal(err)
	}

	if err := s.Prune(map[string]bool{"keep": true}); err != nil {
		t.Fatalf("Prune 失败: %v", err)
	}
	assertItemOrder(t, s, pl.ID, []string{"keep"})
	// 表头保留
	if got, _ := s.GetPlaylist(pl.ID); got == nil {
		t.Fatal("Prune 不应删除播放列表表头")
	}

	// 空集合分支：清空全部条目但保留表头
	if _, err := s.AddPlaylistItem(pl.ID, "another"); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(map[string]bool{}); err != nil {
		t.Fatalf("空集合 Prune 失败: %v", err)
	}
	assertItemOrder(t, s, pl.ID, []string{})
	if got, _ := s.GetPlaylist(pl.ID); got == nil {
		t.Fatal("空集合 Prune 后表头应保留")
	}
}

func TestReopenPersistsPlaylists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pl.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := s.CreatePlaylist("持久化")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPlaylistItems(pl.ID, []string{"v1", "v2"}); err != nil {
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
	got, err := s2.GetPlaylist(pl.ID)
	if err != nil || got == nil || got.Name != "持久化" {
		t.Fatalf("重开后列表丢失: %+v err=%v", got, err)
	}
	if len(got.Items) != 2 || got.Items[1].VideoID != "v2" || got.Items[1].SortOrder != 1 {
		t.Fatalf("重开后条目顺序丢失: %+v", got.Items)
	}
}
