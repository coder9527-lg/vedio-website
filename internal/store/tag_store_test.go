package store

import (
	"errors"
	"strings"
	"testing"
)

func TestSetVideoTagIdempotentAndCaseInsensitive(t *testing.T) {
	s := openTestStore(t)

	tg, created, err := s.SetVideoTag("v1", "  周末看  ")
	if err != nil {
		t.Fatalf("首次打标失败: %v", err)
	}
	if !created || tg.Name != "周末看" {
		t.Fatalf("首次打标应 created=true 且保留名称, got=%+v created=%v", tg, created)
	}

	// 同视频重复打同一标签：幂等，created=false，关联仍只有一行
	_, created, err = s.SetVideoTag("v1", "周末看")
	if err != nil || created {
		t.Fatalf("重复打标应幂等 created=false, got created=%v err=%v", created, err)
	}

	// 另一个视频打内容不同的标签：应新建关联，标签总数随之 +1
	if _, created, err := s.SetVideoTag("v2", "周 末 看"); err != nil || !created {
		t.Fatalf("v2 新标签关联应 created=true, got created=%v err=%v", created, err)
	}
	tags, err := s.ListTags()
	if err != nil {
		t.Fatalf("ListTags 失败: %v", err)
	}
	if len(tags) != 2 { // “周末看”与“周 末 看”是两个不同标签
		t.Fatalf("标签数量异常: %+v", tags)
	}
}

func TestSetVideoTagCaseFolding(t *testing.T) {
	s := openTestStore(t)

	if _, _, err := s.SetVideoTag("v1", "Movie"); err != nil {
		t.Fatalf("打标失败: %v", err)
	}
	tg, created, err := s.SetVideoTag("v1", "movie")
	if err != nil {
		t.Fatalf("小写打标失败: %v", err)
	}
	if created {
		t.Fatalf("Movie/movie 应归一为同一标签且关联幂等, got created=%v", created)
	}
	if tg.Name != "Movie" {
		t.Fatalf("应保留首次写入的大小写, got=%s", tg.Name)
	}
	tags, _ := s.ListTags()
	if len(tags) != 1 {
		t.Fatalf("大小写归一后应只有 1 个标签, got=%d", len(tags))
	}
}

func TestSetVideoTagInvalidName(t *testing.T) {
	s := openTestStore(t)

	for _, name := range []string{"", "   ", strings.Repeat("标", maxTagNameRunes+1)} {
		if _, _, err := s.SetVideoTag("v1", name); err == nil {
			t.Fatalf("非法标签名 %q 应报错", name)
		}
	}
	if _, _, err := s.SetVideoTag("", "tag"); err == nil {
		t.Fatalf("空 videoID 应报错")
	}
}

func TestRemoveVideoTagReclaimsEmptyTag(t *testing.T) {
	s := openTestStore(t)

	tg, _, _ := s.SetVideoTag("v1", "待重看")
	if err := s.RemoveVideoTag("v1", tg.ID); err != nil {
		t.Fatalf("取消标签失败: %v", err)
	}
	// 标签已无任何引用，应被自动回收
	if got, err := s.TagByName("待重看"); err != nil || got != nil {
		t.Fatalf("空标签应被回收, got=%v err=%v", got, err)
	}
	// 再删一次也不报错（幂等）
	if err := s.RemoveVideoTag("v1", tg.ID); err != nil {
		t.Fatalf("重复取消应幂等无错: %v", err)
	}

	// 两个视频共用标签时，取消一个不应回收标签
	tg2, _, _ := s.SetVideoTag("a", "经典")
	_, _, _ = s.SetVideoTag("b", "经典")
	if err := s.RemoveVideoTag("a", tg2.ID); err != nil {
		t.Fatalf("取消标签失败: %v", err)
	}
	if got, _ := s.TagByName("经典"); got == nil {
		t.Fatalf("仍有视频引用的标签不应被回收")
	}
}

func TestVideoTagsAndMap(t *testing.T) {
	s := openTestStore(t)

	if got, err := s.VideoTags("none"); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("无标签应返回非 nil 空切片, got=%v err=%v", got, err)
	}

	s.SetVideoTag("v1", "标签B")
	s.SetVideoTag("v1", "标签A")
	s.SetVideoTag("v2", "标签A")

	t1, err := s.VideoTags("v1")
	if err != nil || len(t1) != 2 {
		t.Fatalf("v1 应有 2 个标签, got=%+v err=%v", t1, err)
	}
	if t1[0].Name != "标签B" || t1[1].Name != "标签A" {
		t.Fatalf("应按打标时间升序, got=%s,%s", t1[0].Name, t1[1].Name)
	}

	m, err := s.VideoTagsMap()
	if err != nil {
		t.Fatalf("VideoTagsMap 失败: %v", err)
	}
	if len(m) != 2 || len(m["v1"]) != 2 || len(m["v2"]) != 1 {
		t.Fatalf("标签映射聚合异常: %+v", m)
	}
}

func TestListTagsCountAndOrder(t *testing.T) {
	s := openTestStore(t)

	tags, err := s.ListTags()
	if err != nil || len(tags) != 0 {
		t.Fatalf("空库应返回空切片, got=%v err=%v", tags, err)
	}

	s.SetVideoTag("v1", "beta")
	s.SetVideoTag("v2", "beta")
	s.SetVideoTag("v1", "alpha")

	tags, _ = s.ListTags()
	if len(tags) != 2 {
		t.Fatalf("应有 2 个标签, got=%+v", tags)
	}
	if tags[0].Name != "alpha" || tags[1].Name != "beta" {
		t.Fatalf("应按名称排序, got=%s,%s", tags[0].Name, tags[1].Name)
	}
	if tags[1].VideoCount != 2 {
		t.Fatalf("beta 计数应为 2, got=%d", tags[1].VideoCount)
	}
}

func TestTagVideoSet(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.TagVideoSet(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知标签应返回 ErrNotFound, got=%v", err)
	}

	tg, _, _ := s.SetVideoTag("v1", "x")
	s.SetVideoTag("v2", "x")
	set, err := s.TagVideoSet(tg.ID)
	if err != nil || len(set) != 2 || !set["v1"] || !set["v2"] {
		t.Fatalf("视频集合异常: %+v err=%v", set, err)
	}
}

func TestPruneTags(t *testing.T) {
	s := openTestStore(t)

	s.SetVideoTag("keep", "共用标签")
	s.SetVideoTag("gone", "悬空标签")
	s.SetVideoTag("gone", "共用标签")

	// 分支一：existing 为空 → 全清
	if err := s.Prune(nil); err != nil {
		t.Fatalf("Prune(nil) 失败: %v", err)
	}
	if tags, _ := s.ListTags(); len(tags) != 0 {
		t.Fatalf("全清后不应残留标签: %+v", tags)
	}

	// 重新造数验证分支二：仅清悬空视频
	s.SetVideoTag("keep", "共用标签")
	s.SetVideoTag("gone", "悬空标签")
	if err := s.Prune(map[string]bool{"keep": true}); err != nil {
		t.Fatalf("Prune 失败: %v", err)
	}
	m, _ := s.VideoTagsMap()
	if len(m) != 1 {
		t.Fatalf("应只剩 keep 的关联, got=%+v", m)
	}
	tags, _ := s.ListTags()
	if len(tags) != 1 || tags[0].Name != "共用标签" {
		t.Fatalf("悬空标签应被回收, got=%+v", tags)
	}
}
