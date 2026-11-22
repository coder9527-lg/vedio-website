package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchPageTimestampLinkInjection 锁死第十五轮时间点分享链接的前端契约：
// 纯浏览器行为、与持久化无关（store=nil 夹具），服务端只验证无条件渲染的注入锚点。
func TestWatchPageTimestampLinkInjection(t *testing.T) {
	// newSubtitleUploadServer 是 store=nil 的最小夹具：时间点按钮与脚本不受 StoreOn 门控
	ts, _, vid := newSubtitleUploadServer(t)

	// 无参数播放页：按钮与脚本无条件渲染
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		// 控制栏入口
		`id="timestamp-link-btn"`,
		`🔗 时间点`,
		// URL 参数解析与跳转契约
		`URLSearchParams`,
		`.get('t')`,
		`window.__urlSeek`,
		`window.__urlSeekDone`,
		`loadedmetadata`,
		// 复制链接契约（clipboard 优先 + execCommand 兜底）
		`?t=${sec}`,
		`navigator.clipboard`,
		`execCommand('copy')`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少时间点链接注入内容 %q", want)
		}
	}

	// 带 t 参数请求：handler 不解析该参数，页面正常渲染且锚点仍在
	code2, html2 := getHTML(t, ts, "/watch/"+vid+"?t=120")
	if code2 != http.StatusOK {
		t.Fatalf("带 t 参数播放页状态码 = %d", code2)
	}
	if !strings.Contains(html2, `id="timestamp-link-btn"`) {
		t.Error("带 t 参数播放页缺少时间点按钮")
	}
	if !strings.Contains(html2, `window.__urlSeek`) {
		t.Error("带 t 参数播放页缺少时间点跳转脚本")
	}

	// 非法参数不应导致页面异常：同样 200 渲染
	code3, _ := getHTML(t, ts, "/watch/"+vid+"?t=abc")
	if code3 != http.StatusOK {
		t.Fatalf("带非法 t 参数播放页状态码 = %d", code3)
	}
}

// TestWatchPageTimestampLinkResumeGuard 验证 t 参数对自动续播的优先级守卫。
// resume() 位于 {{if .StoreOn}} 段内，必须用带 SQLite 的夹具才能渲染到该锚点。
func TestWatchPageTimestampLinkResumeGuard(t *testing.T) {
	_, ts, vid := newPersistTestServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid+"?t=120")
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	// 守卫存在即锁死契约：带合法 t 时 resume 提前返回，不发 progress 请求、不续播
	if !strings.Contains(html, `if (window.__urlSeek != null) return;`) {
		t.Error("播放页缺少续播优先级守卫 window.__urlSeek != null")
	}
}
