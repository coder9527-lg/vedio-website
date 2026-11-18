package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanVideoName(t *testing.T) {
	cases := map[string]string{
		"The.Movie.2019.1080p.BluRay.x264-YIFY": "The Movie 2019",
		"流浪地球.2019.2160p.UHD.BluRay.x265.10bit": "流浪地球 2019",
		"Show.Name.S01E01.WEB-DL.H264-CHD":      "Show Name S01E01",
		"[TAG] Movie Name (2020) [1080p]":       "Movie Name 2020",
		"普通视频":                                  "普通视频",
		"Movie.2021.HDR.2160p.WEB.H265-GROUP":   "Movie 2021",
	}
	for in, want := range cases {
		if got := cleanVideoName(in); got != want {
			t.Errorf("cleanVideoName(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestExtractYear(t *testing.T) {
	if y := extractYear("Movie 2019 1080p"); y != "2019" {
		t.Errorf("年份提取错误: %q", y)
	}
	if y := extractYear("no year here"); y != "" {
		t.Errorf("无年份时期望空串，实际 %q", y)
	}
}

func TestMergeMetadataNFOWins(t *testing.T) {
	local := &Metadata{
		Title:      "本地片名",
		Year:       "2018",
		Rating:     9.1,
		Genres:     []string{"本地类型"},
		PosterPath: "/local/poster.jpg",
		Source:     "nfo",
	}
	tmdb := &Metadata{
		Title:        "TMDB 片名",
		Original:     "Original",
		Year:         "2019",
		Runtime:      100,
		Rating:       7.0,
		Genres:       []string{"TMDB类型"},
		Directors:    []string{"TMDB导演"},
		PosterPath:   "/tmdb/poster.jpg",
		BackdropPath: "/tmdb/backdrop.jpg",
	}
	merged := mergeMetadata(local, tmdb)

	// NFO 已有字段保持不变
	if merged.Title != "本地片名" || merged.Year != "2018" || merged.Rating != 9.1 ||
		merged.PosterPath != "/local/poster.jpg" || len(merged.Genres) != 1 || merged.Genres[0] != "本地类型" {
		t.Errorf("NFO 字段被覆盖: %+v", merged)
	}
	// NFO 缺失字段由 TMDB 补齐
	if merged.Original != "Original" || merged.Runtime != 100 ||
		len(merged.Directors) != 1 || merged.BackdropPath != "/tmdb/backdrop.jpg" {
		t.Errorf("TMDB 补缺失败: %+v", merged)
	}
	// 合并不能改动入参
	if local.Runtime != 0 || len(tmdb.Genres) != 1 && tmdb.Genres[0] != "TMDB类型" {
		t.Error("合并过程修改了入参对象")
	}
}

func TestMergeMetadataNilLocal(t *testing.T) {
	tmdb := &Metadata{Title: "纯 TMDB", Genres: []string{"科幻"}}
	merged := mergeMetadata(nil, tmdb)
	if merged.Title != "纯 TMDB" || len(merged.Genres) != 1 {
		t.Errorf("无本地元信息时应整体采用 TMDB: %+v", merged)
	}
}

func TestScraperDisabledWithoutClient(t *testing.T) {
	if s, err := NewScraper(nil, NewLibrary(t.TempDir(), DefaultExts), t.TempDir()); err != nil || s != nil {
		t.Errorf("client 为 nil 时应返回 nil scraper，实际 s=%v err=%v", s, err)
	}
}

func TestScraperApplyEndToEnd(t *testing.T) {
	client, _, _ := newFakeTMDB(t)
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"films/movie.mp4": "x"})
	lib := NewLibrary(root, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	id := lib.List()[0].ID
	imageDir := filepath.Join(t.TempDir(), "tmdb")

	scraper, err := NewScraper(client, lib, imageDir)
	if err != nil {
		t.Fatal(err)
	}

	// Search：用清洗后的文件名查询
	hits, err := scraper.Search(context.Background(), id)
	if err != nil {
		t.Fatalf("Search 失败: %v", err)
	}
	if len(hits) != 2 || hits[0].ID != 999 {
		t.Fatalf("搜索候选错误: %+v", hits)
	}

	// Apply：拉详情、下载图片、写回元信息
	md, err := scraper.Apply(context.Background(), id, 999)
	if err != nil {
		t.Fatalf("Apply 失败: %v", err)
	}
	if md.Title != "流浪地球" || md.Source != "tmdb" || md.Runtime != 125 {
		t.Errorf("刮削元信息错误: %+v", md)
	}
	if md.PosterPath == "" || filepath.Dir(md.PosterPath) != filepath.Join(imageDir, id) {
		t.Errorf("海报缓存路径错误: %q", md.PosterPath)
	}
	if data, err := os.ReadFile(md.PosterPath); err != nil || string(data) != "image-bytes-p.jpg" {
		t.Errorf("海报内容错误: %q err=%v", data, err)
	}
	if md.BackdropPath == "" {
		t.Error("背景图应已下载")
	}
	// Library 中可读到合并结果
	if got := lib.Metadata(id); got == nil || got.Title != "流浪地球" {
		t.Errorf("Library 元信息回写失败: %+v", got)
	}
}

func TestScraperApplyMergesWithNFO(t *testing.T) {
	client, _, _ := newFakeTMDB(t)
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"films/movie.mp4": "x",
		"films/movie.nfo": "<movie><title>本地优先片名</title><year>2019</year></movie>",
	})
	lib := NewLibrary(root, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	id := lib.List()[0].ID

	scraper, err := NewScraper(client, lib, filepath.Join(t.TempDir(), "tmdb"))
	if err != nil {
		t.Fatal(err)
	}
	md, err := scraper.Apply(context.Background(), id, 999)
	if err != nil {
		t.Fatal(err)
	}
	if md.Title != "本地优先片名" {
		t.Errorf("NFO 片名应优先，实际 %q", md.Title)
	}
	if md.Source != "nfo+tmdb" {
		t.Errorf("混合来源标记错误: %q", md.Source)
	}
	// NFO 没有的简介由 TMDB 补齐
	if md.Overview == "" || md.Runtime != 125 {
		t.Errorf("TMDB 补缺失败: %+v", md)
	}
}

func TestScraperResultSurvivesRescan(t *testing.T) {
	client, _, _ := newFakeTMDB(t)
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"films/movie.mp4": "x"})
	lib := NewLibrary(root, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	id := lib.List()[0].ID
	imageDir := filepath.Join(t.TempDir(), "tmdb")
	scraper, err := NewScraper(client, lib, imageDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scraper.Apply(context.Background(), id, 999); err != nil {
		t.Fatal(err)
	}

	// 重新扫描（模拟重启或手动 rescan），刮削结果应从 metadata.json 恢复
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	got := lib.Metadata(id)
	if got == nil || got.Title != "流浪地球" || got.Source != "tmdb" {
		t.Fatalf("重扫后刮削结果丢失: %+v", got)
	}
	if got.PosterPath == "" || !fileExists(got.PosterPath) {
		t.Errorf("海报路径应仍指向缓存文件: %q", got.PosterPath)
	}

	// 重扫后再叠加本地 NFO：NFO 字段优先，TMDB 补缺
	writeFiles(t, root, map[string]string{
		"films/movie.nfo": "<movie><title>NFO 优先名</title></movie>",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	got = lib.Metadata(id)
	if got.Title != "NFO 优先名" {
		t.Errorf("NFO 片名应覆盖刮削结果，实际 %q", got.Title)
	}
	if got.Overview == "" {
		t.Error("TMDB 简介应作为补缺保留")
	}
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func TestScraperSearchUnknownVideo(t *testing.T) {
	client, _, _ := newFakeTMDB(t)
	scraper, err := NewScraper(client, NewLibrary(t.TempDir(), DefaultExts), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scraper.Search(context.Background(), "no-such-id"); err == nil {
		t.Error("不存在的视频应报错")
	}
}
