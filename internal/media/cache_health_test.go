package media

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCacheFile 写入缓存文件；age>0 时把 mtime 调到过去以脱离保护期，age<=0 保持当前时间。
func writeCacheFile(t *testing.T, path, content string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建缓存目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入缓存文件失败 %s: %v", path, err)
	}
	if age > 0 {
		old := time.Now().Add(-age)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("Chtimes 失败: %v", err)
		}
	}
}

// ageDir 把目录本身的 mtime 调到过去（TMDB 保护期判定读的是目录 mtime）。
func ageDir(t *testing.T, path string, age time.Duration) {
	t.Helper()
	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("目录 Chtimes 失败: %v", err)
	}
}

// scannedTestLibrary 构建并完成扫描的测试库，返回库与首个视频 ID。
func scannedTestLibrary(t *testing.T, files map[string]string) (*Library, string) {
	t.Helper()
	lib, _ := newTestLibrary(t, files)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	if lib.Len() == 0 {
		t.Fatalf("测试库为空")
	}
	return lib, lib.List()[0].ID
}

func TestInspectThumbOrphanAndHit(t *testing.T) {
	lib, id := scannedTestLibrary(t, map[string]string{"a.mp4": "x"})
	dir := t.TempDir()
	writeCacheFile(t, filepath.Join(dir, id+".jpg"), "hit", time.Hour)
	writeCacheFile(t, filepath.Join(dir, "0123456789abcdef.jpg"), "orphan", time.Hour)

	rep := inspectThumbDir(lib, dir)
	if !rep.Configured || !rep.Exists {
		t.Fatalf("目录已配置且存在，实际 Configured=%v Exists=%v", rep.Configured, rep.Exists)
	}
	if rep.TotalCount != 2 {
		t.Fatalf("合规条目应为 2，实际 %d", rep.TotalCount)
	}
	if len(rep.Orphans) != 1 || rep.Orphans[0].Name != "0123456789abcdef.jpg" {
		t.Fatalf("应识别 1 个孤儿，实际 %+v", rep.Orphans)
	}
	if rep.Orphans[0].Protected {
		t.Fatalf("旧 mtime 孤儿不应在保护期内")
	}
	if len(rep.Missing) != 0 {
		t.Fatalf("库内视频有缩略图，不应有缺失，实际 %d", len(rep.Missing))
	}
	if rep.TotalSize != 9 { // "hit"(3) + "orphan"(6)
		t.Fatalf("总字节应为 9，实际 %d", rep.TotalSize)
	}
}

func TestInspectThumbUnidentified(t *testing.T) {
	lib, _ := scannedTestLibrary(t, map[string]string{"a.mp4": "x"})
	dir := t.TempDir()
	writeCacheFile(t, filepath.Join(dir, "readme.txt"), "n", time.Hour)
	writeCacheFile(t, filepath.Join(dir, "abc.jpg"), "n", time.Hour) // 非 16hex
	writeCacheFile(t, filepath.Join(dir, "subdir", "inner.jpg"), "n", time.Hour)

	rep := inspectThumbDir(lib, dir)
	if rep.TotalCount != 0 {
		t.Fatalf("不合规条目不计入合规总数，实际 %d", rep.TotalCount)
	}
	if len(rep.Unidentified) != 3 {
		t.Fatalf("未识别条目应为 3，实际 %d", len(rep.Unidentified))
	}
}

func TestInspectThumbMissing(t *testing.T) {
	lib, id := scannedTestLibrary(t, map[string]string{"a.mp4": "x"})
	dir := t.TempDir()

	rep := inspectThumbDir(lib, dir)
	if rep.MissingCount != 1 || len(rep.Missing) != 1 {
		t.Fatalf("缺失计数应为 1，实际 count=%d len=%d", rep.MissingCount, len(rep.Missing))
	}
	if rep.Missing[0].Name != id+".jpg" || !rep.Missing[0].Missing {
		t.Fatalf("缺失条目不符合预期: %+v", rep.Missing[0])
	}
}

func TestInspectThumbProtected(t *testing.T) {
	lib, _ := scannedTestLibrary(t, map[string]string{"a.mp4": "x"})
	dir := t.TempDir()
	writeCacheFile(t, filepath.Join(dir, "0123456789abcdef.jpg"), "old", time.Hour)
	writeCacheFile(t, filepath.Join(dir, "fedcba9876543210.jpg"), "new", 0)

	rep := inspectThumbDir(lib, dir)
	if len(rep.Orphans) != 2 {
		t.Fatalf("应有 2 个孤儿，实际 %d", len(rep.Orphans))
	}
	got := map[string]bool{}
	for _, e := range rep.Orphans {
		got[e.Name] = e.Protected
	}
	if got["0123456789abcdef.jpg"] {
		t.Fatalf("旧文件不应受保护")
	}
	if !got["fedcba9876543210.jpg"] {
		t.Fatalf("新写入文件应在保护期内")
	}
}

