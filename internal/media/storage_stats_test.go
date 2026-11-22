package media

import (
	"reflect"
	"strings"
	"testing"
)

// findBucket 在桶切片中按名称查找，找不到返回 nil，供断言复用。
func findBucket(buckets []StorageBucket, name string) *StorageBucket {
	for i := range buckets {
		if buckets[i].Name == name {
			return &buckets[i]
		}
	}
	return nil
}

// TestStorageStatsEmpty 空库不 panic，计数为 0 且切片非 nil（保证模板 range 安全）。
func TestStorageStatsEmpty(t *testing.T) {
	lib, _ := newTestLibrary(t, nil)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描空库失败: %v", err)
	}

	stats := lib.StorageStats()
	if stats.TotalCount != 0 || stats.TotalSize != 0 || stats.DirCount != 0 {
		t.Errorf("空库计数应全为 0，得到 %+v", stats)
	}
	if stats.Dirs == nil || stats.Exts == nil || stats.Duplicates == nil {
		t.Error("空库返回的切片应为非 nil 的空切片")
	}
	if len(stats.Dirs) != 0 || len(stats.Exts) != 0 || len(stats.Duplicates) != 0 {
		t.Error("空库各切片长度应为 0")
	}
}

// TestStorageStatsTotals 总量等于夹具文件数与内容字节数之和。
func TestStorageStatsTotals(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{
		"a.mp4":     "x",   // 1 字节
		"sub/b.mkv": "yy",  // 2 字节
		"c.mp4":     "zzz", // 3 字节
		"note.txt":  "ignored",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}

	stats := lib.StorageStats()
	if stats.TotalCount != 3 {
		t.Errorf("视频总数 = %d，期望 3（txt 不计入）", stats.TotalCount)
	}
	if stats.TotalSize != 6 {
		t.Errorf("总字节 = %d，期望 6", stats.TotalSize)
	}
}

// TestStorageStatsDirAggregation 目录只按一级路径聚合，深层嵌套仍计入顶层目录桶。
func TestStorageStatsDirAggregation(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{
		"sub/a.mp4":      "x",
		"sub/deep/b.mkv": "yy",
		"other/c.mov":    "zzz",
		"root.mp4":       "wwww",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}

	stats := lib.StorageStats()
	if stats.DirCount != len(stats.Dirs) {
		t.Errorf("DirCount = %d，应等于 Dirs 长度 %d", stats.DirCount, len(stats.Dirs))
	}
	sub := findBucket(stats.Dirs, "sub")
	if sub == nil {
		t.Fatal("缺少 sub 目录桶")
	}
	if sub.Count != 2 || sub.Size != 3 {
		t.Errorf("sub 桶 = %+v，期望 Count=2 Size=3", sub)
	}
	other := findBucket(stats.Dirs, "other")
	if other == nil || other.Count != 1 || other.Size != 3 {
		t.Errorf("other 桶异常: %+v", other)
	}
	// 不含分隔符的散文件归入「根目录」桶。
	root := findBucket(stats.Dirs, rootDirBucket)
	if root == nil || root.Count != 1 || root.Size != 4 {
		t.Errorf("根目录桶异常: %+v", root)
	}
}

// TestStorageStatsRootBucketName 校验根目录桶常量文案，避免展示层硬编码漂移。
func TestStorageStatsRootBucketName(t *testing.T) {
	if rootDirBucket != "根目录" {
		t.Errorf("根目录桶名 = %q，期望「根目录」", rootDirBucket)
	}
}

// TestStorageStatsExtAggregation 同扩展名多文件正确合并数量与字节数。
func TestStorageStatsExtAggregation(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{
		"a.mp4": "x",
		"b.mp4": "yy",
		"c.mkv": "zzz",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}

	stats := lib.StorageStats()
	mp4 := findBucket(stats.Exts, ".mp4")
	if mp4 == nil {
		t.Fatal("缺少 .mp4 扩展名桶")
	}
	if mp4.Count != 2 || mp4.Size != 3 {
		t.Errorf(".mp4 桶 = %+v，期望 Count=2 Size=3", mp4)
	}
	mkv := findBucket(stats.Exts, ".mkv")
	if mkv == nil || mkv.Count != 1 || mkv.Size != 3 {
		t.Errorf(".mkv 桶异常: %+v", mkv)
	}
}

// TestStorageStatsDuplicates 同名且同大小的文件成组，组内按 RelPath 升序。
func TestStorageStatsDuplicates(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{
		"dir-b/dup.mp4": "same", // 4 字节
		"dir-a/dup.mp4": "same", // 4 字节
		"unique.mp4":    "same", // 同名不同（Name 不同），不成组
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}

	stats := lib.StorageStats()
	if len(stats.Duplicates) != 1 {
		t.Fatalf("疑似重复组数 = %d，期望 1", len(stats.Duplicates))
	}
	g := stats.Duplicates[0]
	if g.Name != "dup" || g.Size != 4 || len(g.Videos) != 2 {
		t.Fatalf("重复组异常: %+v", g)
	}
	// 组内按相对路径升序：dir-a 在 dir-b 之前。
	if g.Videos[0].RelPath != "dir-a/dup.mp4" || g.Videos[1].RelPath != "dir-b/dup.mp4" {
		t.Errorf("组内排序错误: %s, %s", g.Videos[0].RelPath, g.Videos[1].RelPath)
	}
}

// TestStorageStatsDuplicateNegative 同名不同大小、同大小不同名都不算重复。
func TestStorageStatsDuplicateNegative(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{
		"a/dup.mp4": "short",                 // 5 字节
		"b/dup.mp4": "a much longer content", // 同名但大小不同
		"x.mp4":     "same",                  // 4 字节
		"y.mkv":     "same",                  // 同大小但 Name 不同
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}

	stats := lib.StorageStats()
	if len(stats.Duplicates) != 0 {
		var names []string
		for _, g := range stats.Duplicates {
			names = append(names, g.Name)
		}
		t.Errorf("不应存在重复组，得到 %v", names)
	}
}

// TestStorageStatsDeterministic 连续两次聚合结果完全一致（含所有切片顺序）。
func TestStorageStatsDeterministic(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{
		"zeta/a.mp4":       strings.Repeat("a", 10),
		"alpha/b.mp4":      strings.Repeat("b", 5),
		"alpha/deep/c.mkv": strings.Repeat("c", 10),
		"root.mp4":         strings.Repeat("d", 3),
		"d/dup.mp4":        "dup",
		"e/dup.mp4":        "dup",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}

	first := lib.StorageStats()
	second := lib.StorageStats()
	if !reflect.DeepEqual(first, second) {
		t.Errorf("两次聚合结果不一致:\nfirst=%+v\nsecond=%+v", first, second)
	}

	// 桶按 Size 降序排列：alpha 桶 15 字节应排在 zeta 桶 10 字节之前。
	if len(first.Dirs) < 2 || first.Dirs[0].Name != "alpha" {
		t.Errorf("目录桶未按 Size 降序: %+v", first.Dirs)
	}
}
