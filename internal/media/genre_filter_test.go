package media

import "testing"

// TestQueryGenreFilter 验证 Genre 内存过滤与 AllGenres 聚合：
// 仅命中元信息中包含该类型的视频，大小写/空白不敏感，无元信息视频不参与。
func TestQueryGenreFilter(t *testing.T) {
	files := map[string]string{
		"action/a.mp4": "x",
		"action/a.nfo": `<movie><title>动作片</title><genre>动作</genre><genre>科幻</genre></movie>`,
		"comedy/b.mkv": "x",
		"comedy/b.nfo": `<movie><title>喜剧片</title><genre>喜剧</genre></movie>`,
		"plain/c.mp4":  "x", // 无 NFO，无类型
		"scifi/d.mp4":  "x",
		"scifi/d.nfo":  `<movie><title>科幻片</title><genre> 科幻 </genre></movie>`,
	}
	lib, _ := newTestLibrary(t, files)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}

	got := lib.Query(Query{Genre: "动作"})
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("类型「动作」应只命中 a，got %v", got)
	}

	// 大小写不敏感 + 空白修剪：d 的 " 科幻 " 与查询 "科幻" 应匹配，共 2 部
	got = lib.Query(Query{Genre: "科幻"})
	if len(got) != 2 {
		names := make([]string, 0, len(got))
		for _, v := range got {
			names = append(names, v.Name)
		}
		t.Fatalf("类型「科幻」应命中 a 与 d，got %v", names)
	}

	// 不存在的类型返回空切片
	if got := lib.Query(Query{Genre: "纪录片"}); len(got) != 0 {
		t.Fatalf("不存在的类型应返回空，got %d", len(got))
	}

	// 类型筛选可与关键字组合
	got = lib.Query(Query{Genre: "科幻", Keyword: "d"})
	if len(got) != 1 || got[0].Name != "d" {
		t.Fatalf("类型+关键字组合应只命中 d，got %v", got)
	}
}

// TestAllGenres 验证全库类型聚合去重且按名称排序。
func TestAllGenres(t *testing.T) {
	files := map[string]string{
		"a/a.mp4": "x",
		"a/a.nfo": `<movie><genre>科幻</genre><genre>动作</genre></movie>`,
		"b/b.mp4": "x",
		"b/b.nfo": `<movie><genre>动作</genre><genre>喜剧</genre></movie>`,
		"c/c.mp4": "x", // 无元信息，不贡献标签
	}
	lib, _ := newTestLibrary(t, files)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}

	genres := lib.AllGenres()
	want := []string{"动作", "喜剧", "科幻"} // sort.Strings 按字节序，与实现一致
	if len(genres) != len(want) {
		t.Fatalf("聚合类型数不对，want %v got %v", want, genres)
	}
	for i, g := range want {
		if genres[i] != g {
			t.Fatalf("第 %d 个类型 want %q got %q（完整 %v）", i, g, genres[i], genres)
		}
	}
}
