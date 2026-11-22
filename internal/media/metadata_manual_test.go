package media

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newManualTestLibrary 构造含一个视频、并登记了刮削缓存目录的库（缓存目录可写）。
func newManualTestLibrary(t *testing.T) (*Library, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.mp4"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("写测试视频失败: %v", err)
	}
	lib := NewLibrary(root, DefaultExts)
	lib.SetScraperImageDir(t.TempDir())
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	vid := lib.List()[0]
	return lib, vid.ID, root
}

// TestMergeManualOverwritesCoreFields 验证手动值覆盖核心字段、空值不覆盖、图片路径不受影响。
func TestMergeManualOverwritesCoreFields(t *testing.T) {
	base := &Metadata{
		Title:        "刮削片名",
		Original:     "Scraped",
		Overview:     "刮削简介",
		Year:         "2020",
		Runtime:      100,
		Rating:       7.0,
		Genres:       []string{"动作"},
		Directors:    []string{"原导演"},
		Actors:       []string{"原演员"},
		Studio:       "原厂牌",
		PosterPath:   "/cache/poster.jpg",
		BackdropPath: "/cache/backdrop.jpg",
		Source:       "tmdb",
	}
	manual := &Metadata{
		Title:  "手动片名",
		Year:   "2024",
		Rating: 9.1,
		Genres: []string{"剧情", "悬疑"},
		// Original/Overview/Runtime/Studio/Directors/Actors 留零值，应保留 base
	}

	out := mergeManual(base, manual)
	if out.Title != "手动片名" || out.Year != "2024" || out.Rating != 9.1 {
		t.Fatalf("手动标量字段未生效: %+v", out)
	}
	if out.Original != "Scraped" || out.Overview != "刮削简介" || out.Runtime != 100 || out.Studio != "原厂牌" {
		t.Fatalf("手动未填字段不应覆盖 base: %+v", out)
	}
	if len(out.Genres) != 2 || out.Genres[0] != "剧情" || out.Genres[1] != "悬疑" {
		t.Fatalf("类型应被手动切片整体替换: %+v", out.Genres)
	}
	if len(out.Directors) != 1 || out.Directors[0] != "原导演" {
		t.Fatalf("nil 切片应保留 base 原值: %+v", out.Directors)
	}
	// 图片与来源不在手动编辑范围，必须原样保留
	if out.PosterPath != "/cache/poster.jpg" || out.BackdropPath != "/cache/backdrop.jpg" || out.Source != "tmdb" {
		t.Fatalf("图片路径/来源不应被手动编辑影响: %+v", out)
	}
	// base 本身不能被修改（mergeManual 应在副本上操作）
	if base.Title != "刮削片名" || len(base.Genres) != 1 {
		t.Fatalf("mergeManual 修改了 base 原值: %+v", base)
	}
}

// TestMergeManualEmptySliceClearsField 验证空切片表示主动清空、nil 切片表示保留。
func TestMergeManualEmptySliceClearsField(t *testing.T) {
	base := &Metadata{Title: "x", Genres: []string{"动作"}, Actors: []string{"演员"}}
	// Genres 给空切片（非 nil）→ 清空；Actors 不给（nil）→ 保留
	out := mergeManual(base, &Metadata{Title: "x", Genres: []string{}})
	if out.Genres != nil && len(out.Genres) != 0 {
		t.Fatalf("空切片应清空类型，实际: %+v", out.Genres)
	}
	if len(out.Actors) != 1 {
		t.Fatalf("nil 切片应保留演员，实际: %+v", out.Actors)
	}

	// manual 为空/nil 时直接返回 base
	if got := mergeManual(base, nil); got != base {
		t.Fatal("manual 为 nil 应原样返回 base")
	}
	if got := mergeManual(base, &Metadata{}); got != base {
		t.Fatal("manual 全空应原样返回 base")
	}
	// base 为 nil 时返回 manual 的副本（修改返回值不应反向影响入参）
	manual := &Metadata{Title: "仅手动"}
	got := mergeManual(nil, manual)
	if got == nil || got.Title != "仅手动" {
		t.Fatalf("base 为 nil 时应返回 manual 副本: %+v", got)
	}
	got.Title = "改了"
	if manual.Title != "仅手动" {
		t.Fatal("base 为 nil 分支应返回副本而非 manual 本身")
	}
}

// TestSaveManualMetadataLifecycle 验证保存→内存生效→文件落盘→清空回退的完整生命周期。
func TestSaveManualMetadataLifecycle(t *testing.T) {
	lib, videoID, _ := newManualTestLibrary(t)

	manual := &Metadata{
		Title:     "我的手动片名",
		Year:      "2024",
		Runtime:   120,
		Rating:    8.8,
		Genres:    []string{"剧情"},
		Directors: []string{"导演甲"},
		Actors:    []string{"演员乙"},
		Studio:    "手动厂牌",
		Overview:  "手动简介",
	}
	if err := lib.SaveManualMetadata(videoID, manual); err != nil {
		t.Fatalf("保存手动元信息失败: %v", err)
	}

	// 内存立即生效（此前该视频无任何元信息）
	got := lib.Metadata(videoID)
	if got == nil || got.Title != "我的手动片名" || got.Runtime != 120 || got.Rating != 8.8 {
		t.Fatalf("保存后内存元信息不符: %+v", got)
	}

	// manual.json 确实落盘且可被重新加载
	path := filepath.Join(lib.ScraperImageDir(), videoID, "manual.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("manual.json 未生成: %v", err)
	}
	if reloaded := lib.loadManualMetadata(videoID); reloaded == nil || reloaded.Title != "我的手动片名" {
		t.Fatalf("重新加载 manual.json 失败: %+v", reloaded)
	}

	// 全量重扫后手动值仍应生效（Scan 叠加 mergeManual）
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("重扫失败: %v", err)
	}
	if got := lib.Metadata(videoID); got == nil || got.Title != "我的手动片名" {
		t.Fatalf("重扫后手动值丢失: %+v", got)
	}

	// 清空：传 nil 应删除文件并回退到无元信息状态
	if err := lib.SaveManualMetadata(videoID, nil); err != nil {
		t.Fatalf("清空手动元信息失败: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("清空后 manual.json 应被删除，stat err=%v", err)
	}
	if md := lib.Metadata(videoID); md != nil {
		t.Fatalf("清空且无 NFO/刮削底值时应无元信息，实际: %+v", md)
	}
}