func TestDeleteThumbOrphans(t *testing.T) {
	lib, id := scannedTestLibrary(t, map[string]string{"a.mp4": "x"})
	dir := t.TempDir()
	hit := filepath.Join(dir, id+".jpg")
	oldOrphan := filepath.Join(dir, "0123456789abcdef.jpg")
	protectedOrphan := filepath.Join(dir, "fedcba9876543210.jpg")
	writeCacheFile(t, hit, "hit", time.Hour)
	writeCacheFile(t, oldOrphan, "orphan", time.Hour) // 6 字节
	writeCacheFile(t, protectedOrphan, "new", 0)

	removed, reclaimed, err := DeleteThumbOrphans(lib, dir)
	if err != nil {
		t.Fatalf("清理返回错误: %v", err)
	}
	if removed != 1 || reclaimed != 6 {
		t.Fatalf("应删除 1 个回收 6 字节，实际 removed=%d reclaimed=%d", removed, reclaimed)
	}
	if _, err := os.Stat(hit); err != nil {
		t.Fatalf("命中文件应保留: %v", err)
	}
	if _, err := os.Stat(oldOrphan); !os.IsNotExist(err) {
		t.Fatalf("旧孤儿应已删除")
	}
	if _, err := os.Stat(protectedOrphan); err != nil {
		t.Fatalf("保护期内孤儿应保留: %v", err)
	}
}

func TestDeleteThumbEmptyLibraryBlocks(t *testing.T) {
	lib := NewLibrary(t.TempDir(), DefaultExts) // 未扫描，空库
	dir := t.TempDir()
	orphan := filepath.Join(dir, "0123456789abcdef.jpg")
	writeCacheFile(t, orphan, "x", time.Hour)
	tmdbDir := t.TempDir()
	tmdbOrphan := filepath.Join(tmdbDir, "0123456789abcdef", "poster.jpg")
	writeCacheFile(t, tmdbOrphan, "x", time.Hour)
	ageDir(t, filepath.Join(tmdbDir, "0123456789abcdef"), time.Hour)

	if _, _, err := DeleteThumbOrphans(lib, dir); !errors.Is(err, ErrLibraryEmpty) {
		t.Fatalf("空库缩略图清理应返回 ErrLibraryEmpty，实际 %v", err)
	}
	if _, _, err := DeleteScraperOrphans(lib, tmdbDir); !errors.Is(err, ErrLibraryEmpty) {
		t.Fatalf("空库 TMDB 清理应返回 ErrLibraryEmpty，实际 %v", err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("被护栏拦截后文件应保留: %v", err)
	}
	if _, err := os.Stat(tmdbOrphan); err != nil {
		t.Fatalf("被护栏拦截后目录应保留: %v", err)
	}
}

func TestInspectTMDBTree(t *testing.T) {
	lib, id := scannedTestLibrary(t, map[string]string{"a.mp4": "x"})
	dir := t.TempDir()
	writeCacheFile(t, filepath.Join(dir, id, "poster.jpg"), "poster", time.Hour)
	writeCacheFile(t, filepath.Join(dir, id, "metadata.json"), "{}", time.Hour)
	writeCacheFile(t, filepath.Join(dir, "0123456789abcdef", "poster.jpg"), "bigfile", time.Hour)
	ageDir(t, filepath.Join(dir, id), time.Hour)
	ageDir(t, filepath.Join(dir, "0123456789abcdef"), time.Hour)

	rep := inspectTMDBDir(lib, dir)
	if rep.TotalCount != 2 {
		t.Fatalf("合规子目录应为 2，实际 %d", rep.TotalCount)
	}
	if len(rep.Orphans) != 1 || rep.Orphans[0].Name != "0123456789abcdef" {
		t.Fatalf("应识别 1 个孤儿目录，实际 %+v", rep.Orphans)
	}
	if rep.Orphans[0].Size != 7 { // "bigfile"
		t.Fatalf("孤儿目录递归大小应为 7，实际 %d", rep.Orphans[0].Size)
	}
	if rep.TotalSize != 15 { // poster(6)+{}(2)+bigfile(7)
		t.Fatalf("总字节应为 15，实际 %d", rep.TotalSize)
	}
}

func TestInspectTMDBUnidentified(t *testing.T) {
	lib, _ := scannedTestLibrary(t, map[string]string{"a.mp4": "x"})
	dir := t.TempDir()
	writeCacheFile(t, filepath.Join(dir, "loose.json"), "{}", time.Hour)
	writeCacheFile(t, filepath.Join(dir, "nothex", "poster.jpg"), "x", time.Hour)

	rep := inspectTMDBDir(lib, dir)
	if rep.TotalCount != 0 {
		t.Fatalf("不合规条目不入合规总数，实际 %d", rep.TotalCount)
	}
	if len(rep.Unidentified) != 2 {
		t.Fatalf("未识别条目应为 2，实际 %d", len(rep.Unidentified))
	}
}

func TestInspectTMDBDeleteRemovesWholeDir(t *testing.T) {
	lib, id := scannedTestLibrary(t, map[string]string{"a.mp4": "x"})
	dir := t.TempDir()
	hitDir := filepath.Join(dir, id)
	orphanDir := filepath.Join(dir, "0123456789abcdef")
	writeCacheFile(t, filepath.Join(hitDir, "poster.jpg"), "hit", time.Hour)
	writeCacheFile(t, filepath.Join(orphanDir, "poster.jpg"), "orphan", time.Hour)
	writeCacheFile(t, filepath.Join(orphanDir, "metadata.json"), "{}", time.Hour)
	ageDir(t, hitDir, time.Hour)
	ageDir(t, orphanDir, time.Hour)

	removed, reclaimed, err := DeleteScraperOrphans(lib, dir)
	if err != nil {
		t.Fatalf("清理返回错误: %v", err)
	}
	if removed != 1 || reclaimed != 8 { // "orphan"(6) + "{}"(2)
		t.Fatalf("应删除 1 目录回收 8 字节，实际 removed=%d reclaimed=%d", removed, reclaimed)
	}
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("孤儿目录应被整体删除")
	}
	if _, err := os.Stat(hitDir); err != nil {
		t.Fatalf("命中目录应原样保留: %v", err)
	}
}

