package media

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 与 newTestLibrary 相同的布局，但放在本文件以保持元信息测试自包含。
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("写入文件失败 %s: %v", rel, err)
		}
	}
}

func TestParseNFOFullFields(t *testing.T) {
	dir := t.TempDir()
	nfo := `<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <title>测试电影</title>
  <originaltitle>Test Movie</originaltitle>
  <plot>这是一段剧情简介。</plot>
  <year>2024</year>
  <runtime>128</runtime>
  <ratings><rating default="true"><value>8.5</value></rating></ratings>
  <genre>动作</genre>
  <genre>科幻 / 冒险</genre>
  <director>张三</director>
  <studio>某制片厂</studio>
  <actor><name>李四</name></actor>
  <actor><name>王五</name></actor>
  <uniqueid type="tmdb" default="true">12345</uniqueid>
</movie>`
	nfoPath := filepath.Join(dir, "movie.nfo")
	if err := os.WriteFile(nfoPath, []byte(nfo), 0o644); err != nil {
		t.Fatalf("写 NFO 失败: %v", err)
	}

	md, err := parseNFO(nfoPath)
	if err != nil {
		t.Fatalf("parseNFO 返回错误: %v", err)
	}
	if md.Title != "测试电影" || md.Original != "Test Movie" {
		t.Errorf("片名解析错误: %+v", md)
	}
	if md.Overview != "这是一段剧情简介。" {
		t.Errorf("剧情简介解析错误: %q", md.Overview)
	}
	if md.Year != "2024" || md.Runtime != 128 || md.Rating != 8.5 {
		t.Errorf("年份/片长/评分解析错误: year=%q runtime=%d rating=%v", md.Year, md.Runtime, md.Rating)
	}
	// 单个 <genre> 内的 "科幻 / 冒险" 应被拆成两个类型
	wantGenres := []string{"动作", "科幻", "冒险"}
	if len(md.Genres) != 3 {
		t.Errorf("类型解析错误，期望 %v，实际 %v", wantGenres, md.Genres)
	}
	if len(md.Actors) != 2 || md.Actors[0] != "李四" || md.Directors[0] != "张三" || md.Studio != "某制片厂" {
		t.Errorf("演职人员/出品方解析错误: %+v", md)
	}
}

func TestParseNFOLegacyRatingAndPremiered(t *testing.T) {
	dir := t.TempDir()
	// 旧版 NFO：顶层 <rating> 直接是数字，无 <year> 只有 <premiered>
	nfo := `<movie><premiered>2010-07-16</premiered><rating>7.2</rating><plot>x</plot></movie>`
	nfoPath := filepath.Join(dir, "a.nfo")
	if err := os.WriteFile(nfoPath, []byte(nfo), 0o644); err != nil {
		t.Fatalf("写 NFO 失败: %v", err)
	}
	md, err := parseNFO(nfoPath)
	if err != nil {
		t.Fatalf("parseNFO 返回错误: %v", err)
	}
	if md.Year != "2010" {
		t.Errorf("期望从 premiered 提取年份 2010，实际 %q", md.Year)
	}
	if md.Rating != 7.2 {
		t.Errorf("期望旧版评分 7.2，实际 %v", md.Rating)
	}
	if md.Overview != "x" {
		t.Errorf("剧情简介错误: %q", md.Overview)
	}
}

func TestLoadMetadataSameNameNFOAndPoster(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"films/星际穿越.mp4":        "x",
		"films/星际穿越.nfo":        "<movie><title>星际穿越</title><year>2014</year></movie>",
		"films/星际穿越-poster.jpg": "fake-jpeg",
		"films/星际穿越.fanart.png": "fake-png",
	})
	lib := NewLibrary(root, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	v := lib.List()[0]
	md := lib.Metadata(v.ID)
	if md == nil {
		t.Fatal("期望存在元信息，实际为 nil")
	}
	if md.Source != "nfo" || md.Title != "星际穿越" || md.Year != "2014" {
		t.Errorf("NFO 元信息错误: %+v", md)
	}
	if filepath.Base(md.PosterPath) != "星际穿越-poster.jpg" {
		t.Errorf("海报发现错误: %q", md.PosterPath)
	}
	if filepath.Base(md.BackdropPath) != "星际穿越.fanart.png" {
		t.Errorf("背景图发现错误: %q", md.BackdropPath)
	}
}

func TestLoadMetadataMovieNFOAndFolderArt(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"m/movie.mp4":  "x",
		"m/movie.nfo":  "<movie><title>目录级电影</title></movie>",
		"m/poster.jpg": "img",
		"m/fanart.jpg": "img",
	})
	lib := NewLibrary(root, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	md := lib.Metadata(lib.List()[0].ID)
	if md == nil || md.Title != "目录级电影" {
		t.Fatalf("movie.nfo 解析错误: %+v", md)
	}
	if filepath.Base(md.PosterPath) != "poster.jpg" {
		t.Errorf("目录级 poster.jpg 发现错误: %q", md.PosterPath)
	}
	if filepath.Base(md.BackdropPath) != "fanart.jpg" {
		t.Errorf("目录级 fanart.jpg 发现错误: %q", md.BackdropPath)
	}
}

