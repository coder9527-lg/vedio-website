package server

import (
	"net/http"
	"testing"
)

// TestIndexRendersCardDensityControls 首页渲染卡片密度三档控件、防闪烁脚本与 JS 引用。
// 纯前端功能，服务端只负责输出固定标记，行为由浏览器冒烟覆盖。
func TestIndexRendersCardDensityControls(t *testing.T) {
	srv, _ := newRelatedTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/")
	if code != http.StatusOK {
		t.Fatalf("首页状态码 = %d，期望 200", code)
	}
	containsAll(t, html,
		`data-density-group`,
		`data-density-option="compact"`,
		`data-density-option="normal"`,
		`data-density-option="comfortable"`,
		`/static/js/card-density.js`,
		`vw:cardDensity`,
	)
}
