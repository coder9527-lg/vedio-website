package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchPageSleepTimerInjection 锁死第十六轮睡眠定时器的前端契约：
// 纯浏览器行为、与持久化无关（store=nil 夹具），服务端只验证注入锚点。
func TestWatchPageSleepTimerInjection(t *testing.T) {
	// newSubtitleUploadServer 是 store=nil 的最小夹具：睡眠定时不受 StoreOn 门控
	ts, _, vid := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		// 菜单 DOM 入口
		`id="sleep-menu-btn"`,
		`id="sleep-dropdown"`,
		`睡眠定时`,
		// 固定三档与关闭项
		`15 分钟`,
		`30 分钟`,
		`60 分钟`,
		`label: '关闭'`,
		// 墙钟倒计时契约
		`let deadline = null`,
		`Date.now() + minutes * 60000`,
		`setInterval(tick, 1000)`,
		`clearInterval`,
		// 到点仅暂停 + 提示
		`if (!player.paused) player.pause()`,
		`睡眠定时到点，已暂停播放`,
		// 剩余时间按钮文案前缀
		`'睡眠 ' + formatRemain`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少睡眠定时注入内容 %q", want)
		}
	}

	// 纯前端契约回归：睡眠脚本段不得引用任何持久化 API
	seg := extractSleepSegment(html)
	if seg == "" {
		t.Fatal("未找到睡眠定时器脚本段")
	}
	if strings.Contains(seg, "/api/v1/") {
		t.Error("睡眠定时器脚本段不应引用任何 /api/v1/ 持久化接口")
	}
}

// extractSleepSegment 截取睡眠定时器 IIFE 脚本段，供局部契约检查。
// 用脚本内稳定标识符定位起点，避开中文标点被 html/template 转义的问题。
func extractSleepSegment(html string) string {
	start := strings.Index(html, "const BASE_LABEL = '睡眠定时 '")
	if start < 0 {
		return ""
	}
	end := strings.Index(html[start:], "</script>")
	if end < 0 {
		return html[start:]
	}
	return html[start : start+end]
}
