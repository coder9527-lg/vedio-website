package server

import (
	"net/http"
	"strings"
	"testing"
)

// 每个可直接 GET 的页面都应内联防闪烁脚本、引用 theme.js 并渲染切换按钮。
func TestThemeScaffoldingOnPages(t *testing.T) {
	ts, _, vid := newSubtitleUploadServer(t)

	pages := []string{
		"/",
		"/search?q=test",
		"/stats",
		"/history",
		"/watch/" + vid,
	}
	wantFragments := []string{
		`data-theme`,           // 防闪烁脚本写入的属性约定
		`vw:theme`,             // 本地偏好存储键
		`prefers-color-scheme`, // 跟随系统档的媒体查询
		`/static/js/theme.js`,  // 交互脚本引用
		`data-theme-toggle`,    // 切换按钮锚点
	}
	for _, path := range pages {
		code, body := getHTML(t, ts, path)
		if code != http.StatusOK {
			t.Fatalf("GET %s 应 200，实际 %d", path, code)
		}
		for _, frag := range wantFragments {
			if !strings.Contains(body, frag) {
				t.Errorf("页面 %s 缺少主题片段 %q", path, frag)
			}
		}
	}
}

// 样式表必须包含浅色变量档与新增语义变量，组件规则才能随主题切换。
func TestThemeStylesheetVariables(t *testing.T) {
	ts, _, _ := newSubtitleUploadServer(t)

	code, body := getHTML(t, ts, "/static/css/style.css")
	if code != http.StatusOK {
		t.Fatalf("样式表应 200，实际 %d", code)
	}
	for _, frag := range []string{
		`[data-theme="light"]`,
		`--thumb-bg`,
		`--on-accent`,
		`--surface-raised`,
		`.theme-toggle`,
		`.theme-toggle-float`,
	} {
		if !strings.Contains(body, frag) {
			t.Errorf("样式表缺少 %q", frag)
		}
	}
}

// theme.js 经静态目录对外可访问，且纯前端实现不得引用任何后端接口。
func TestThemeScriptStaticAsset(t *testing.T) {
	ts, _, _ := newSubtitleUploadServer(t)

	code, body := getHTML(t, ts, "/static/js/theme.js")
	if code != http.StatusOK {
		t.Fatalf("theme.js 应 200，实际 %d", code)
	}
	if !strings.Contains(body, `ThemeController`) {
		t.Error("theme.js 应暴露 window.ThemeController")
	}
	if strings.Contains(body, `/api/v1/`) {
		t.Error("主题脚本为纯前端实现，不应引用 /api/v1/ 接口")
	}
}