func TestInspectCacheDisabledAndMissing(t *testing.T) {
	lib, _ := scannedTestLibrary(t, map[string]string{"a.mp4": "x"})

	// 空串目录：未配置
	rep := inspectThumbDir(lib, "")
	if rep.Configured {
		t.Fatalf("空串目录应为未配置")
	}
	if rep.Orphans == nil || rep.Unidentified == nil || rep.Missing == nil {
		t.Fatalf("未配置时切片也必须非 nil")
	}

	// 不存在路径：已配置但不存在
	rep = inspectTMDBDir(lib, filepath.Join(t.TempDir(), "not-exist"))
	if !rep.Configured || rep.Exists {
		t.Fatalf("不存在目录应为 Configured=true Exists=false")
	}
	if rep.Orphans == nil || rep.Unidentified == nil || rep.Missing == nil {
		t.Fatalf("不存在时切片也必须非 nil")
	}

	// 顶层合并入口同样不应报错
	health := InspectCache(lib, "", "")
	if health.Thumb.Configured || health.TMDB.Configured {
		t.Fatalf("两类均未配置时 Configured 应为 false")
	}
}

func TestCacheReportSortedAndNonNil(t *testing.T) {
	lib, _ := scannedTestLibrary(t, map[string]string{
		"a.mp4": "x",
		"b.mkv": "yy",
		"c.mov": "zzz",
	})
	dir := t.TempDir()
	// 乱序写入三个孤儿
	writeCacheFile(t, filepath.Join(dir, "cccccccccccccccc.jpg"), "c", time.Hour)
	writeCacheFile(t, filepath.Join(dir, "aaaaaaaaaaaaaaaa.jpg"), "a", time.Hour)
	writeCacheFile(t, filepath.Join(dir, "bbbbbbbbbbbbbbbb.jpg"), "b", time.Hour)
	// 未识别条目同样乱序
	writeCacheFile(t, filepath.Join(dir, "z.txt"), "z", time.Hour)
	writeCacheFile(t, filepath.Join(dir, "a.txt"), "a", time.Hour)

	rep := inspectThumbDir(lib, dir)
	want := []string{"aaaaaaaaaaaaaaaa.jpg", "bbbbbbbbbbbbbbbb.jpg", "cccccccccccccccc.jpg"}
	for i, e := range rep.Orphans {
		if e.Name != want[i] {
			t.Fatalf("孤儿未按 Name 升序，索引 %d 期望 %s 实际 %s", i, want[i], e.Name)
		}
	}
	if rep.Unidentified[0].Name != "a.txt" || rep.Unidentified[1].Name != "z.txt" {
		t.Fatalf("未识别条目未排序: %+v", rep.Unidentified)
	}
	// 三个库内视频都没有缩略图 → 缺失清单也应有序
	if len(rep.Missing) != 3 {
		t.Fatalf("缺失应为 3 条，实际 %d", len(rep.Missing))
	}
	for i := 1; i < len(rep.Missing); i++ {
		if rep.Missing[i-1].Name > rep.Missing[i].Name {
			t.Fatalf("缺失清单未按 Name 升序")
		}
	}

	// 空目录：各切片非 nil 且长度为 0
	empty := inspectThumbDir(lib, t.TempDir())
	if len(empty.Orphans) != 0 || len(empty.Unidentified) != 0 {
		t.Fatalf("空目录不应有孤儿/未识别条目")
	}
	if empty.Orphans == nil || empty.Unidentified == nil || empty.Missing == nil {
		t.Fatalf("空目录切片必须非 nil")
	}
}
