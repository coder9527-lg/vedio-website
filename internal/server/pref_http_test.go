package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchPagePlayerPrefsInjection 锁死播放器偏好记忆的前端契约：
// 纯 localStorage 实现、不挂持久化门控（store=nil 夹具下仍须渲染全部锚点）。
func TestWatchPagePlayerPrefsInjection(t *testing.T) {
	// newSubtitleUploadServer 本身就是 store=nil、无字幕的最小夹具，直接复用
	ts, _, vid := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		// 偏好核心模块：存储键、全局对象、四项偏好字段
		`vw:prefs`,
		`window.PlayerPrefs`,
		`getSubLabel`,
		// 重置入口与提示 toast（控制区按钮，不依赖 StoreOn）
		`id="pref-reset-btn"`,
		`id="pref-toast"`,
		`prefs-reset`,
		// 现有菜单锚点不得被本轮改动破坏
		`id="speed-dropdown"`,
		`id="cue-dropdown"`,
		`id="sub-menu-wrap"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少播放器偏好注入内容 %q", want)
		}
	}
}