// TestSaveManualMetadataWinsOverScrapedAndNFO 验证三级合并优先级：手动 > 刮削缓存 > 本地 NFO。
func TestSaveManualMetadataWinsOverScrapedAndNFO(t *testing.T) {
	lib, videoID, root := newManualTestLibrary(t)

	// 本地 NFO 提供底值
	nfo := `<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <title>NFO 片名</title>
  <year>2019</year>
  <runtime>90</runtime>
  <genre>纪录</genre>
</movie>`
	if err := os.WriteFile(filepath.Join(root, "movie.nfo"), []byte(nfo), 0o644); err != nil {
		t.Fatalf("写 NFO 失败: %v", err)
	}
	// 刮削缓存 metadata.json 提供第二级值
	scraped := &Metadata{Title: "刮削片名", Year: "2021", Runtime: 110, Genres: []string{"动作"}, Source: "tmdb"}
	if err := writeJSONAtomic(filepath.Join(lib.ScraperImageDir(), videoID, "metadata.json"), scraped); err != nil {
		t.Fatalf("写刮削缓存失败: %v", err)
	}
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("重扫失败: %v", err)
	}
	// NFO 非空字段在 mergeMetadata 中优先于刮削值，刮削仅补 NFO 缺失字段
	before := lib.Metadata(videoID)
	if before == nil || before.Title != "NFO 片名" || before.Source != "nfo+tmdb" {
		t.Fatalf("NFO/刮削合并结果不符: %+v", before)
	}

	// 手动只改片名与类型，其余字段保留 NFO/刮削合并值；手动值优先级最高
	if err := lib.SaveManualMetadata(videoID, &Metadata{Title: "手动片名", Genres: []string{"悬疑"}}); err != nil {
		t.Fatalf("保存手动值失败: %v", err)
	}
	after := lib.Metadata(videoID)
	if after == nil {
		t.Fatal("保存后元信息不应为 nil")
	}
	if after.Title != "手动片名" {
		t.Fatalf("手动片名应胜出，实际 %q", after.Title)
	}
	if len(after.Genres) != 1 || after.Genres[0] != "悬疑" {
		t.Fatalf("手动类型应整体替换，实际 %+v", after.Genres)
	}
	if after.Year != "2019" || after.Runtime != 90 {
		t.Fatalf("未手动编辑的字段应保留 NFO 合并值，实际 year=%q runtime=%d", after.Year, after.Runtime)
	}

	// RefreshMetadata（NFO 重建）后手动值仍应保留
	lib.RefreshMetadata(videoID)
	refreshed := lib.Metadata(videoID)
	if refreshed == nil || refreshed.Title != "手动片名" {
		t.Fatalf("RefreshMetadata 后手动片名丢失: %+v", refreshed)
	}
}

// TestSaveManualMetadataErrors 验证目录未配置与视频不存在两条错误路径。
func TestSaveManualMetadataErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.mp4"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("写视频失败: %v", err)
	}
	lib := NewLibrary(root, DefaultExts) // 不登记缓存目录
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	videoID := lib.List()[0].ID

	if err := lib.SaveManualMetadata(videoID, &Metadata{Title: "x"}); !errors.Is(err, ErrMetadataDirNotConfigured) {
		t.Fatalf("未配置缓存目录应返回 ErrMetadataDirNotConfigured，实际 %v", err)
	}

	// 登记目录后，对不存在的视频保存应报“视频不存在”
	lib.SetScraperImageDir(t.TempDir())
	if err := lib.SaveManualMetadata("ghost-id", &Metadata{Title: "x"}); err == nil {
		t.Fatal("不存在的视频应返回错误")
	}
}

// TestManualEmpty 验证 manualEmpty 只看 10 个核心字段，图片路径不参与判定。
func TestManualEmpty(t *testing.T) {
	if !(&Metadata{}).manualEmpty() {
		t.Fatal("零值 Metadata 应判定为 manualEmpty")
	}
	withPoster := &Metadata{PosterPath: "/p.jpg", BackdropPath: "/b.jpg", Source: "nfo"}
	if !withPoster.manualEmpty() {
		t.Fatal("仅含图片/来源时核心字段仍为空，应 manualEmpty")
	}
	for _, md := range []*Metadata{
		{Title: "x"}, {Original: "x"}, {Overview: "x"}, {Year: "2024"},
		{Runtime: 1}, {Rating: 1}, {Studio: "x"},
		{Genres: []string{"x"}}, {Directors: []string{"x"}}, {Actors: []string{"x"}},
	} {
		if md.manualEmpty() {
			t.Fatalf("含非空核心字段时不应 manualEmpty: %+v", md)
		}
	}
}
