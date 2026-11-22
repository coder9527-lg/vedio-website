package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchPagePictureInPictureInjection 锁死画中画的前端契约：
// 按钮默认 hidden、能力检测后才显示，纯浏览器 API、与持久化无关（store=nil 夹具）。
func TestWatchPagePictureInPictureInjection(t *testing.T) {
	// newSubtitleUploadServer 是 store=nil 的最小夹具，画中画与持久化无关，直接复用
	ts, _, vid := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		// 入口：包裹层默认 hidden，按钮带 aria-pressed 初始态
		`id="pip-menu-wrap" hidden`,
		`id="pip-btn"`,
		`aria-pressed="false"`,
		// 能力检测与进入/退出 API
		`pictureInPictureEnabled`,
		`requestPictureInPicture`,
		`exitPictureInPicture`,
		// 系统事件驱动按钮状态同步
		`enterpictureinpicture`,
		`leavepictureinpicture`,
		// 回归：上一轮截图按钮仍在，控制区结构未被破坏
		`id="screenshot-btn"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少画中画注入内容 %q", want)
		}
	}
}
