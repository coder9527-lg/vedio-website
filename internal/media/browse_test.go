package media

import (
	"reflect"
	"testing"
)

// browseFixture 构造多层目录库：剧A 下两季各一集、根散文件、中文空格目录、仅含 txt 的目录。
func browseFixture(t *testing.T) *Library {
	t.Helper()
	lib, _ := newTestLibrary(t, map[string]string{
		"剧A/S01/e1.mkv":       "a",       // 长度 1
		"剧A/S02/e2.mkv":       "bb",      // 长度 2
		"电影 2024/电影 2024.mp4": "ccc",     // 长度 3，目录与文件名均含空格
		"根散文件.mov":            "dddd",    // 长度 4
		"杂项/readme.txt":       "ignored", // 非视频，整目录不应可见
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	return lib
}

func TestBrowseRoot(t *testing.T) {
	lib := browseFixture(t)
	view := lib.Browse("")

	if !view.Exists {
		t.Fatal("根目录应恒存在")
	}
	if view.Path != "" {
		t.Fatalf("根路径应为空串，实际 %q", view.Path)
	}
	// 直接子目录：剧A（递归 2 集、3 字节）、电影 2024（1 个、3 字节）；「杂项」仅含 txt 不可见
	wantDirs := map[string]struct {
		count int
		size  int64
	}{
		"剧A":      {2, 3},
		"电影 2024": {1, 3},
	}
	if len(view.Dirs) != len(wantDirs) {
		t.Fatalf("根目录子目录数不符: %+v", view.Dirs)
	}
	for _, d := range view.Dirs {
		want, ok := wantDirs[d.Name]
		if !ok {
			t.Fatalf("出现未预期的子目录: %s", d.Name)
		}
		if d.Count != want.count || d.Size != want.size {
			t.Fatalf("子目录 %s 聚合值不符: Count=%d Size=%d", d.Name, d.Count, d.Size)
		}
		if d.Path != d.Name {
			t.Fatalf("根下一级目录 Path 应等于 Name，实际 %q", d.Path)
		}
	}
	// 目录按名称升序
	if view.Dirs[0].Name != "剧A" || view.Dirs[1].Name != "电影 2024" {
		t.Fatalf("子目录未按名称升序: %v", []string{view.Dirs[0].Name, view.Dirs[1].Name})
	}
	// 根散文件进直接视频
	if len(view.Videos) != 1 || view.Videos[0].RelPath != "根散文件.mov" {
		t.Fatalf("根散文件归类错误: %+v", view.Videos)
	}
}

func TestBrowseEnterShow(t *testing.T) {
	lib := browseFixture(t)
	view := lib.Browse("剧A")

	if !view.Exists || view.Path != "剧A" {
		t.Fatalf("剧A 视图状态错误: %+v", view)
	}
	// 直接子目录 S01/S02 各 1 集，无直接视频
	if len(view.Dirs) != 2 || view.Dirs[0].Name != "S01" || view.Dirs[1].Name != "S02" {
		t.Fatalf("剧A 下应为 S01/S02 两个子目录: %+v", view.Dirs)
	}
	for _, d := range view.Dirs {
		if d.Count != 1 {
			t.Fatalf("子目录 %s 应含 1 个视频，实际 %d", d.Name, d.Count)
		}
		if d.Path != "剧A/"+d.Name {
			t.Fatalf("子目录完整路径错误: %q", d.Path)
		}
	}
	if len(view.Videos) != 0 {
		t.Fatalf("剧A 下无直接视频，实际 %d 个", len(view.Videos))
	}
}

func TestBrowseEnterSeason(t *testing.T) {
	lib := browseFixture(t)
	view := lib.Browse("剧A/S01")

	if !view.Exists {
		t.Fatal("剧A/S01 应存在")
	}
	if len(view.Dirs) != 0 {
		t.Fatalf("季目录下不应再有子目录: %+v", view.Dirs)
	}
	if len(view.Videos) != 1 || view.Videos[0].RelPath != "剧A/S01/e1.mkv" {
		t.Fatalf("季目录直接视频错误: %+v", view.Videos)
	}
}

func TestBrowseDeterministic(t *testing.T) {
	lib := browseFixture(t)
	first := lib.Browse("")
	second := lib.Browse("")
	if !reflect.DeepEqual(first, second) {
		t.Fatal("连续两次根浏览结果应完全一致")
	}
	first = lib.Browse("剧A")
	second = lib.Browse("剧A")
	if !reflect.DeepEqual(first, second) {
		t.Fatal("连续两次子目录浏览结果应完全一致")
	}
}

func TestBrowseNotExist(t *testing.T) {
	lib := browseFixture(t)
	view := lib.Browse("不存在的目录")
	if view.Exists {
		t.Fatal("不存在的目录 Exists 应为 false")
	}
	if len(view.Dirs) != 0 || len(view.Videos) != 0 {
		t.Fatal("不存在的目录不应返回任何条目")
	}
}

func TestBrowseEmptyLibrary(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{})
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	view := lib.Browse("")
	if !view.Exists {
		t.Fatal("空库根目录仍应存在")
	}
	if view.Dirs == nil || view.Videos == nil {
		t.Fatal("空库切片也应初始化为非 nil")
	}
	if len(view.Dirs) != 0 || len(view.Videos) != 0 {
		t.Fatal("空库浏览不应有条目")
	}
}

func TestBrowseChineseAndSpace(t *testing.T) {
	lib := browseFixture(t)
	view := lib.Browse("电影 2024")
	if !view.Exists {
		t.Fatal("含空格的中文目录应存在")
	}
	if len(view.Dirs) != 0 || len(view.Videos) != 1 {
		t.Fatalf("中文空格目录内容错误: %+v", view)
	}
	if got := view.Videos[0].RelPath; got != "电影 2024/电影 2024.mp4" {
		t.Fatalf("中文空格路径分段错误: %q", got)
	}
}

func TestBrowseNonVideoDirInvisible(t *testing.T) {
	lib := browseFixture(t)
	// 「杂项」目录只含 txt，索引中没有任何视频归属，浏览根不可见、直接访问也不存在
	root := lib.Browse("")
	for _, d := range root.Dirs {
		if d.Name == "杂项" {
			t.Fatal("仅含非视频文件的目录不应出现在浏览结果中")
		}
	}
	view := lib.Browse("杂项")
	if view.Exists {
		t.Fatal("仅含非视频文件的目录 Exists 应为 false")
	}
}
