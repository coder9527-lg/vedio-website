package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchPageGestureInjection 锁死第二十轮阶段二触控手势的前端契约：
// 纯浏览器行为、与持久化无关（store=nil 夹具），服务端只验证注入锚点。
func TestWatchPageGestureInjection(t *testing.T) {
	// store=nil 最小夹具：手势不受 StoreOn 门控，无条件渲染
	ts, _, vid := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		// 指示层 DOM
		`id="gesture-indicator"`,
		`class="gesture-indicator"`,
		// 触屏能力守卫：桌面端不绑定
		`'ontouchstart' in window`,
		`navigator.maxTouchPoints < 1`,
		// 三类触摸事件与单指守卫
		`'touchstart'`,
		`'touchmove'`,
		`'touchend'`,
		`e.touches.length !== 1`,
		// 双击三分屏与 10 秒快进退
		`GESTURE_DOUBLE_MS`,
		`GESTURE_SEEK_SECONDS`,
		`ratio < 1 / 3`,
		`ratio > 2 / 3`,
		// 水平拖动 seek
		`direction === 'h'`,
		`player.currentTime = target`,
		// 左半屏亮度（CSS filter）与右半屏音量
		`'v-bright'`,
		`'v-volume'`,
		`brightness(`,
		`player.volume = v`,
		// 方向锁阈值与拖动期间阻止默认行为
		`GESTURE_TAP_SLOP`,
		`e.preventDefault()`,
		`{ passive: false }`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少触控手势注入内容 %q", want)
		}
	}

	// 纯前端契约回归：手势脚本段不得引用任何持久化 API
	seg := extractGestureSegment(html)
	if seg == "" {
		t.Fatal("未找到触控手势脚本段")
	}
	if strings.Contains(seg, "/api/v1/") {
		t.Error("触控手势脚本段不应引用任何 /api/v1/ 持久化接口")
	}
	if strings.Contains(seg, "localStorage") {
		t.Error("触控手势为会话级行为，不应写入 localStorage")
	}
}

// extractGestureSegment 截取触控手势 IIFE 脚本段，供局部契约检查。
// 用脚本内稳定标识符定位起点，避开中文标点被 html/template 转义的问题。
func extractGestureSegment(html string) string {
	start := strings.Index(html, "const GESTURE_TAP_SLOP = 12;")
	if start < 0 {
		return ""
	}
	end := strings.Index(html[start:], "</script>")
	if end < 0 {
		return html[start:]
	}
	return html[start : start+end]
}
