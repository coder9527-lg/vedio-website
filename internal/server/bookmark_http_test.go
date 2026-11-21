package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// itoa64 把书签主键转成路径字符串。
func itoa64(id int64) string { return strconv.FormatInt(id, 10) }

// bookmarksFrom 从响应体取出书签数组，结构异常时终止测试。
func bookmarksFrom(t *testing.T, body map[string]any) []any {
	t.Helper()
	raw, ok := body["bookmarks"]
	if !ok {
		t.Fatalf("响应缺少 bookmarks 字段: %v", body)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("bookmarks 字段类型异常: %T", raw)
	}
	return list
}

// bookmarkFrom 从响应体取出单个书签对象。
func bookmarkFrom(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	raw, ok := body["bookmark"]
	if !ok {
		t.Fatalf("响应缺少 bookmark 字段: %v", body)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("bookmark 字段类型异常: %T", raw)
	}
	return m
}

// TestBookmarkAPIFullLifecycle 验证空列表、创建两条、排序、改备注、删除的完整链路。
func TestBookmarkAPIFullLifecycle(t *testing.T) {
	_, ts, id := newPersistTestServer(t)
	path := "/api/v1/videos/" + id + "/bookmarks"

	// 初始空列表回空数组而非 null
	code, body := doJSON(t, ts, http.MethodGet, path, nil)
	if code != http.StatusOK || len(bookmarksFrom(t, body)) != 0 {
		t.Fatalf("初始 GET 期望 200 空数组，实际 %d %v", code, body)
	}

	// 创建两条：先 120s 带备注，再 30s 空备注
	code, body = doJSON(t, ts, http.MethodPost, path,
		map[string]any{"position": 120, "note": "精彩片段"})
	if code != http.StatusCreated {
		t.Fatalf("首次 POST 期望 201，实际 %d %v", code, body)
	}
	first := bookmarkFrom(t, body)
	if first["id"] == nil || first["position"] != 120.0 || first["note"] != "精彩片段" {
		t.Fatalf("首次 POST 回显不符: %v", body)
	}
	code, body = doJSON(t, ts, http.MethodPost, path,
		map[string]any{"position": 30})
	if code != http.StatusCreated {
		t.Fatalf("二次 POST 期望 201，实际 %d %v", code, body)
	}
	second := bookmarkFrom(t, body)
	if second["note"] != "" {
		t.Fatalf("省略 note 应为空串，实际 %v", second["note"])
	}

	// 列表按位置升序：30 在 120 前
	code, body = doJSON(t, ts, http.MethodGet, path, nil)
	if code != http.StatusOK {
		t.Fatalf("GET 列表期望 200，实际 %d", code)
	}
	list := bookmarksFrom(t, body)
	if len(list) != 2 {
		t.Fatalf("期望 2 条书签，实际 %d", len(list))
	}
	if list[0].(map[string]any)["position"] != 30.0 ||
		list[1].(map[string]any)["position"] != 120.0 {
		t.Fatalf("列表应按位置升序，实际 %v", list)
	}

	// PATCH 修改第一条（120s）的备注
	bid := int64(first["id"].(float64))
	code, body = doJSON(t, ts, http.MethodPatch,
		path+"/"+itoa64(bid), map[string]any{"note": "改写后的备注"})
	if code != http.StatusOK {
		t.Fatalf("PATCH 期望 200，实际 %d %v", code, body)
	}
	if got := bookmarkFrom(t, body)["note"]; got != "改写后的备注" {
		t.Fatalf("PATCH 回显备注不符: %v", got)
	}

	// 删除第二条后列表只剩第一条
	code, body = doJSON(t, ts, http.MethodDelete,
		path+"/"+itoa64(int64(second["id"].(float64))), nil)
	if code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("DELETE 期望 200 status=ok，实际 %d %v", code, body)
	}
	code, body = doJSON(t, ts, http.MethodGet, path, nil)
	list = bookmarksFrom(t, body)
	if len(list) != 1 || list[0].(map[string]any)["note"] != "改写后的备注" {
		t.Fatalf("删除后应剩 1 条且为修改后的备注，实际 %v", list)
	}

	// 重复删除幂等
	code, _ = doJSON(t, ts, http.MethodDelete,
		path+"/"+itoa64(int64(second["id"].(float64))), nil)
	if code != http.StatusOK {
		t.Fatalf("重复 DELETE 应幂等 200，实际 %d", code)
	}
}

// TestBookmarkAPIValidation 覆盖 400 校验：负位置、非法 JSON、非法书签 ID。
func TestBookmarkAPIValidation(t *testing.T) {
	_, ts, id := newPersistTestServer(t)
	path := "/api/v1/videos/" + id + "/bookmarks"

	if code, _ := doJSON(t, ts, http.MethodPost, path,
		map[string]any{"position": -5}); code != http.StatusBadRequest {
		t.Fatalf("负位置 POST 期望 400，实际 %d", code)
	}

	// 非法 JSON
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("非法 JSON 请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 JSON 期望 400，实际 %d", resp.StatusCode)
	}

	// 非法书签 ID
	if code, _ := doJSON(t, ts, http.MethodPatch, path+"/abc",
		map[string]any{"note": "x"}); code != http.StatusBadRequest {
		t.Fatalf("非法 bid PATCH 期望 400，实际 %d", code)
	}
	if code, _ := doJSON(t, ts, http.MethodDelete, path+"/abc", nil); code != http.StatusBadRequest {
		t.Fatalf("非法 bid DELETE 期望 400，实际 %d", code)
	}
}

