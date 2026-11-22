package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchPageShortcutInjection 锁死第十四轮快捷键增强与说明面板的前端契约：
// 纯浏览器按键行为、与持久化无关（store=nil 夹具），服务端只验证注入锚点。
func TestWatchPageShortcutInjection(t *testing.T) {
	// newSubtitleUploadServer 是 store=nil 的最小夹具，快捷键与持久化无关，直接复用
	ts, _, vid := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		// 入口与面板 DOM
		`id="kbd-help-btn"`,
		`id="kbd-modal"`,
		`快捷键 ?`,
		`键盘快捷键`,
		// J/L 长跳转（大小写两分支）
		`case 'j'`,
		`case 'J'`,
		`case 'l'`,
		`case 'L'`,
		// 逐帧与百分比跳转契约
		`case ','`,
		`case '.'`,
		`1 / 30`,
		`/ 10`,
		// 面板开关按键
		`case '?'`,
		`case 'Escape'`,
		// 面板内容与回归锚点
		`<kbd>`,
		`<kbd>空格</kbd>`,
		`id="pip-btn"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少快捷键注入内容 %q", want)
		}
	}
}
