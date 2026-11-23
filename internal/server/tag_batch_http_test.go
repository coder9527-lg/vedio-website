package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// newPersistShowTestServer 构造带 SQLite、含一部两集剧与一部电影的测试服务，
// 供批量标签的剧集展开用例使用。
func newPersistShowTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"剧A/剧A.S01E01.mkv":  "a",
		"剧A/剧A.S01E02.mkv":  "b",
		"电影 2024 1080p.mkv": "c",
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatalf("NewThumbnailer 失败: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(lib, thumbs, nil, st, nil, nil, false)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return srv, newHTTPTestServer(t, srv.Handler())
}

// tagIDFrom 从批量打标响应中取出标签 ID。
func tagIDFrom(t *testing.T, body map[string]any) int64 {
	t.Helper()
	tag, ok := body["tag"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 tag 对象: %v", body)
	}
	return int64(tag["id"].(float64))
}

// TestBatchTagSetAndRemove 覆盖批量打标（201/200 幂等）、缺失 ID 跳过与批量取消计数。
func TestBatchTagSetAndRemove(t *testing.T) {
	_, ts, id := newPersistTestServer(t)
	items := []map[string]string{{"type": "video", "id": id}}

	// 首次批量打标 → 201，added=1
	status, body := doJSON(t, ts, http.MethodPost, "/api/v1/tags/batch-set",
		map[string]any{"name": "周末看", "items": items})
	if status != http.StatusCreated {
		t.Fatalf("首次批量打标应 201，实际 %d body=%v", status, body)
	}
	if body["added"].(float64) != 1 || body["skipped"].(float64) != 0 || body["total"].(float64) != 1 {
		t.Fatalf("计数异常: %v", body)
	}
	tagID := tagIDFrom(t, body)

	// 重复打标 → 200，全部幂等 skipped
	status, body = doJSON(t, ts, http.MethodPost, "/api/v1/tags/batch-set",
		map[string]any{"name": "周末看", "items": items})
	if status != http.StatusOK || body["added"].(float64) != 0 || body["skipped"].(float64) != 1 {
		t.Fatalf("重复批量打标应 200 且 skipped=1，实际 %d body=%v", status, body)
	}

	// 含缺失 ID：有效 +1、缺失计入 skipped，total 按展开后数量计
	status, body = doJSON(t, ts, http.MethodPost, "/api/v1/tags/batch-set",
		map[string]any{"name": "新片", "items": []map[string]string{
			{"type": "video", "id": id},
			{"type": "video", "id": "missing-id"},
		}})
	if status != http.StatusCreated || body["added"].(float64) != 1 ||
		body["skipped"].(float64) != 1 || body["total"].(float64) != 2 {
		t.Fatalf("缺失 ID 应跳过计数，实际 %d body=%v", status, body)
	}

	// 全部缺失：短路 200 零计数，不创建标签
	status, body = doJSON(t, ts, http.MethodPost, "/api/v1/tags/batch-set",
		map[string]any{"name": "悬空标签", "items": []map[string]string{
			{"type": "video", "id": "nope1"},
			{"type": "video", "id": "nope2"},
		}})
	if status != http.StatusOK || body["added"].(float64) != 0 ||
		body["skipped"].(float64) != 2 || body["total"].(float64) != 2 {
		t.Fatalf("全缺失应 200 零计数，实际 %d body=%v", status, body)
	}
	if tags, _ := srvTagList(t, ts); len(tags) != 2 {
		t.Fatalf("全缺失不应创建悬空标签，实际 %v", tags)
	}

	// 批量取消：移除 1 个关联，空标签自动回收
	status, body = doJSON(t, ts, http.MethodPost, "/api/v1/tags/batch-remove",
		map[string]any{"tagId": tagID, "items": items})
	if status != http.StatusOK || body["removed"].(float64) != 1 ||
		body["skipped"].(float64) != 0 || body["total"].(float64) != 1 {
		t.Fatalf("批量取消计数异常，实际 %d body=%v", status, body)
	}
	if tags, _ := srvTagList(t, ts); hasTag(tags, "周末看") {
		t.Fatalf("引用归零的标签应被回收: %v", tags)
	}

	// 未知标签批量取消 → 404
	status, _ = doJSON(t, ts, http.MethodPost, "/api/v1/tags/batch-remove",
		map[string]any{"tagId": 99999, "items": items})
	if status != http.StatusNotFound {
		t.Fatalf("未知标签批量取消应 404，实际 %d", status)
	}
}

