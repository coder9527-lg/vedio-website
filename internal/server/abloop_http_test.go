package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchPageABLoopInjection 锁死第十七轮 AB 片段循环的前端契约：
// 纯浏览器行为、与持久化无关（store=nil 夹具），服务端只验证注入锚点。
func TestWatchPageABLoopInjection(t *testing.T) {
	// newSubtitleUploadServer 是 store=nil 的最小夹具：AB 循环不受 StoreOn 门控
	ts, _, vid := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		// 菜单 DOM 入口与三个固定菜单项
		`id="abloop-menu-btn"`,
		`id="abloop-dropdown"`,
		`id="abloop-set-a"`,
		`id="abloop-set-b"`,
		`id="abloop-clear"`,
		`AB 循环`,
		`设 A 点`,
		`设 B 点`,
		`清除循环`,
		// 闭包状态与就绪守卫
		`const AB_BASE_LABEL`,
		`let pointA = null`,
		`let pointB = null`,
		`player.readyState === 0`,
		// 标记校验规则
		`请先标记 A 点`,
		`B 点必须晚于 A 点`,
		// 循环驱动契约：timeupdate 到 B 跳回 A
		`'timeupdate'`,
		`player.currentTime = pointA`,
		`AB 循环：跳回 A 点`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少 AB 循环注入内容 %q", want)
		}
	}

	// 纯前端契约回归：AB 循环脚本段不得引用任何持久化 API
	seg := extractABLoopSegment(html)
	if seg == "" {
		t.Fatal("未找到 AB 循环脚本段")
	}
	if strings.Contains(seg, "/api/v1/") {
		t.Error("AB 循环脚本段不应引用任何 /api/v1/ 持久化接口")
	}
	if strings.Contains(seg, "localStorage") {
		t.Error("AB 循环脚本段不应写 localStorage（会话级状态）")
	}
}

// extractABLoopSegment 截取 AB 循环 IIFE 脚本段，供局部契约检查。
// 用脚本内稳定标识符定位起点，避开中文标点被 html/template 转义的问题。
func extractABLoopSegment(html string) string {
	start := strings.Index(html, "const AB_BASE_LABEL")
	if start < 0 {
		return ""
	}
	end := strings.Index(html[start:], "</script>")
	if end < 0 {
		return html[start:]
	}
	return html[start : start+end]
}
