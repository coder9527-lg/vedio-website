package media

import "testing"

// videoIDByName 从查询结果中按 Name 找视频 ID，找不到返回空串。
func videoIDByName(t *testing.T, lib *Library, name string) string {
	t.Helper()
	for _, v := range lib.List() {
		if v.Name == name {
			return v.ID
		}
	}
	t.Fatalf("测试夹具中找不到视频 %q", name)
	return ""
}

// TestQueryTagFilter 验证标签筛选的 TagOn 语义：
// TagOn 区分「未启用标签筛选」与「筛选空集」，后者必须强制零结果（回归未知标签漏过滤缺陷）。
func TestQueryTagFilter(t *testing.T) {
	files := map[string]string{
		"dir/a.mp4": "x",
		"dir/b.mp4": "x",
		"dir/c.mp4": "x",
	}
	lib, _ := newTestLibrary(t, files)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}

	idA := videoIDByName(t, lib, "a")
	idB := videoIDByName(t, lib, "b")

	// TagOn=true 且集合命中：只返回集合内视频
	got := lib.Query(Query{TagOn: true, TagIDs: map[string]bool{idA: true, idB: true}})
	if len(got) != 2 {
		t.Fatalf("标签集合命中应返回 2 部，got %d", len(got))
	}
	for _, v := range got {
		if v.ID != idA && v.ID != idB {
			t.Fatalf("结果含非标签视频 %q", v.ID)
		}
	}

	// TagOn=true 空集：强制零结果（未知标签场景的回归断言）
	if got := lib.Query(Query{TagOn: true, TagIDs: map[string]bool{}}); len(got) != 0 {
		t.Fatalf("TagOn 空集必须零结果，got %d", len(got))
	}

	// TagOn=false：即使 TagIDs 为 nil/空也不参与过滤，返回全部
	if got := lib.Query(Query{TagOn: false, TagIDs: nil}); len(got) != 3 {
		t.Fatalf("未启用标签筛选应返回全部 3 部，got %d", len(got))
	}
	if got := lib.Query(Query{}); len(got) != 3 {
		t.Fatalf("零值 Query 应返回全部 3 部，got %d", len(got))
	}
}

// TestQueryTagFilterOrthogonal 验证标签筛选与 IDs 视图、关键字、类型过滤正交组合。
func TestQueryTagFilterOrthogonal(t *testing.T) {
	files := map[string]string{
		"action/a.mp4": "x",
		"action/a.nfo": `<movie><title>动作片甲</title><genre>动作</genre></movie>`,
		"action/b.mp4": "x",
		"action/b.nfo": `<movie><title>动作片乙</title><genre>动作</genre></movie>`,
		"comedy/c.mp4": "x",
		"comedy/c.nfo": `<movie><title>喜剧片</title><genre>喜剧</genre></movie>`,
	}
	lib, _ := newTestLibrary(t, files)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}

	idA := videoIDByName(t, lib, "a")
	idB := videoIDByName(t, lib, "b")
	idC := videoIDByName(t, lib, "c")

	// 标签集合 {a, b} 与 IDs 视图 {b, c} 取交集 → 只剩 b
	got := lib.Query(Query{
		TagOn:  true,
		TagIDs: map[string]bool{idA: true, idB: true},
		IDs:    map[string]bool{idB: true, idC: true},
	})
	if len(got) != 1 || got[0].ID != idB {
		t.Fatalf("标签与 IDs 视图交集应只剩 b，got %v", got)
	}

	// 标签集合 {a, b} 与关键字「乙」组合 → 只剩 b
	got = lib.Query(Query{
		TagOn:   true,
		TagIDs:  map[string]bool{idA: true, idB: true},
		Keyword: "乙",
	})
	if len(got) != 1 || got[0].ID != idB {
		t.Fatalf("标签与关键字组合应只剩 b，got %v", got)
	}

	// 标签集合 {a, c} 与类型「动作」组合 → 只剩 a（c 是喜剧被类型过滤）
	got = lib.Query(Query{
		TagOn:  true,
		TagIDs: map[string]bool{idA: true, idC: true},
		Genre:  "动作",
	})
	if len(got) != 1 || got[0].ID != idA {
		t.Fatalf("标签与类型组合应只剩 a，got %v", got)
	}
}