func TestLoadMetadataNFOArtRelativePath(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"m/movie.mp4":        "x",
		"m/movie.nfo":        `<movie><art><poster>images/cover.jpg</poster></art></movie>`,
		"m/images/cover.jpg": "img",
	})
	lib := NewLibrary(root, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	md := lib.Metadata(lib.List()[0].ID)
	if md == nil || md.PosterPath == "" {
		t.Fatalf("期望解析 NFO art 相对路径，实际: %+v", md)
	}
	if filepath.Base(md.PosterPath) != "cover.jpg" {
		t.Errorf("NFO art 相对路径解析错误: %q", md.PosterPath)
	}
}

func TestMetadataAbsentReturnsNil(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"plain.mp4": "x"})
	lib := NewLibrary(root, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	if md := lib.Metadata(lib.List()[0].ID); md != nil {
		t.Errorf("无 NFO/海报时期望 nil，实际 %+v", md)
	}
}

func TestUpsertLoadsMetadata(t *testing.T) {
	root := t.TempDir()
	lib := NewLibrary(root, DefaultExts)
	writeFiles(t, root, map[string]string{
		"new/a.mp4":        "x",
		"new/a.nfo":        "<movie><title>增量电影</title></movie>",
		"new/a-poster.jpg": "img",
	})
	v, ok := lib.Upsert(filepath.Join(root, "new", "a.mp4"))
	if !ok {
		t.Fatal("Upsert 应返回成功")
	}
	md := lib.Metadata(v.ID)
	if md == nil || md.Title != "增量电影" || md.PosterPath == "" {
		t.Errorf("Upsert 未加载元信息: %+v", md)
	}
}

func TestRemoveCleansMetadata(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"m/movie.mp4": "x",
		"m/movie.nfo": "<movie><title>x</title></movie>",
	})
	lib := NewLibrary(root, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	id := lib.List()[0].ID
	if lib.Metadata(id) == nil {
		t.Fatal("扫描后应存在元信息")
	}
	if n := lib.Remove(filepath.Join(root, "m", "movie.mp4")); n != 1 {
		t.Fatalf("期望移除 1 个视频，实际 %d", n)
	}
	if lib.Metadata(id) != nil {
		t.Error("删除视频后元信息应一并清理")
	}
}

func TestRefreshMetadataSidecarAddAndDelete(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"m/movie.mp4": "x"})
	lib := NewLibrary(root, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	id := lib.List()[0].ID

	// 稍后落地 NFO 与海报，手动刷新后应被发现
	writeFiles(t, root, map[string]string{
		"m/movie.nfo":  "<movie><title>后到的元信息</title></movie>",
		"m/poster.jpg": "img",
	})
	lib.RefreshMetadata(id)
	md := lib.Metadata(id)
	if md == nil || md.Title != "后到的元信息" || md.PosterPath == "" {
		t.Fatalf("刷新后应加载新 NFO/海报: %+v", md)
	}

	// 删除全部附属文件后刷新，元信息应清空
	for _, rel := range []string{"m/movie.nfo", "m/poster.jpg"} {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("删除失败: %v", err)
		}
	}
	lib.RefreshMetadata(id)
	if md := lib.Metadata(id); md != nil {
		t.Errorf("附属文件删除后期望 nil，实际 %+v", md)
	}
}

func TestIsMetadataSidecar(t *testing.T) {
	cases := map[string]bool{
		"movie.nfo":    true,
		"x.NFO":        true,
		"x-poster.jpg": true,
		"folder.PNG":   true,
		"cover.webp":   true,
		"movie.mp4":    false,
		"sub.srt":      false,
		"notes.txt":    false,
	}
	for name, want := range cases {
		if got := IsMetadataSidecar(name); got != want {
			t.Errorf("IsMetadataSidecar(%q) = %v，期望 %v", name, got, want)
		}
	}
}

// TestWatcherMetadataRefresh 验证 NFO/海报落地后 Watcher 自动刷新元信息。
func TestWatcherMetadataRefresh(t *testing.T) {
	lib, _, root := newTestWatcher(t, 30*time.Millisecond)
	writeFiles(t, root, map[string]string{"m/movie.mp4": "x"})
	waitFor(t, 2*time.Second, func() bool { return lib.Len() == 1 })
	id := lib.List()[0].ID

	// 落地 NFO，等待 Watcher 防抖刷新
	writeFiles(t, root, map[string]string{"m/movie.nfo": "<movie><title>监听刷新</title></movie>"})
	waitFor(t, 3*time.Second, func() bool {
		md := lib.Metadata(id)
		return md != nil && md.Title == "监听刷新"
	})

	// 再落地海报，应被增量发现
	writeFiles(t, root, map[string]string{"m/movie-poster.jpg": "img"})
	waitFor(t, 3*time.Second, func() bool {
		md := lib.Metadata(id)
		return md != nil && md.PosterPath != ""
	})

	// 删除 NFO 后元信息应被清空（海报仍在，但无 NFO 时仍有本地海报元信息，
	// 因此这里删除全部附属文件验证完整回收）
	for _, rel := range []string{"m/movie.nfo", "m/movie-poster.jpg"} {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("删除失败: %v", err)
		}
	}
	waitFor(t, 3*time.Second, func() bool { return lib.Metadata(id) == nil })
}
