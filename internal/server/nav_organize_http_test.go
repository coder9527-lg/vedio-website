package server

import (
	"net/http"
	"strings"
	"testing"
)

// 本文件锁定前端收纳优化的渲染契约：
// 首页把统计/历史/分享收进「更多」导航下拉；播放页把低频直按按钮收进控制栏「更多」下拉。
// 仅校验元素是否出现在 HTML 中，交互行为属浏览器范畴，不在此覆盖。

// containsAll 断言 html 包含全部子串，缺失即报错。
func containsAll(t *testing.T, html string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(html, want) {
			t.Errorf("页面缺少期望内容 %q", want)
		}
	}
}

// TestIndexMoreMenu 持久化开启时，首页渲染「更多」导航下拉并收纳三个独立页面入口，
// 同时四个常用视图入口仍在视图行中。
func TestIndexMoreMenu(t *testing.T) {
	_, ts, _ := newPersistTestServer(t)
	code, html := getHTML(t, ts, "/")
	if code != http.StatusOK {
		t.Fatalf("首页状态码 = %d", code)
	}
	containsAll(t, html,
		`id="nav-more-wrap"`,
		`id="nav-more-btn"`,
		`id="nav-dropdown"`,
		`href="/library/storage"`,
		`href="/browse"`,
		`href="/stats"`,
		`href="/review"`,
		`href="/history"`,
		`href="/shares"`,
		// 常用视图入口保留在同级 tab（URL 由 indexURL 模板函数生成，按文案断言）
		`>继续观看<`,
		`>⭐ 收藏<`,
		`>📂 播放列表<`,
	)
}

// TestIndexMoreMenuStoreOff 未启用持久化时「更多」下拉仍常驻（片库存储分析不依赖 SQLite），
// 但观影统计/观看历史/分享管理三个 StoreOn 专属入口不出现，视图切换行也不渲染。
func TestIndexMoreMenuStoreOff(t *testing.T) {
	ts, _, _ := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/")
	if code != http.StatusOK {
		t.Fatalf("首页状态码 = %d", code)
	}
	containsAll(t, html,
		`id="nav-more-wrap"`,
		`id="nav-dropdown"`,
		`href="/library/storage"`,
		`href="/browse"`,
	)
	for _, gone := range []string{`href="/stats"`, `href="/review"`, `href="/history"`, `href="/shares"`} {
		if strings.Contains(html, gone) {
			t.Errorf("未启用持久化时不应渲染 %s", gone)
		}
	}
}

// TestWatchMoreMenu 播放页控制栏渲染「更多」下拉，七个被收纳的直按按钮 id 全部保留，
// 且原有的高频多选项菜单（字幕/倍速/画面）仍在控制栏顶层。
func TestWatchMoreMenu(t *testing.T) {
	_, ts, videoID := newPersistTestServer(t)
	code, html := getHTML(t, ts, "/watch/"+videoID)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	containsAll(t, html,
		`id="more-menu-wrap"`,
		`id="more-menu-btn"`,
		`id="more-dropdown"`,
		// 七个被收纳的直按按钮（StoreOn 夹具下含跳过片头/书签）
		`id="skip-open-btn"`,
		`id="bm-add-btn"`,
		`id="pref-reset-btn"`,
		`id="screenshot-btn"`,
		`id="pip-btn"`,
		`id="kbd-help-btn"`,
		`id="timestamp-link-btn"`,
		// 保留在控制栏顶层的高频菜单
		`id="speed-menu-btn"`,
		`id="fit-menu-btn"`,
	)
	// 旧的独立外层包裹已移除，避免遗留空容器
	for _, gone := range []string{`id="pip-menu-wrap"`, `id="shot-menu-wrap"`, `id="kbd-menu-wrap"`} {
		if strings.Contains(html, gone) {
			t.Errorf("旧外层包裹 %s 应已移除", gone)
		}
	}
}

// TestWatchMoreMenuStoreOff 未启用持久化时，「更多」下拉仍渲染公共功能，
// 但跳过片头/书签两个 StoreOn 专属项不出现。
func TestWatchMoreMenuStoreOff(t *testing.T) {
	ts, _, videoID := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/watch/"+videoID)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	containsAll(t, html,
		`id="more-menu-wrap"`,
		`id="screenshot-btn"`,
		`id="kbd-help-btn"`,
		`id="timestamp-link-btn"`,
	)
	for _, gone := range []string{`id="skip-open-btn"`, `id="bm-add-btn"`} {
		if strings.Contains(html, gone) {
			t.Errorf("未启用持久化时不应渲染 %s", gone)
		}
	}
}
