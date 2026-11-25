package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// addSmartFixture 在持久化测试服务器的媒体根写入两个额外视频并重扫，返回 name→ID。
func addSmartFixture(t *testing.T, srv *Server) map[string]string {
	t.Helper()
	root := srv.library.Root()
	rels := []string{"alpha.mp4", "beta.mp4"}
	for _, rel := range rels {
		p := filepath.Join(root, rel)
		if err := os.WriteFile(p, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := srv.library.Scan(); err != nil {
		t.Fatalf("重扫失败: %v", err)
	}
	ids := map[string]string{}
	for _, v := range srv.library.List() {
		ids[v.Name] = v.ID
	}
	return ids
}

// TestSmartCenterListsBuiltinRules 中心页列出三条内置规则；无持久化时仅未观看可见。
func TestSmartCenterListsBuiltinRules(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	addSmartFixture(t, srv)

	code, body := getHTML(t, ts, "/smart")
	if code != http.StatusOK {
		t.Fatalf("中心页状态码 = %d", code)
	}
	for _, name := range []string{"未观看", "继续观看", "我的收藏"} {
		if !strings.Contains(body, name) {
			t.Errorf("中心页应包含规则 %q", name)
		}
	}
	if !strings.Contains(body, "data-smart-rules") {
		t.Error("中心页应渲染规则容器")
	}

	// 无持久化：继续观看/收藏依赖 store，不应出现；未观看仍在
	_, tsNoStore := newTestServer(t, "")
	code, body = getHTML(t, tsNoStore, "/smart")
	if code != http.StatusOK {
		t.Fatalf("无持久化中心页状态码 = %d", code)
	}
	if !strings.Contains(body, "未观看") {
		t.Error("无持久化时未观看规则仍应展示")
	}
	if strings.Contains(body, "继续观看") || strings.Contains(body, "我的收藏") {
		t.Error("无持久化时不应展示依赖 store 的规则")
	}
}

// TestSmartUnwatchedRule 未观看规则排除已有进度的视频。
func TestSmartUnwatchedRule(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	ids := addSmartFixture(t, srv)

	code, body := getHTML(t, ts, "/smart/unwatched")
	if code != http.StatusOK || !strings.Contains(body, "alpha.mp4") ||
		!strings.Contains(body, "beta.mp4") {
		t.Fatalf("初始全部视频应未观看，code=%d", code)
	}

	// 给 alpha 写有效进度后，它应从未观看清单消失
	if status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+ids["alpha"]+"/progress",
		map[string]float64{"position": 60, "duration": 100}); status != http.StatusOK {
		t.Fatalf("写进度失败，状态码 = %d", status)
	}
	code, body = getHTML(t, ts, "/smart/unwatched")
	if code != http.StatusOK {
		t.Fatalf("状态码 = %d", code)
	}
	if strings.Contains(body, "alpha.mp4") {
		t.Error("已有进度的 alpha 不应出现在未观看清单")
	}
	if !strings.Contains(body, "beta.mp4") {
		t.Error("无进度的 beta 应保留在未观看清单")
	}
}

// TestSmartContinueRule 继续观看只收有效进度（>5s 且 <95%），并排除已看完/片头抖动。
func TestSmartContinueRule(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	ids := addSmartFixture(t, srv)

	// 空清单空态
	code, body := getHTML(t, ts, "/smart/continue")
	if code != http.StatusOK || !strings.Contains(body, "data-smart-empty") {
		t.Fatalf("无进度时应显示空态，code=%d", code)
	}

	// alpha 有效进度 → 入选
	doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+ids["alpha"]+"/progress",
		map[string]float64{"position": 50, "duration": 100})
	// beta 已看到 98% → 视为看完，不入选
	doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+ids["beta"]+"/progress",
		map[string]float64{"position": 98, "duration": 100})

	code, body = getHTML(t, ts, "/smart/continue")
	if code != http.StatusOK {
		t.Fatalf("状态码 = %d", code)
	}
	if !strings.Contains(body, "alpha.mp4") {
		t.Error("有效进度的 alpha 应出现在继续观看")
	}
	if strings.Contains(body, "beta.mp4") {
		t.Error("已看完的 beta 不应出现在继续观看")
	}
}

// TestSmartFavoritesRule 收藏规则实时反映收藏状态。
func TestSmartFavoritesRule(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	ids := addSmartFixture(t, srv)

	code, body := getHTML(t, ts, "/smart/favorites")
	if code != http.StatusOK || !strings.Contains(body, "data-smart-empty") {
		t.Fatalf("无收藏时应空态，code=%d", code)
	}

	if status, _ := doJSON(t, ts, http.MethodPost, "/api/v1/videos/"+ids["beta"]+"/favorite",
		map[string]any{}); status != http.StatusOK {
		t.Fatalf("收藏失败，状态码 = %d", status)
	}
	code, body = getHTML(t, ts, "/smart/favorites")
	if code != http.StatusOK || !strings.Contains(body, "beta.mp4") ||
		strings.Contains(body, "alpha.mp4") {
		t.Fatalf("收藏清单应只含 beta，code=%d", code)
	}
}

// TestSmartTagRule 标签规则按标签视频集合实时计算；标签删除/伪造返回 404。
func TestSmartTagRule(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	ids := addSmartFixture(t, srv)

	if status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+ids["alpha"]+"/tags",
		map[string]string{"name": "精选"}); status != http.StatusCreated {
		t.Fatalf("打标签失败，状态码 = %d", status)
	}

	// 中心页应出现标签规则卡片与正确链接
	_, body := getHTML(t, ts, "/smart")
	wantURL := "/smart/tag?name=" + url.QueryEscape("精选")
	if !strings.Contains(body, wantURL) {
		t.Errorf("中心页应包含标签规则链接 %q", wantURL)
	}

	code, body := getHTML(t, ts, wantURL)
	if code != http.StatusOK || !strings.Contains(body, "alpha.mp4") ||
		strings.Contains(body, "beta.mp4") {
		t.Fatalf("标签清单应只含 alpha，code=%d", code)
	}

	// 不存在的标签 → 404，不静默回退
	code, _ = getHTML(t, ts, "/smart/tag?name="+url.QueryEscape("幽灵标签"))
	if code != http.StatusNotFound {
		t.Errorf("幽灵标签应 404，实际 %d", code)
	}
}

// TestSmartRuleGuards 非法规则名 404；无持久化访问 store 依赖规则 404。
func TestSmartRuleGuards(t *testing.T) {
	_, ts, _ := newPersistTestServer(t)
	if code, _ := getHTML(t, ts, "/smart/not-a-rule"); code != http.StatusNotFound {
		t.Errorf("非法规则应 404，实际 %d", code)
	}

	_, tsNoStore := newTestServer(t, "")
	for _, p := range []string{"/smart/continue", "/smart/favorites", "/smart/tag?name=x"} {
		if code, _ := getHTML(t, tsNoStore, p); code != http.StatusNotFound {
			t.Errorf("无持久化访问 %s 应 404，实际 %d", p, code)
		}
	}
	// 未观看不依赖 store，无持久化仍可访问
	if code, _ := getHTML(t, tsNoStore, "/smart/unwatched"); code != http.StatusOK {
		t.Errorf("无持久化访问未观看应 200，实际 %d", code)
	}
}
