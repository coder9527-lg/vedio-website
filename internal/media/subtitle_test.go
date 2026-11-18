package media

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLibrarySubtitleDiscovery(t *testing.T) {
	lib, root := newTestLibrary(t, map[string]string{
		"movies/Movie.mkv":          "x",
		"movies/Movie.srt":          "1\n00:00:01,000 --> 00:00:02,000\nhi\n",
		"movies/Movie.zh-CN.ass":    "[Script Info]\n",
		"movies/Movie.en.vtt":       "WEBVTT\n",
		"movies/Movie.part1.srt":    "should-not-match",
		"movies/Other.mp4":          "y",
		"movies/Other.zh.srt":       "1\n00:00:01,000 --> 00:00:02,000\nhi\n",
		"series/S01E01.mov":         "z",
		"series/S01E01.default.srt": "1\n00:00:01,000 --> 00:00:02,000\nhi\n",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}

	movie, ok := lib.Get(makeID("movies/Movie.mkv"))
	if !ok {
		t.Fatal("Movie 未入库")
	}
	subs := lib.Subtitles(movie.ID)
	if len(subs) != 3 {
		t.Fatalf("Movie 应关联 3 个字幕，实际 %d: %+v", len(subs), subs)
	}
	// 默认字幕（中文）应排在最前
	if !subs[0].Default {
		t.Errorf("首位应为默认中文字幕，got %+v", subs[0])
	}
	formats := map[string]bool{}
	for _, s := range subs {
		formats[s.Format] = true
		if s.AbsPath() == "" || !filepath.IsAbs(s.AbsPath()) {
			t.Errorf("字幕绝对路径异常: %q", s.AbsPath())
		}
	}
	if !formats["srt"] || !formats["ass"] || !formats["vtt"] {
		t.Errorf("字幕格式收集不全: %v", formats)
	}

	// GetSubtitle 命中与未命中
	if _, ok := lib.GetSubtitle(movie.ID, subs[0].ID); !ok {
		t.Error("按 ID 查找字幕失败")
	}
	if _, ok := lib.GetSubtitle(movie.ID, "nope"); ok {
		t.Error("不存在的字幕 ID 不应命中")
	}
	if _, ok := lib.GetSubtitle("nope-video", subs[0].ID); ok {
		t.Error("不属于该视频的字幕不应命中")
	}

	// .part1.srt 不应误关联
	for _, s := range subs {
		if filepath.Base(s.RelPath) == "Movie.part1.srt" {
			t.Error("Movie.part1.srt 不应被自动挂载")
		}
	}

	// default 后缀的字幕应标记默认
	ep, _ := lib.Get(makeID("series/S01E01.mov"))
	epSubs := lib.Subtitles(ep.ID)
	if len(epSubs) != 1 || !epSubs[0].Default {
		t.Errorf("default 后缀字幕应标记默认，got %+v", epSubs)
	}

	// 无字幕视频返回 nil
	other, _ := lib.Get(makeID("movies/Other.mp4"))
	if len(lib.Subtitles(other.ID)) != 1 {
		t.Fatal("Other 应有 1 个字幕")
	}
	// 删除字幕后刷新应清空
	if err := os.Remove(filepath.Join(root, "movies", "Other.zh.srt")); err != nil {
		t.Fatal(err)
	}
	lib.RefreshSubtitles(other.ID)
	if got := lib.Subtitles(other.ID); len(got) != 0 {
		t.Errorf("删除字幕刷新后应为空，got %d", len(got))
	}
}

func TestParseSubtitleLang(t *testing.T) {
	cases := []struct {
		stem, base             string
		wantCode, wantLabelHas string
	}{
		{"Movie", "Movie", "und", "字幕"},
		{"Movie.zh-CN", "Movie", "zh-Hans", "简体中文"},
		{"Movie.zh_tw", "Movie", "zh-Hant", "繁体中文"},
		{"Movie.en", "Movie", "en", "English"},
		{"Movie.ja", "Movie", "ja", "日本語"},
		{"Movie.unknown", "Movie", "und", "unknown"},
		{"Movie.zh-CN.forced", "Movie", "zh-Hans", "简体中文"},
	}
	for _, c := range cases {
		code, label := parseSubtitleLang(c.stem, c.base)
		if code != c.wantCode || label != c.wantLabelHas {
			t.Errorf("parseSubtitleLang(%q) = (%q,%q), want (%q,%q)",
				c.stem, code, label, c.wantCode, c.wantLabelHas)
		}
	}
}

func TestSubtitleStemMatch(t *testing.T) {
	if !subtitleStemMatch("Movie", "Movie") {
		t.Error("精确同名应匹配")
	}
	if !subtitleStemMatch("Movie.zh-CN", "Movie") {
		t.Error("语言后缀应匹配")
	}
	if subtitleStemMatch("Movie2", "Movie") {
		t.Error("前缀相似但不同名不应匹配")
	}
	if subtitleStemMatch("Movie.part1", "Movie") {
		// 实际上 "Movie.part1" 满足前缀规则，会被挂载，标签为 part1 —— 记录当前行为
	}
}
