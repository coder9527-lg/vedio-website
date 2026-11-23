package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestTagAPILifecycle 覆盖打标（201/200）、列表、点查、取消（204 幂等）与空标签回收。
func TestTagAPILifecycle(t *testing.T) {
	_, ts, id := newPersistTestServer(t)

	// 首次打标 → 201
	status, body := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/tags",
		map[string]string{"name": "周末看"})
	if status != http.StatusCreated {
		t.Fatalf("首次打标应 201，实际 %d body=%v", status, body)
	}
	tag, ok := body["tag"].(map[string]any)
	if !ok || tag["name"] != "周末看" || body["created"] != true {
		t.Fatalf("响应体异常: %v", body)
	}
	tagID := int64(tag["id"].(float64))

	// 重复打标（带首尾空白）→ 200 幂等
	status, body = doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/tags",
		map[string]string{"name": " 周末看 "})
	if status != http.StatusOK || body["created"] != false {
		t.Fatalf("重复打标应 200 且 created=false，实际 %d body=%v", status, body)
	}

	// 视频标签点查
	status, body = doJSON(t, ts, http.MethodGet, "/api/v1/videos/"+id+"/tags", nil)
	if status != http.StatusOK {
		t.Fatalf("点查标签应 200，实际 %d", status)
	}
	if list, _ := body["tags"].([]any); len(list) != 1 {
		t.Fatalf("视频应有 1 个标签，实际 %v", body["tags"])
	}

	// 全库标签带计数
	status, body = doJSON(t, ts, http.MethodGet, "/api/v1/tags", nil)
	if status != http.StatusOK {
		t.Fatalf("标签列表应 200，实际 %d", status)
	}
	list, _ := body["tags"].([]any)
	if len(list) != 1 {
		t.Fatalf("全库应有 1 个标签，实际 %v", body["tags"])
	}
	first := list[0].(map[string]any)
	if first["videoCount"].(float64) != 1 {
		t.Fatalf("标签计数应为 1，实际 %v", first["videoCount"])
	}

	// 取消 → 204；空标签自动回收，列表归零
	status, _ = doJSON(t, ts, http.MethodDelete,
		"/api/v1/videos/"+id+"/tags/"+itoa64(tagID), nil)
	if status != http.StatusNoContent {
		t.Fatalf("取消标签应 204，实际 %d", status)
	}
	status, body = doJSON(t, ts, http.MethodGet, "/api/v1/tags", nil)
	if list, _ := body["tags"].([]any); len(list) != 0 {
		t.Fatalf("空标签应被回收，实际 %v", body["tags"])
	}
	// 再删一次仍 204（幂等）
	status, _ = doJSON(t, ts, http.MethodDelete,
		"/api/v1/videos/"+id+"/tags/"+itoa64(tagID), nil)
	if status != http.StatusNoContent {
		t.Fatalf("重复取消应幂等 204，实际 %d", status)
	}
}

// TestTagAPIValidation 覆盖 400（空名/非法 ID）、404（未知视频）。
func TestTagAPIValidation(t *testing.T) {
	_, ts, id := newPersistTestServer(t)

	if status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/tags",
		map[string]string{"name": "   "}); status != http.StatusBadRequest {
		t.Fatalf("空标签名应 400，实际 %d", status)
	}
	if status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/missing/tags",
		map[string]string{"name": "x"}); status != http.StatusNotFound {
		t.Fatalf("未知视频应 404，实际 %d", status)
	}
	if status, _ := doJSON(t, ts, http.MethodGet, "/api/v1/videos/missing/tags",
		nil); status != http.StatusNotFound {
		t.Fatalf("未知视频点查应 404，实际 %d", status)
	}
	if status, _ := doJSON(t, ts, http.MethodDelete,
		"/api/v1/videos/"+id+"/tags/abc", nil); status != http.StatusBadRequest {
		t.Fatalf("非法标签 ID 应 400，实际 %d", status)
	}
}

// TestTagIndexFilterAndRender 验证首页 ?tag= 筛选、chips 与卡片标签渲染，
// 并确认标签筛选与收藏视图正交。
func TestTagIndexFilterAndRender(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)

	if _, _, err := srv.store.SetVideoTag(id, "经典"); err != nil {
		t.Fatalf("打标失败: %v", err)
	}

	// chips 与卡片标签出现在首页 HTML
	code, html := getHTML(t, ts, "/")
	if code != http.StatusOK {
		t.Fatalf("首页应 200，实际 %d", code)
	}
	containsAll(t, html, "经典", "全部标签", "card-tag")

	// 按标签筛选：命中（卡片网格渲染 video-card）
	code, html = getHTML(t, ts, "/?tag="+url.QueryEscape("经典"))
	if code != http.StatusOK || !strings.Contains(html, "video-card") {
		t.Fatalf("标签筛选应保留该视频，code=%d", code)
	}
	// 不存在的标签：卡片网格为空（页面仍 200，渲染 empty 块）
	code, html = getHTML(t, ts, "/?tag="+url.QueryEscape("不存在的标签"))
	if code != http.StatusOK || strings.Contains(html, "video-card") {
		t.Fatalf("未知标签应得到空结果，code=%d", code)
	}

	// 与收藏视图正交：收藏后 ?view=favorites&tag=经典 仍命中
	if err := srv.store.SetFavorite(id, true); err != nil {
		t.Fatal(err)
	}
	code, html = getHTML(t, ts, "/?view=favorites&tag="+url.QueryEscape("经典"))
	if code != http.StatusOK || !strings.Contains(html, "video-card") {
		t.Fatalf("收藏+标签正交筛选应命中，code=%d", code)
	}
	// 收藏视图叠加一个不存在的标签 → 空
	code, html = getHTML(t, ts, "/?view=favorites&tag="+url.QueryEscape("不存在"))
	if code != http.StatusOK || strings.Contains(html, "video-card") {
		t.Fatalf("收藏视图叠加未知标签应为空，code=%d", code)
	}
}

// TestTagAPIStoreDisabled 未启用持久化时四个接口全部 503，页面不渲染标签区。
func TestTagAPIStoreDisabled(t *testing.T) {
	_, ts := newTestServer(t, "")

	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/tags", nil},
		{http.MethodGet, "/api/v1/videos/x/tags", nil},
		{http.MethodPut, "/api/v1/videos/x/tags", map[string]string{"name": "x"}},
		{http.MethodDelete, "/api/v1/videos/x/tags/1", nil},
	} {
		if status, _ := doJSON(t, ts, c.method, c.path, c.body); status != http.StatusServiceUnavailable {
			t.Fatalf("%s %s 未启用持久化应 503，实际 %d", c.method, c.path, status)
		}
	}
	code, html := getHTML(t, ts, "/")
	if code != http.StatusOK || strings.Contains(html, "watch-tags") {
		t.Fatalf("未启用持久化时不应出现标签区，code=%d", code)
	}
}
