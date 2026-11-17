package media

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestLibrary 在临时目录上构建一个库，并写入给定的相对路径文件。
func newTestLibrary(t *testing.T, files map[string]string) (*Library, string) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("写入文件失败 %s: %v", rel, err)
		}
	}
	lib := NewLibrary(root, DefaultExts)
	return lib, root
}

func TestLibraryScan(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{
		"a.mp4":             "x",
		"sub/b.mkv":         "yy",
		"note.txt":          "ignored",
		"deep/nested/c.mov": "zzz",
	})

	n, err := lib.Scan()
	if err != nil {
		t.Fatalf("Scan 返回错误: %v", err)
	}
	if n != 3 {
		t.Fatalf("期望索引 3 个视频，实际 %d", n)
	}
	if lib.Len() != 3 {
		t.Fatalf("期望 Len=3，实际 %d", lib.Len())
	}

	// 非视频文件不应入库
	for _, v := range lib.List() {
		if !lib.IsVideoExt(v.RelPath) {
			t.Errorf("非视频文件被入库: %s", v.RelPath)
		}
	}

	// List 应按相对路径排序
	list := lib.List()
	if list[0].RelPath != "a.mp4" || list[1].RelPath != "deep/nested/c.mov" || list[2].RelPath != "sub/b.mkv" {
		t.Errorf("List 排序不符合预期: %v", []string{list[0].RelPath, list[1].RelPath, list[2].RelPath})
	}
}

func TestLibraryGetAndStableID(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{"a.mp4": "x"})
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}

	v, ok := lib.Get(makeID("a.mp4"))
	if !ok {
		t.Fatal("按 ID 查找视频失败")
	}
	if v.Name != "a" || v.Ext != ".mp4" || v.Size != 1 {
		t.Errorf("视频元信息不正确: %+v", v)
	}
	if _, ok := lib.Get("not-exist"); ok {
		t.Error("不存在的 ID 不应命中")
	}

	// 重扫后 ID 保持稳定，且 absPath 仍然有效
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	if _, ok := lib.Get(makeID("a.mp4")); !ok {
		t.Error("重扫后稳定 ID 失效")
	}
}

func TestLibraryUpsert(t *testing.T) {
	lib, root := newTestLibrary(t, map[string]string{"a.mp4": "x"})
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}

	// 非视频扩展名 / 不存在的路径被忽略
	if _, ok := lib.Upsert(filepath.Join(root, "note.txt")); ok {
		t.Error("非视频文件不应被 Upsert")
	}
	if _, ok := lib.Upsert(filepath.Join(root, "missing.mp4")); ok {
		t.Error("不存在的文件不应被 Upsert")
	}

	// 新增视频文件
	newFile := filepath.Join(root, "sub", "b.mp4")
	if err := os.MkdirAll(filepath.Dir(newFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, ok := lib.Upsert(newFile)
	if !ok {
		t.Fatal("新增视频 Upsert 失败")
	}
	if lib.Len() != 2 {
		t.Fatalf("期望 Len=2，实际 %d", lib.Len())
	}
	if v.RelPath != "sub/b.mp4" || v.Size != 5 {
		t.Errorf("Upsert 元信息不正确: %+v", v)
	}

	// 再次 Upsert 同一路径（模拟文件被覆盖）应原地更新而不是新增条目
	if err := os.WriteFile(newFile, []byte("hello-world!!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := lib.Upsert(newFile); !ok {
		t.Fatal("更新视频 Upsert 失败")
	}
	if lib.Len() != 2 {
		t.Fatalf("更新后期望 Len 仍为 2，实际 %d", lib.Len())
	}
	got, _ := lib.Get(v.ID)
	if got.Size != 14 { // "hello-world!!!" 共 14 字节
		t.Errorf("期望更新后 Size=14，实际 %d", got.Size)
	}
}

func TestLibraryRemove(t *testing.T) {
	lib, root := newTestLibrary(t, map[string]string{
		"a.mp4":      "x",
		"sub/b.mp4":  "y",
		"sub/c.mkv":  "z",
		"sub2/d.mov": "w",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}

	// 删除单个文件
	if n := lib.Remove(filepath.Join(root, "a.mp4")); n != 1 {
		t.Errorf("期望移除 1 个，实际 %d", n)
	}
	if lib.Len() != 3 {
		t.Errorf("期望 Len=3，实际 %d", lib.Len())
	}

	// 删除整个目录应连带移除其下全部视频
	if n := lib.Remove(filepath.Join(root, "sub")); n != 2 {
		t.Errorf("期望移除 2 个，实际 %d", n)
	}
	if lib.Len() != 1 {
		t.Errorf("期望 Len=1，实际 %d", lib.Len())
	}

	// 删除不存在的路径不应有副作用
	if n := lib.Remove(filepath.Join(root, "nope")); n != 0 {
		t.Errorf("不存在路径期望移除 0 个，实际 %d", n)
	}
	if lib.Len() != 1 {
		t.Fatalf("期望 Len 仍为 1，实际 %d", lib.Len())
	}
}