// TestBatchTagSetShowExpansion 验证剧集卡 item 展开为全部单集，且与电影 item 合并去重。
func TestBatchTagSetShowExpansion(t *testing.T) {
	srv, ts := newPersistShowTestServer(t)

	shows := srv.library.ListShows()
	if len(shows) != 1 {
		t.Fatalf("期望 1 部剧，实际 %d", len(shows))
	}
	showID := shows[0].ID
	movies := srv.library.List()
	if len(movies) != 3 { // 两集单集 + 一部电影
		t.Fatalf("期望 3 个视频，实际 %d", len(movies))
	}
	var movieID string
	for _, v := range movies {
		if !srv.library.IsShowEpisode(v.ID) {
			movieID = v.ID
		}
	}
	if movieID == "" {
		t.Fatal("未找到电影视频")
	}

	status, body := doJSON(t, ts, http.MethodPost, "/api/v1/tags/batch-set",
		map[string]any{"name": "合集", "items": []map[string]string{
			{"type": "show", "id": showID},
			{"type": "video", "id": movieID},
		}})
	if status != http.StatusCreated {
		t.Fatalf("剧集展开批量打标应 201，实际 %d body=%v", status, body)
	}
	if body["added"].(float64) != 3 || body["total"].(float64) != 3 {
		t.Fatalf("剧 2 集 + 电影应展开为 3 个视频: %v", body)
	}

	// 每个视频都能点查到该标签
	tagID := tagIDFrom(t, body)
	for _, v := range movies {
		tags, err := srv.store.VideoTags(v.ID)
		if err != nil {
			t.Fatalf("查询视频标签失败: %v", err)
		}
		if len(tags) != 1 || tags[0].ID != tagID {
			t.Fatalf("视频 %s 应已打标，实际 %+v", v.ID, tags)
		}
	}

	// 剧集整体批量取消：removed=2，标签仍被电影引用故不回收
	status, body = doJSON(t, ts, http.MethodPost, "/api/v1/tags/batch-remove",
		map[string]any{"tagId": tagID, "items": []map[string]string{
			{"type": "show", "id": showID},
		}})
	if status != http.StatusOK || body["removed"].(float64) != 2 {
		t.Fatalf("剧集批量取消应 removed=2，实际 %d body=%v", status, body)
	}
	if tags, _ := srvTagList(t, ts); !hasTag(tags, "合集") {
		t.Fatalf("仍有电影引用的标签不应被回收: %v", tags)
	}
}

// TestBatchTagValidation 覆盖批量接口的 400 场景与 503 门禁。
func TestBatchTagValidation(t *testing.T) {
	_, ts, id := newPersistTestServer(t)
	items := []map[string]string{{"type": "video", "id": id}}

	// 空标签名
	if status, _ := doJSON(t, ts, http.MethodPost, "/api/v1/tags/batch-set",
		map[string]any{"name": "  ", "items": items}); status != http.StatusBadRequest {
		t.Fatalf("空标签名应 400，实际 %d", status)
	}
	// 空 items
	if status, _ := doJSON(t, ts, http.MethodPost, "/api/v1/tags/batch-set",
		map[string]any{"name": "x", "items": []map[string]string{}}); status != http.StatusBadRequest {
		t.Fatalf("空 items 应 400，实际 %d", status)
	}
	// 未知类型 item 展开为空 → 400
	if status, _ := doJSON(t, ts, http.MethodPost, "/api/v1/tags/batch-set",
		map[string]any{"name": "x", "items": []map[string]string{
			{"type": "album", "id": id},
		}}); status != http.StatusBadRequest {
		t.Fatalf("未知 item 类型应 400，实际 %d", status)
	}
	// 坏 JSON
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/tags/batch-set",
		strings.NewReader("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏 JSON 应 400，实际 %d", resp.StatusCode)
	}
	// 批量取消非法 tagId
	if status, _ := doJSON(t, ts, http.MethodPost, "/api/v1/tags/batch-remove",
		map[string]any{"tagId": 0, "items": items}); status != http.StatusBadRequest {
		t.Fatalf("非法 tagId 应 400，实际 %d", status)
	}

	// 未启用持久化：四个新接口全部 503
	_, tsNoDB := newTestServer(t, "")
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPatch, "/api/v1/tags/1", map[string]string{"name": "x"}},
		{http.MethodDelete, "/api/v1/tags/1", nil},
		{http.MethodPost, "/api/v1/tags/batch-set", map[string]any{"name": "x", "items": items}},
		{http.MethodPost, "/api/v1/tags/batch-remove", map[string]any{"tagId": 1, "items": items}},
	} {
		if status, _ := doJSON(t, tsNoDB, c.method, c.path, c.body); status != http.StatusServiceUnavailable {
			t.Fatalf("%s %s 未启用持久化应 503，实际 %d", c.method, c.path, status)
		}
	}
}

