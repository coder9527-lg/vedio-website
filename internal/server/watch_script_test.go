package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchPageVideoIDNotDoubleQuoted 回归：模板里曾用 {{printf "%q" .Video.ID}}，
// 在 JS 上下文被二次转义成带引号的 ID，导致进度/书签/标签等接口全部打到错误地址。
// 现在必须直接输出为合法 JS 字符串字面量，且不含多余转义引号。
func TestWatchPageVideoIDNotDoubleQuoted(t *testing.T) {
	_, ts, vid := newPersistTestServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}

	want := `const videoId = "` + vid + `";`
	if !strings.Contains(html, want) {
		t.Errorf("播放页缺少正确的 videoId 字面量 %q", want)
	}
	bad := `\"` + vid + `\"`
	if strings.Contains(html, bad) {
		t.Errorf("videoId 仍被二次转义为 %q", bad)
	}
}

// TestWatchPagePlayerPrefsDefinedEarly 回归：字幕模块在解析期即调用 PlayerPrefs，
// 定义必须先于首次调用，否则控制台抛 TypeError 且字幕偏好不生效。
func TestWatchPagePlayerPrefsDefinedEarly(t *testing.T) {
	_, ts, vid := newPersistTestServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}

	def := strings.Index(html, "window.PlayerPrefs = ")
	if def < 0 {
		t.Fatal("播放页未定义 PlayerPrefs")
	}
	// 最小夹具既无转码也无字幕，字幕模块可能整体不渲染；出现调用时再校验先后顺序
	use := strings.Index(html, "window.PlayerPrefs.getSubLabel()")
	if use >= 0 && def > use {
		t.Error("PlayerPrefs 必须在首个 getSubLabel 调用之前定义")
	}
}
