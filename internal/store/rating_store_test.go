package store

import "testing"

func TestRatingSetGetOverwrite(t *testing.T) {
	s := openTestStore(t)

	// 空库查询返回 nil
	if got, err := s.GetRating("v1"); err != nil || got != nil {
		t.Fatalf("空库评分应为 nil，got=%v err=%v", got, err)
	}

	if err := s.SetRating("v1", 3); err != nil {
		t.Fatalf("SetRating 失败: %v", err)
	}
	// 覆盖为 5 星
	if err := s.SetRating("v1", 5); err != nil {
		t.Fatalf("覆盖评分失败: %v", err)
	}
	got, err := s.GetRating("v1")
	if err != nil || got == nil || got.Stars != 5 {
		t.Fatalf("覆盖后评分期望 5，got=%v err=%v", got, err)
	}

	// 非法档位拒绝
	if err := s.SetRating("v2", 0); err == nil {
		t.Error("0 星应报错")
	}
	if err := s.SetRating("v2", 6); err == nil {
		t.Error("6 星应报错")
	}
}

func TestRatingDeleteAndMap(t *testing.T) {
	s := openTestStore(t)
	_ = s.SetRating("v1", 4)
	_ = s.SetRating("v2", 2)

	if err := s.DeleteRating("v1"); err != nil {
		t.Fatalf("DeleteRating 失败: %v", err)
	}
	// 删除不存在的记录不报错
	if err := s.DeleteRating("ghost"); err != nil {
		t.Fatalf("删除缺失评分不应报错: %v", err)
	}
	if got, _ := s.GetRating("v1"); got != nil {
		t.Errorf("删除后评分应为 nil，got=%v", got)
	}

	m, err := s.RatingMap()
	if err != nil {
		t.Fatalf("RatingMap 失败: %v", err)
	}
	if len(m) != 1 || m["v2"].Stars != 2 {
		t.Fatalf("RatingMap 数据不符: %+v", m)
	}
}

func TestRatingHighRatedOrder(t *testing.T) {
	s := openTestStore(t)
	_ = s.SetRating("low", 2)
	_ = s.SetRating("four", 4)
	_ = s.SetRating("five", 5)

	got, err := s.HighRated(4, 50)
	if err != nil {
		t.Fatalf("HighRated 失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("高分记录期望 2 条，实际 %d", len(got))
	}
	// 星级倒序：5 星在前
	if got[0].VideoID != "five" || got[1].VideoID != "four" {
		t.Fatalf("高分排序错误: %+v", got)
	}
}

func TestPruneRemovesOrphanRatings(t *testing.T) {
	s := openTestStore(t)
	_ = s.SetRating("keep", 5)
	_ = s.SetRating("gone", 4)

	if err := s.Prune(map[string]bool{"keep": true}); err != nil {
		t.Fatalf("Prune 失败: %v", err)
	}
	if got, _ := s.GetRating("gone"); got != nil {
		t.Error("悬空评分应被 Prune 清理")
	}
	if got, _ := s.GetRating("keep"); got == nil {
		t.Error("存活视频的评分不应被清理")
	}
}
