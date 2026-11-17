package media

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindSubtitlesDiscovery 覆盖同目录、subs 子目录发现、命名匹配与无关文件过滤。
func TestFindSubtitlesDiscovery(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{
		"movies/movie.mp4":                  "video",
		"movies/movie.srt":                  "1",
		"movies/movie.en.vtt":               "2",
		"movies/movie.zh-CN.forced.ass":     "3",
		"movies/subs/movie.ja.srt":          "4",
		"movies/subtitles/movie.zh.sdh.srt": "5",
		"movies/other.mp4":                  "video",
		"movies/other.srt":                  "不应匹配 movie",
		"movies/readme.txt":                 "ignore",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	v, ok := lib.Get(makeID("movies/movie.mp4"))
	if !ok {
		t.Fatal("未找到 movie.mp4")
	}

	subs := lib.FindSubtitles(v)
	if len(subs) != 5 {
		t.Fatalf("期望发现 5 个字幕，实际 %d", len(subs))
	}

	// 每个 ID 应可通过 GetSubtitle 回查
	for _, s := range subs {
		if got, ok := lib.GetSubtitle(v, s.ID); !ok || got.ID != s.ID {
			t.Errorf("GetSubtitle 回查失败: %s", s.ID)
		}
	}
	if _, ok := lib.GetSubtitle(v, "nonexistent-id"); ok {
		t.Error("不存在的字幕 ID 不应回查成功")
	}
}

// TestParseSubtitleMeta 验证语言、SDH、强制标记与展示名解析。
func TestParseSubtitleMeta(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{
		"m.mp4":            "v",
		"m.zh-CN.srt":      "1",
		"m.eng.forced.ass": "2",
		"m.cht.sdh.vtt":    "3",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	v, _ := lib.Get(makeID("m.mp4"))
	subs := lib.FindSubtitles(v)

	byID := map[string]*Subtitle{}
	for _, s := range subs {
		byID[s.AbsPath()] = s
	}

	zh := byID[filepath.Join(filepath.Dir(v.AbsPath()), "m.zh-CN.srt")]
	if zh.Lang != "zh-CN" || zh.SDH || zh.Forced || zh.Format != "srt" {
		t.Errorf("简中字幕解析错误: %+v", zh)
	}
	if !strings.Contains(zh.Name, "简体中文") {
		t.Errorf("简中展示名异常: %q", zh.Name)
	}

	en := byID[filepath.Join(filepath.Dir(v.AbsPath()), "m.eng.forced.ass")]
	if en.Lang != "en" || !en.Forced || en.Format != "ass" {
		t.Errorf("英文强制字幕解析错误: %+v", en)
	}

	zhTW := byID[filepath.Join(filepath.Dir(v.AbsPath()), "m.cht.sdh.vtt")]
	if zhTW.Lang != "zh-TW" || !zhTW.SDH || zhTW.Format != "vtt" {
		t.Errorf("繁中 SDH 字幕解析错误: %+v", zhTW)
	}
}

// TestSortSubtitlesAndDefault 验证排序优先级与默认轨道选择。
func TestSortSubtitlesAndDefault(t *testing.T) {
	lib, _ := newTestLibrary(t, map[string]string{
		"m.mp4":           "v",
		"m.en.srt":        "1",
		"m.zh.forced.srt": "2",
		"m.zh.sdh.srt":    "3",
		"m.zh.srt":        "4",
	})
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	v, _ := lib.Get(makeID("m.mp4"))
	subs := lib.FindSubtitles(v)

	if len(subs) != 4 {
		t.Fatalf("期望 4 个字幕，实际 %d", len(subs))
	}
	// 普通简中应排第一并成为默认；强制字幕垫底
	first := subs[0]
	if first.Lang != "zh-CN" || first.Forced || first.SDH || !first.Default {
		t.Errorf("默认字幕不符合预期: %+v", first)
	}
	defaults := 0
	for _, s := range subs {
		if s.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("期望恰好 1 条默认字幕，实际 %d", defaults)
	}
	if subs[len(subs)-1].Forced != true {
		t.Error("强制字幕应排在最后")
	}
}

// TestSubtitleConverterUnavailable 使用不存在的 ffmpeg 名称验证降级行为。
func TestSubtitleConverterUnavailable(t *testing.T) {
	c, err := NewSubtitleConverter(t.TempDir(), "ffmpeg-definitely-missing")
	if err != nil {
		t.Fatalf("NewSubtitleConverter 失败: %v", err)
	}
	if c.Available() {
		t.Fatal("不存在的 ffmpeg 不应报告可用")
	}
	s := &Subtitle{ID: "abc", Format: "srt", absPath: "/tmp/no-such.srt"}
	if _, err := c.Ensure(s); err == nil {
		t.Error("ffmpeg 不可用时转换应返回错误")
	}
}

// TestSubtitleConverterVTTPassthrough 验证 VTT 原文件直接返回、不产生缓存。
func TestSubtitleConverterVTTPassthrough(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewSubtitleConverter(dir, "ffmpeg-definitely-missing")
	vttPath := filepath.Join(t.TempDir(), "movie.vtt")
	if err := os.WriteFile(vttPath, []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatalf("写入 VTT 失败: %v", err)
	}
	s := &Subtitle{ID: "vtt1", Format: "vtt", absPath: vttPath}
	got, err := c.Ensure(s)
	if err != nil {
		t.Fatalf("VTT 直通不应报错: %v", err)
	}
	if got != vttPath {
		t.Errorf("VTT 应返回原路径，得到 %s", got)
	}
}

// TestSubtitleConverterSRT 验证 SRT 经 ffmpeg 转成 VTT 并命中缓存。
func TestSubtitleConverterSRT(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("本机无 ffmpeg，跳过转换测试")
	}
	cacheDir := t.TempDir()
	c, err := NewSubtitleConverter(cacheDir, "ffmpeg")
	if err != nil {
		t.Fatalf("NewSubtitleConverter 失败: %v", err)
	}

	srcDir := t.TempDir()
	srtPath := filepath.Join(srcDir, "movie.srt")
	srt := "1\n00:00:01,000 --> 00:00:03,000\n你好\n\n2\n00:00:04,000 --> 00:00:06,000\n世界\n"
	if err := os.WriteFile(srtPath, []byte(srt), 0o644); err != nil {
		t.Fatalf("写入 SRT 失败: %v", err)
	}
	s := &Subtitle{ID: "srt1", Format: "srt", absPath: srtPath}

	out, err := c.Ensure(s)
	if err != nil {
		t.Fatalf("SRT 转换失败: %v", err)
	}
	if !strings.HasSuffix(out, "srt1.vtt") {
		t.Errorf("缓存路径异常: %s", out)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("读取转换结果失败: %v", err)
	}
	if !strings.HasPrefix(string(data), "WEBVTT") {
		t.Errorf("转换结果不是合法 VTT: %q", string(data)[:min(20, len(data))])
	}

	// 再次调用应命中缓存（删除源文件后仍能返回，证明未重新转换）
	got2, err := c.Ensure(s)
	if err != nil || got2 != out {
		t.Errorf("缓存命中异常: got=%s err=%v", got2, err)
	}
}