// TestBookmarkAPINotFound 覆盖视频不存在、书签不存在与跨视频归属三种 404。
func TestBookmarkAPINotFound(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)

	// 对不存在的视频创建书签 → 404
	code, _ := doJSON(t, ts, http.MethodPost,
		"/api/v1/videos/ghost-video/bookmarks", map[string]any{"position": 10})
	if code != http.StatusNotFound {
		t.Fatalf("ghost 视频 POST 期望 404，实际 %d", code)
	}

	// 先在 id 下创建一条
	_, body := doJSON(t, ts, http.MethodPost,
		"/api/v1/videos/"+id+"/bookmarks", map[string]any{"position": 10, "note": "原备注"})
	bid := itoa64(int64(bookmarkFrom(t, body)["id"].(float64)))

	// 不存在的书签 ID → 404
	if code, _ := doJSON(t, ts, http.MethodPatch,
		"/api/v1/videos/"+id+"/bookmarks/999999",
		map[string]any{"note": "x"}); code != http.StatusNotFound {
		t.Fatalf("不存在 bid PATCH 期望 404，实际 %d", code)
	}

	// 造第二个视频，拿 A 视频的书签 ID 去 B 视频路径下改 → 归属不命中 404
	secondPath := filepath.Join(srv.library.Root(), "second.mp4")
	if err := os.WriteFile(secondPath, []byte("fake2"), 0o644); err != nil {
		t.Fatalf("写入第二个测试视频失败: %v", err)
	}
	if _, err := srv.library.Scan(); err != nil {
		t.Fatalf("重扫失败: %v", err)
	}
	var secondID string
	for _, v := range srv.library.List() {
		if v.ID != id {
			secondID = v.ID
		}
	}
	if secondID == "" {
		t.Fatal("未找到第二个视频 ID")
	}
	if code, _ := doJSON(t, ts, http.MethodPatch,
		"/api/v1/videos/"+secondID+"/bookmarks/"+bid,
		map[string]any{"note": "越权"}); code != http.StatusNotFound {
		t.Fatalf("跨视频 PATCH 期望 404，实际 %d", code)
	}
	// 原备注未被改动
	_, listBody := doJSON(t, ts, http.MethodGet,
		"/api/v1/videos/"+id+"/bookmarks", nil)
	list := bookmarksFrom(t, listBody)
	if list[0].(map[string]any)["note"] != "原备注" {
		t.Fatal("跨视频修改不应落库")
	}
}

// TestBookmarkWatchPageInjection 验证播放页注入书签 UI 与初始数据，以及无 DB 时整块消失。
func TestBookmarkWatchPageInjection(t *testing.T) {
	_, ts, id := newPersistTestServer(t)
	path := "/api/v1/videos/" + id + "/bookmarks"

	// 先经 API 打两条书签，一条带备注
	doJSON(t, ts, http.MethodPost, path, map[string]any{"position": 120, "note": "精彩片段"})
	doJSON(t, ts, http.MethodPost, path, map[string]any{"position": 30})

	code, html := getHTML(t, ts, "/watch/"+id)
	if code != http.StatusOK {
		t.Fatalf("播放页期望 200，实际 %d", code)
	}
	for _, want := range []string{
		`id="bm-add-btn"`,
		`id="bm-marker-bar"`,
		`id="bm-panel"`,
		`id="bm-list"`,
		`id="bm-toast"`,
		`书签（`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少书签片段 %q", want)
		}
	}
	// 初始数据经 data-bookmarks 注入（属性上下文双引号被模板转义为 &#34;，断言转义后的字段）
	if !strings.Contains(html, "position&#34;:120") || !strings.Contains(html, "position&#34;:30") {
		t.Error("播放页应注入两条书签的位置数据")
	}
	if !strings.Contains(html, "精彩片段") {
		t.Error("播放页应注入书签备注文本")
	}

	// 无持久化的服务：自建视频重扫后访问，书签 UI 整块不渲染
	srvNoDB, tsNoDB := newTestServer(t, "")
	noDBPath := filepath.Join(srvNoDB.library.Root(), "plain.mp4")
	if err := os.WriteFile(noDBPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("写入无 DB 测试视频失败: %v", err)
	}
	if _, err := srvNoDB.library.Scan(); err != nil {
		t.Fatalf("无 DB 服务重扫失败: %v", err)
	}
	noDBID := srvNoDB.library.List()[0].ID
	code, html = getHTML(t, tsNoDB, "/watch/"+noDBID)
	if code != http.StatusOK {
		t.Fatalf("无 DB 播放页期望 200，实际 %d", code)
	}
	if strings.Contains(html, "bm-add-btn") || strings.Contains(html, "bm-panel") ||
		strings.Contains(html, "bm-marker-bar") {
		t.Error("未启用持久化时播放页不应渲染任何书签 UI")
	}
}

// TestBookmarkWatchPageEmptyState 验证无书签时面板仍渲染且注入空数组。
func TestBookmarkWatchPageEmptyState(t *testing.T) {
	_, ts, id := newPersistTestServer(t)
	code, html := getHTML(t, ts, "/watch/"+id)
	if code != http.StatusOK {
		t.Fatalf("播放页期望 200，实际 %d", code)
	}
	if !strings.Contains(html, `id="bm-empty"`) {
		t.Error("无书签时应渲染空态提示")
	}
	if !strings.Contains(html, `data-bookmarks="null"`) && !strings.Contains(html, `data-bookmarks="[]"`) {
		t.Error("无书签时应注入 null 或空数组的初始数据")
	}
}