// TestTagRenameAndDelete 覆盖标签重命名（200/409/404/400）与全局删除（204/404/级联）。
func TestTagRenameAndDelete(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)

	// 准备两个标签
	if _, _, err := srv.store.SetVideoTag(id, "周末看"); err != nil {
		t.Fatalf("打标失败: %v", err)
	}
	first, _, err := srv.store.SetVideoTag(id, "经典")
	if err != nil {
		t.Fatalf("打标失败: %v", err)
	}

	// 普通重命名 → 200
	status, body := doJSON(t, ts, http.MethodPatch, "/api/v1/tags/"+itoa64(first.ID),
		map[string]string{"name": " 经典必看 "})
	if status != http.StatusOK {
		t.Fatalf("重命名应 200，实际 %d body=%v", status, body)
	}
	if tag, _ := body["tag"].(map[string]any); tag["name"] != "经典必看" {
		t.Fatalf("响应标签名异常: %v", body)
	}

	// 与已有标签重名（大小写不敏感）→ 409，两个标签都不变
	status, body = doJSON(t, ts, http.MethodPatch, "/api/v1/tags/"+itoa64(first.ID),
		map[string]string{"name": "周末看"})
	if status != http.StatusConflict {
		t.Fatalf("重名应 409，实际 %d body=%v", status, body)
	}
	tags, _ := srvTagList(t, ts)
	if !hasTag(tags, "周末看") || !hasTag(tags, "经典必看") {
		t.Fatalf("409 后两标签均应保持不变: %v", tags)
	}

	// 仅改大小写 → 200 放行
	status, _ = doJSON(t, ts, http.MethodPatch, "/api/v1/tags/"+itoa64(first.ID),
		map[string]string{"name": "经典必看！"})
	if status != http.StatusOK {
		t.Fatalf("改名应 200，实际 %d", status)
	}

	// 非法参数：未知 ID → 404；非法 ID → 400；空名 → 400
	if status, _ := doJSON(t, ts, http.MethodPatch, "/api/v1/tags/99999",
		map[string]string{"name": "x"}); status != http.StatusNotFound {
		t.Fatalf("未知标签重命名应 404，实际 %d", status)
	}
	if status, _ := doJSON(t, ts, http.MethodPatch, "/api/v1/tags/abc",
		map[string]string{"name": "x"}); status != http.StatusBadRequest {
		t.Fatalf("非法标签 ID 应 400，实际 %d", status)
	}
	if status, _ := doJSON(t, ts, http.MethodPatch, "/api/v1/tags/"+itoa64(first.ID),
		map[string]string{"name": "  "}); status != http.StatusBadRequest {
		t.Fatalf("空名应 400，实际 %d", status)
	}

	// 全局删除 → 204，标签与关联一并清除
	status, _ = doJSON(t, ts, http.MethodDelete, "/api/v1/tags/"+itoa64(first.ID), nil)
	if status != http.StatusNoContent {
		t.Fatalf("全局删除应 204，实际 %d", status)
	}
	if tags, _ := srvTagList(t, ts); hasTag(tags, "经典必看！") {
		t.Fatalf("删除后标签不应残留: %v", tags)
	}
	if vtags, err := srv.store.VideoTags(id); err != nil || len(vtags) != 1 {
		t.Fatalf("级联删除后该视频应只剩 1 个标签，实际 %+v err=%v", vtags, err)
	}
	// 再删 → 404；非法 ID → 400
	if status, _ := doJSON(t, ts, http.MethodDelete, "/api/v1/tags/"+itoa64(first.ID), nil); status != http.StatusNotFound {
		t.Fatalf("重复删除应 404，实际 %d", status)
	}
	if status, _ := doJSON(t, ts, http.MethodDelete, "/api/v1/tags/abc", nil); status != http.StatusBadRequest {
		t.Fatalf("非法标签 ID 应 400，实际 %d", status)
	}
}

// srvTagList 拉取全库标签并返回名称列表。
func srvTagList(t *testing.T, ts *httptest.Server) ([]map[string]any, error) {
	t.Helper()
	_, body := doJSON(t, ts, http.MethodGet, "/api/v1/tags", nil)
	raw, ok := body["tags"].([]any)
	if !ok {
		t.Fatalf("标签列表结构异常: %v", body)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(map[string]any))
	}
	return out, nil
}

// hasTag 判断标签响应列表中是否包含指定名称。
func hasTag(tags []map[string]any, name string) bool {
	for _, tg := range tags {
		if tg["name"] == name {
			return true
		}
	}
	return false
}

// TestBatchTagUIRendered 验证开启持久化时首页渲染批量管理 DOM；未开启时完全不渲染。
func TestBatchTagUIRendered(t *testing.T) {
	_, ts, id := newPersistTestServer(t)
	if st, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/tags",
		map[string]string{"name": "周末看"}); st != http.StatusCreated {
		t.Fatalf("准备标签应 201，实际 %d", st)
	}
	status, html := getHTML(t, ts, "/")
	if status != http.StatusOK {
		t.Fatalf("首页应 200，实际 %d", status)
	}
	containsAll(t, html,
		`id="batch-mode-btn"`,
		`id="batch-bar"`,
		`id="batch-set-modal"`,
		`id="batch-remove-modal"`,
		`id="batch-tag-options"`,
		`class="batch-pick"`,
		`class="batch-check"`,
		`tag-rename-btn`,
		`tag-delete-btn`,
		`data-tag-id="`,
	)

	// 未启用持久化：批量 UI 与管理按钮均不渲染
	_, tsNoDB := newTestServer(t, "")
	_, htmlNoDB := getHTML(t, tsNoDB, "/")
	for _, marker := range []string{
		"batch-mode-btn", "batch-bar", "batch-set-modal", "batch-pick",
		"tag-rename-btn", "tag-delete-btn",
	} {
		if strings.Contains(htmlNoDB, marker) {
			t.Fatalf("未启用持久化时不应渲染 %s", marker)
		}
	}
}
