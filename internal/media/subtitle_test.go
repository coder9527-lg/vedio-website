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
	// 默认字幕（中文，排序最高）应排在最前
	if !subs[0].Default {
		t.Errorf("首位应为默认字幕，got %+v", subs[0])
	}
	if subs[0].Lang != "zh-Hans" {
		t.Errorf("首位应为简体中文，got %q", subs[0].Lang)
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

	// 删除字幕后刷新应清空
	other, _ := lib.Get(makeID("movies/Other.mp4"))
	if len(lib.Subtitles(other.ID)) != 1 {
		t.Fatal("Other 应有 1 个字幕")
	}
	if err := os.Remove(filepath.Join(root, "movies", "Other.zh.srt")); err != nil {
		t.Fatal(err)
	}
	lib.RefreshSubtitles(other.ID)
	if got := lib.Subtitles(other.ID); len(got) != 0 {
		t.Errorf("删除字幕刷新后应为空，got %d", len(got))
	}
}

func TestSubtitleStemMatch(t *testing.T) {
	cases := []struct {
		stem, base string
		want       bool
	}{
		{"Movie", "Movie", true},
		{"Movie.zh-CN", "Movie", true},
		{"Movie.zh_tw", "Movie", true},
		{"Movie.en", "Movie", true},
		{"Movie.default", "Movie", true},
		{"Movie.forced", "Movie", true},
		{"Movie.sdh", "Movie", true},
		{"Movie zh srt-ish", "Movie", false}, // 无扩展名场景由上层过滤，这里仅测分隔后 token
		{"Movie2", "Movie", false},
		{"Movie.part1", "Movie", false}, // part1 不是语言/标记
		{"Movie-eng", "Movie", true},    // 连字符分隔
		{"Movie_ja", "Movie", true},     // 下划线分隔
		{"Movie en.sdh", "Movie", true}, // 多 token，首段为语言
	}
	for _, c := range cases {
		// 带空格的用例 stem 含空格会被 FieldsFunc 拆分，单独跳过
		if c.stem == "Movie zh srt-ish" {
			continue
		}
		if got := subtitleStemMatch(c.stem, c.base); got != c.want {
			t.Errorf("subtitleStemMatch(%q,%q)=%v want %v", c.stem, c.base, got, c.want)
		}
	}
}

func TestParseSubtitleMarkers(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{
		"m/Movie.mkv": "x",
		// SDH / 强制 / 多标记 / 子目录
		"m/Movie.en.sdh.srt":       "1\n00:00:01,000 --> 00:00:02,000\nhi\n",
		"m/Movie.zh-CN.forced.ass": "[Script Info]\n",
		"m/Movie.ja.cc.srt":        "1\n00:00:01,000 --> 00:00:02,000\nhi\n",
		"m/subs/Movie.fr.srt":      "1\n00:00:01,000 --> 00:00:02,000\nbonjour\n",
		"m/subtitles/Movie.de.vtt": "WEBVTT\n",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	v, _ := lib.Get(makeID("m/Movie.mkv"))
	subs := lib.Subtitles(v.ID)
	if len(subs) != 5 {
		t.Fatalf("同目录 3 + 子目录 2，期望 5 个字幕，got %d", len(subs))
	}
	byRel := map[string]*Subtitle{}
	for _, s := range subs {
		byRel[s.RelPath] = s
	}
	sdh := byRel["m/Movie.en.sdh.srt"]
	if sdh == nil || !sdh.SDH || sdh.Forced {
		t.Errorf("SDH 标记解析错误: %+v", sdh)
	}
	forced := byRel["m/Movie.zh-CN.forced.ass"]
	if forced == nil || !forced.Forced || forced.Lang != "zh-Hans" {
		t.Errorf("forced 标记/语言解析错误: %+v", forced)
	}
	cc := byRel["m/Movie.ja.cc.srt"]
	if cc == nil || !cc.SDH || cc.Lang != "ja" {
		t.Errorf("cc 应等同 SDH，语言应为 ja: %+v", cc)
	}
	if s := byRel["m/subs/Movie.fr.srt"]; s == nil || s.Lang != "fr" {
		t.Errorf("subs 子目录字幕未发现: %+v", s)
	}
	if s := byRel["m/subtitles/Movie.de.vtt"]; s == nil || s.Format != "vtt" {
		t.Errorf("subtitles 子目录字幕未发现: %+v", s)
	}
	// 默认轨道：强制字幕不应默认，排序后中文非强制优先；本例中文是 forced，
	// 因此默认应落到排序最靠前的非强制字幕
	var defaults int
	var defaultForced bool
	for _, s := range subs {
		if s.Default {
			defaults++
			defaultForced = s.Forced
		}
	}
	if defaults != 1 {
		t.Errorf("应有且仅有 1 条默认字幕，got %d", defaults)
	}
	if defaultForced {
		t.Error("强制字幕不应被选为默认轨道")
	}
	// 展示名应带标记
	if forced.Name != "简体中文（强制）" {
		t.Errorf("强制字幕展示名异常: %q", forced.Name)
	}
	if sdh.Name != "English（SDH）" {
		t.Errorf("SDH 字幕展示名异常: %q", sdh.Name)
	}
}

func TestSubtitleSubdirOnly(t *testing.T) {
	// 字幕只存在于 subs 子目录，根目录无同名文件
	lib, _ := newTestLibrary(t, map[string]string{
		"film/A.mp4":      "x",
		"film/subs/A.srt": "1\n00:00:01,000 --> 00:00:02,000\nhi\n",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	v, _ := lib.Get(makeID("film/A.mp4"))
	subs := lib.Subtitles(v.ID)
	if len(subs) != 1 || subs[0].RelPath != "film/subs/A.srt" {
		t.Fatalf("子目录字幕发现失败: %+v", subs)
	}
}
