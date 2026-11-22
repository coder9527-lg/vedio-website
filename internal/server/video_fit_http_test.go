package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchPageVideoFitInjection 锁死第十八轮播放器画面缩放切换的前端契约：
// 纯浏览器行为、与持久化无关（store=nil 夹具），偏好复用 PlayerPrefs 的 localStorage。
func TestWatchPageVideoFitInjection(t *testing.T) {
	// newSubtitleUploadServer 是 store=nil 的最小夹具：画面菜单不受 StoreOn 门控
	ts, _, vid := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		// 菜单 DOM 入口与三个固定档位
		`id="fit-menu-btn"`,
		`id="fit-dropdown"`,
		`data-fit="contain"`,
		`data-fit="cover"`,
		`data-fit="fill"`,
		`适应`,
		`裁切铺满`,
		`拉伸填充`,
		// PlayerPrefs 白名单与字段级校验
		`const FITS = ['contain', 'cover', 'fill']`,
		`prefs.videoFit = obj.videoFit`,
		// IIFE 契约锚点
		`const FIT_DEFAULT = 'contain'`,
		`fit-contain`,
		`player.style.objectFit = mode`,
		`PlayerPrefs.save({ videoFit: mode })`,
		`'prefs-reset'`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少画面缩放注入内容 %q", want)
		}
	}

	// 纯前端契约回归：画面缩放脚本段不得引用任何持久化 API
	seg := extractVideoFitSegment(html)
	if seg == "" {
		t.Fatal("未找到画面缩放脚本段")
	}
	if strings.Contains(seg, "/api/v1/") {
		t.Error("画面缩放脚本段不应引用任何 /api/v1/ 持久化接口")
	}
}

// extractVideoFitSegment 截取画面缩放 IIFE 脚本段，供局部契约检查。
// 用脚本内稳定标识符定位起点，避开中文标点被 html/template 转义的问题。
func extractVideoFitSegment(html string) string {
	start := strings.Index(html, "const FIT_DEFAULT = 'contain'")
	if start < 0 {
		return ""
	}
	end := strings.Index(html[start:], "</script>")
	if end < 0 {
		return html[start:]
	}
	return html[start : start+end]
}
