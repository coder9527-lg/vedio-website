package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchPageMediaSessionInjection 锁死第十九轮 Media Session 的前端契约：
// 纯浏览器行为、与持久化无关（store=nil 夹具），服务端只验证注入锚点。
func TestWatchPageMediaSessionInjection(t *testing.T) {
	// newSubtitleUploadServer 是 store=nil 的最小夹具：Media Session 不受 StoreOn 门控
	ts, _, vid := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		// 能力守卫
		`'mediaSession' in navigator`,
		// 元数据契约
		`const MS_ALBUM = '个人视频库'`,
		`new MediaMetadata(`,
		`artist: msArtist`,
		`artwork: [{ src: msArtwork`,
		// 模板门控：刮削优先 + 文件名/缩略图兜底
		`const msTitle =`,
		`const msArtwork =`,
		`/thumb/` + vid,
		// 播放暂停常驻与双向状态同步
		`setActionHandler('play'`,
		`setActionHandler('pause'`,
		`session.playbackState = 'playing'`,
		`session.playbackState = 'paused'`,
		// 连播门控：无 AutoNextURL 时两个动作置 null
		`const msNextURL =`,
		`setActionHandler('nexttrack', null)`,
		`setActionHandler('previoustrack', null)`,
		`history.back()`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少 Media Session 注入内容 %q", want)
		}
	}

	// 纯前端契约回归：脚本段不得引用任何持久化 API 或本地存储
	seg := extractMediaSessionSegment(html)
	if seg == "" {
		t.Fatal("未找到 Media Session 脚本段")
	}
	if strings.Contains(seg, "/api/v1/") {
		t.Error("Media Session 脚本段不应引用任何 /api/v1/ 持久化接口")
	}
	if strings.Contains(seg, "localStorage") {
		t.Error("Media Session 脚本段不应写 localStorage（会话级状态）")
	}
}

// TestWatchPageMediaSessionAutoNextGate 验证连播上下文的模板门控确实存在。
// 现有夹具仅含 1 个视频无法制造 AutoNextURL，退化为断言 {{if .AutoNextURL}} 门控
// 与 nexttrack 跳转分支被渲染（运行期由 msNextURL 空串决定走 null 分支）。
func TestWatchPageMediaSessionAutoNextGate(t *testing.T) {
	_, ts, vid := newPersistTestServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		`if (msNextURL) {`,
		`location.href = msNextURL`,
		`setActionHandler('nexttrack'`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少连播门控内容 %q", want)
		}
	}
}

// extractMediaSessionSegment 截取 Media Session IIFE 脚本段，供局部契约检查。
// 用脚本内稳定标识符定位起点，避开中文标点被 html/template 转义的问题。
func extractMediaSessionSegment(html string) string {
	start := strings.Index(html, "const MS_ALBUM = '个人视频库'")
	if start < 0 {
		return ""
	}
	end := strings.Index(html[start:], "</script>")
	if end < 0 {
		return html[start:]
	}
	return html[start : start+end]
}
