package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchPageScreenshotInjection 锁死播放画面截图的前端契约：
// 纯 canvas 抓帧实现、不挂持久化门控（store=nil 夹具下仍须渲染全部锚点）。
func TestWatchPageScreenshotInjection(t *testing.T) {
	// newSubtitleUploadServer 是 store=nil 的最小夹具，截图功能与持久化无关，直接复用
	ts, _, vid := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		// 触发入口与提示 toast
		`id="screenshot-btn"`,
		`id="shot-toast"`,
		// 抓帧下载核心链路
		`drawImage`,
		`toBlob`,
		`image/png`,
		`a.download`,
		// 视频名模板注入锚点
		`videoName`,
		// 回归：第十轮偏好按钮仍在，控制区结构未被破坏
		`id="pref-reset-btn"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少截图功能注入内容 %q", want)
		}
	}
}
