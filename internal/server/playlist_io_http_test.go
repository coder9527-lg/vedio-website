package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// downloadPlaylistsJSON 请求导出接口，返回状态码、响应头与原始正文。
func downloadPlaylistsJSON(t *testing.T, ts *httptest.Server) (int, http.Header, []byte) {
	t.Helper()
	resp, err := noRedirectClient.Get(ts.URL + "/api/v1/export/playlists.json")
	if err != nil {
		t.Fatalf("导出请求失败: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取导出正文失败: %v", err)
	}
	return resp.StatusCode, resp.Header, body
}

// uploadPlaylistsJSON 以 multipart 表单上传导入文件（字段名 file），返回状态码与解析后的 JSON。
func uploadPlaylistsJSON(t *testing.T, ts *httptest.Server, filename string, content []byte) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("创建 multipart 字段失败: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("写入 multipart 正文失败: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭 multipart writer 失败: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/import/playlists", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("导入请求失败: %v", err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// uploadEmptyMultipart 提交不含 file 字段的空 multipart 表单。
func uploadEmptyMultipart(t *testing.T, ts *httptest.Server) int {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭 multipart writer 失败: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/import/playlists", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("空表单导入请求失败: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// decodeExport 把导出字节解析为顶层结构（用 map 与切片断言，避免与生产结构体耦合）。
func decodeExport(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("导出 JSON 解析失败: %v", err)
	}
	return doc
}

func TestExportPlaylistsEmptyLibrary(t *testing.T) {
	_, ts := newPlaylistTestServer(t)

	status, header, body := downloadPlaylistsJSON(t, ts)
	if status != http.StatusOK {
		t.Fatalf("空库导出应 200，实际 %d", status)
	}
	if ct := header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type 应为 JSON，实际 %q", ct)
	}
	cd := header.Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "playlists-") || !strings.HasSuffix(cd, `.json"`) {
		t.Fatalf("Content-Disposition 不符: %q", cd)
	}
	doc := decodeExport(t, body)
	if doc["version"].(float64) != 1 {
		t.Fatalf("version 应为 1，实际 %v", doc["version"])
	}
	pls, ok := doc["playlists"].([]any)
	if !ok || len(pls) != 0 {
		t.Fatalf("空库 playlists 应为空数组，实际 %v", doc["playlists"])
	}
}

func TestExportPlaylistsWithData(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	videos := srv.library.List()
	if len(videos) != 3 {
		t.Fatalf("夹具应有 3 个视频，实际 %d", len(videos))
	}
	// 建两个列表：第一个含 2 条且故意反序加入，验证导出按 sort_order 排序
	p1 := createPlaylistViaAPI(t, ts, "片单一")
	p2 := createPlaylistViaAPI(t, ts, "片单二")
	doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+p1+"/items", map[string]string{"videoId": videos[1].ID})
	doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+p1+"/items", map[string]string{"videoId": videos[0].ID})
	doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+p2+"/items", map[string]string{"videoId": videos[2].ID})

	status, _, body := downloadPlaylistsJSON(t, ts)
	if status != http.StatusOK {
		t.Fatalf("导出应 200，实际 %d", status)
	}
	doc := decodeExport(t, body)
	pls := doc["playlists"].([]any)
	if len(pls) != 2 {
		t.Fatalf("应导出 2 个列表，实际 %d", len(pls))
	}
	first := pls[0].(map[string]any)
	if first["name"] != "片单一" {
		t.Fatalf("列表名称不符: %v", first["name"])
	}
	items := first["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("片单一应有 2 条，实际 %d", len(items))
	}
	// 顺序应为 videos[1]、videos[0]（按加入顺序即 sort_order）
	wantOrder := []string{videos[1].ID, videos[0].ID}
	for i, want := range wantOrder {
		it := items[i].(map[string]any)
		if it["videoId"] != want {
			t.Fatalf("第 %d 条 videoId 不符: got %v want %s", i, it["videoId"], want)
		}
		if int(it["sortOrder"].(float64)) != i {
			t.Fatalf("第 %d 条 sortOrder 应为 %d", i, i)
		}
		if it["relPath"] == "" {
			t.Fatalf("存在的视频 relPath 不应为空")
		}
	}
}

func TestExportPlaylistsKeepsDanglingItem(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	pl, err := srv.store.CreatePlaylist("含悬空条目")
	if err != nil {
		t.Fatalf("建列表失败: %v", err)
	}
	ghostID := "ffffffffffffffff" // 媒体库中必然不存在的 16 位 hex
	if n, err := srv.store.AddPlaylistItems(pl.ID, []string{ghostID}); err != nil || n != 1 {
		t.Fatalf("写入悬空条目失败: n=%d err=%v", n, err)
	}

	status, _, body := downloadPlaylistsJSON(t, ts)
	if status != http.StatusOK {
		t.Fatalf("导出应 200，实际 %d", status)
	}
	doc := decodeExport(t, body)
	items := doc["playlists"].([]any)[0].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("悬空条目应保留，实际条目数 %d", len(items))
	}
	it := items[0].(map[string]any)
	if it["videoId"] != ghostID || it["relPath"] != "" {
		t.Fatalf("悬空条目应保留 ID 且 relPath 为空: %v", it)
	}
}

func TestExportPlaylistsRequiresStore(t *testing.T) {
	_, ts := newTestServer(t, "")
	status, _, _ := downloadPlaylistsJSON(t, ts)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("未启用持久化应 503，实际 %d", status)
	}
}

func TestExportPlaylistsRequiresAuth(t *testing.T) {
	_, ts := newTestServer(t, "pass123")
	status, _, _ := downloadPlaylistsJSON(t, ts)
	if status != http.StatusUnauthorized {
		t.Fatalf("未登录访问 API 应 401，实际 %d", status)
	}
}

func TestImportPlaylistsRoundTrip(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	videos := srv.library.List()
	p1 := createPlaylistViaAPI(t, ts, "往返片单一")
	p2 := createPlaylistViaAPI(t, ts, "往返片单二")
	doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+p1+"/items", map[string]string{"videoId": videos[0].ID})
	doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+p1+"/items", map[string]string{"videoId": videos[1].ID})
	doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+p2+"/items", map[string]string{"videoId": videos[2].ID})

	_, _, exported := downloadPlaylistsJSON(t, ts)
	status, body := uploadPlaylistsJSON(t, ts, "playlists.json", exported)
	if status != http.StatusOK {
		t.Fatalf("导入应 200，实际 %d body=%v", status, body)
	}
	if int(body["created"].(float64)) != 2 {
		t.Fatalf("应新建 2 个列表，实际 %v", body["created"])
	}
	if int(body["addedItems"].(float64)) != 3 {
		t.Fatalf("应写入 3 个条目，实际 %v", body["addedItems"])
	}
	if int(body["skippedItems"].(float64)) != 0 {
		t.Fatalf("不应有跳过，实际 %v", body["skippedItems"])
	}
	// 列表总数由 2 翻倍为 4，且新列表 ID 与旧列表不同
	summaries, err := srv.store.ListPlaylists()
	if err != nil {
		t.Fatalf("查询列表失败: %v", err)
	}
	if len(summaries) != 4 {
		t.Fatalf("导入后应有 4 个列表，实际 %d", len(summaries))
	}
	idSet := map[string]bool{}
	for _, sm := range summaries {
		idSet[sm.ID] = true
	}
	if !idSet[p1] || !idSet[p2] {
		t.Fatalf("原有列表不应被改动")
	}
}

func TestImportPlaylistsPreservesItemOrder(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	videos := srv.library.List()
	// 手工构造反序条目文件，导入后顺序必须与文件数组一致
	doc := map[string]any{
		"version": 1,
		"playlists": []any{
			map[string]any{
				"name": "顺序校验",
				"items": []any{
					map[string]any{"videoId": videos[2].ID, "relPath": videos[2].RelPath, "sortOrder": 0},
					map[string]any{"videoId": videos[0].ID, "relPath": videos[0].RelPath, "sortOrder": 1},
					map[string]any{"videoId": videos[1].ID, "relPath": videos[1].RelPath, "sortOrder": 2},
				},
			},
		},
	}
	raw, _ := json.Marshal(doc)
	status, body := uploadPlaylistsJSON(t, ts, "order.json", raw)
	if status != http.StatusOK {
		t.Fatalf("导入应 200，实际 %d body=%v", status, body)
	}
	newID := body["playlists"].([]any)[0].(map[string]any)["id"].(string)
	pl, err := srv.store.GetPlaylist(newID)
	if err != nil || pl == nil {
		t.Fatalf("读取新列表失败: %v", err)
	}
	want := []string{videos[2].ID, videos[0].ID, videos[1].ID}
	if len(pl.Items) != 3 {
		t.Fatalf("应有 3 条，实际 %d", len(pl.Items))
	}
	for i, vid := range want {
		if pl.Items[i].VideoID != vid {
			t.Fatalf("第 %d 条顺序不符: got %s want %s", i, pl.Items[i].VideoID, vid)
		}
	}
}

func TestImportPlaylistsAlwaysCreatesNew(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	createPlaylistViaAPI(t, ts, "重名列表")
	doc := map[string]any{
		"version":   1,
		"playlists": []any{map[string]any{"name": "重名列表", "items": []any{}}},
	}
	raw, _ := json.Marshal(doc)

	// 同一文件连续导入两次，每次都是新建
	for i := 0; i < 2; i++ {
		status, body := uploadPlaylistsJSON(t, ts, "dup.json", raw)
		if status != http.StatusOK {
			t.Fatalf("第 %d 次导入应 200，实际 %d", i+1, status)
		}
		if int(body["created"].(float64)) != 1 {
			t.Fatalf("每次应新建 1 个，实际 %v", body["created"])
		}
	}
	summaries, _ := srv.store.ListPlaylists()
	if len(summaries) != 3 {
		t.Fatalf("两次导入后应有 3 个同名列表，实际 %d", len(summaries))
	}
	ids := map[string]bool{}
	for _, sm := range summaries {
		if sm.Name != "重名列表" || ids[sm.ID] {
			t.Fatalf("应全部为同名且 ID 互不相同: %+v", sm)
		}
		ids[sm.ID] = true
	}
}

func TestImportPlaylistsSkipsMissingVideos(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	videos := srv.library.List()
	doc := map[string]any{
		"version": 1,
		"playlists": []any{
			map[string]any{
				"name": "混合条目",
				"items": []any{
					map[string]any{"videoId": videos[0].ID, "relPath": videos[0].RelPath, "sortOrder": 0},
					map[string]any{"videoId": "deadbeefdeadbeef", "relPath": "不存在/影片.mkv", "sortOrder": 1},
				},
			},
		},
	}
	raw, _ := json.Marshal(doc)
	status, body := uploadPlaylistsJSON(t, ts, "mix.json", raw)
	if status != http.StatusOK {
		t.Fatalf("含缺失视频也应 200，实际 %d body=%v", status, body)
	}
	if int(body["created"].(float64)) != 1 || int(body["addedItems"].(float64)) != 1 {
		t.Fatalf("应建 1 列表写 1 条: %v", body)
	}
	if int(body["skippedItems"].(float64)) != 1 {
		t.Fatalf("应跳过 1 条，实际 %v", body["skippedItems"])
	}
	skipped := body["skippedVideos"].([]any)
	if len(skipped) != 1 {
		t.Fatalf("skippedVideos 应有 1 条明细，实际 %d", len(skipped))
	}
	detail := skipped[0].(map[string]any)
	if detail["videoId"] != "deadbeefdeadbeef" || detail["playlistName"] != "混合条目" {
		t.Fatalf("跳过明细不符: %v", detail)
	}
	// 存在的条目仍正常落库
	summaries, _ := srv.store.ListPlaylists()
	pl, _ := srv.store.GetPlaylist(summaries[0].ID)
	if len(pl.Items) != 1 || pl.Items[0].VideoID != videos[0].ID {
		t.Fatalf("存在的条目应正常写入: %+v", pl.Items)
	}
}

func TestImportPlaylistsRelPathFallback(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	videos := srv.library.List()
	target := videos[0]
	// videoId 故意写错，但 relPath 与本机真实文件一致，应经兜底 map 命中
	doc := map[string]any{
		"version": 1,
		"playlists": []any{
			map[string]any{
				"name":  "兜底命中",
				"items": []any{map[string]any{"videoId": "0000000000000000", "relPath": target.RelPath, "sortOrder": 0}},
			},
		},
	}
	raw, _ := json.Marshal(doc)
	status, body := uploadPlaylistsJSON(t, ts, "fallback.json", raw)
	if status != http.StatusOK {
		t.Fatalf("导入应 200，实际 %d body=%v", status, body)
	}
	if int(body["addedItems"].(float64)) != 1 || int(body["skippedItems"].(float64)) != 0 {
		t.Fatalf("relPath 兜底应命中写入: %v", body)
	}
	summaries, _ := srv.store.ListPlaylists()
	pl, _ := srv.store.GetPlaylist(summaries[0].ID)
	if len(pl.Items) != 1 || pl.Items[0].VideoID != target.ID {
		t.Fatalf("兜底应解析为真实 ID %s，实际 %+v", target.ID, pl.Items)
	}
}

func TestImportPlaylistsMissingFileField(t *testing.T) {
	_, ts := newPlaylistTestServer(t)
	if status := uploadEmptyMultipart(t, ts); status != http.StatusBadRequest {
		t.Fatalf("缺 file 字段应 400，实际 %d", status)
	}
}

func TestImportPlaylistsEmptyFile(t *testing.T) {
	_, ts := newPlaylistTestServer(t)
	status, body := uploadPlaylistsJSON(t, ts, "empty.json", nil)
	if status != http.StatusBadRequest || body["error"] == "" {
		t.Fatalf("空文件应 400 且带错误信息，实际 %d %v", status, body)
	}
}

func TestImportPlaylistsRejectsOversize(t *testing.T) {
	_, ts := newPlaylistTestServer(t)
	big := make([]byte, maxPlaylistImport+1)
	status, body := uploadPlaylistsJSON(t, ts, "big.json", big)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限文件应 413，实际 %d", status)
	}
	if body["error"] == "" {
		t.Fatalf("413 应带错误信息")
	}
}

func TestImportPlaylistsRejectsBadJSON(t *testing.T) {
	_, ts := newPlaylistTestServer(t)

	// 非 JSON 文本
	status, body := uploadPlaylistsJSON(t, ts, "bad.txt", []byte("这不是 JSON"))
	if status != http.StatusBadRequest || body["error"] == "" {
		t.Fatalf("坏 JSON 应 400，实际 %d %v", status, body)
	}

	// 含未知顶层字段
	withUnknown := []byte(`{"version":1,"playlists":[],"unexpected":true}`)
	status, body = uploadPlaylistsJSON(t, ts, "unknown.json", withUnknown)
	if status != http.StatusBadRequest {
		t.Fatalf("未知字段应 400，实际 %d body=%v", status, body)
	}

	// 合法 JSON 后尾随第二个值
	trailing := []byte(`{"version":1,"playlists":[]}{"x":1}`)
	status, _ = uploadPlaylistsJSON(t, ts, "trailing.json", trailing)
	if status != http.StatusBadRequest {
		t.Fatalf("尾随 JSON 值应 400，实际 %d", status)
	}
}

func TestImportPlaylistsValidationCases(t *testing.T) {
	_, ts := newPlaylistTestServer(t)

	cases := []struct {
		name string
		raw  []byte
	}{
		{"版本不支持", []byte(`{"version":2,"playlists":[{"name":"x","items":[]}]}`)},
		{"列表为空", []byte(`{"version":1,"playlists":[]}`)},
		{"空白名称", []byte(`{"version":1,"playlists":[{"name":"   ","items":[]}]}`)},
	}
	for _, tc := range cases {
		status, body := uploadPlaylistsJSON(t, ts, "case.json", tc.raw)
		if status != http.StatusBadRequest || body["error"] == "" {
			t.Fatalf("%s 应 400 带错误信息，实际 %d %v", tc.name, status, body)
		}
	}

	// 名称超 100 字符
	longName := strings.Repeat("长", maxImportNameLen+1)
	over, _ := json.Marshal(map[string]any{
		"version":   1,
		"playlists": []any{map[string]any{"name": longName, "items": []any{}}},
	})
	status, _ := uploadPlaylistsJSON(t, ts, "long.json", over)
	if status != http.StatusBadRequest {
		t.Fatalf("超长名称应 400，实际 %d", status)
	}
}

func TestImportPlaylistsAllowsEmptyItems(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	raw := []byte(`{"version":1,"playlists":[{"name":"空列表","items":[]}]}`)
	status, body := uploadPlaylistsJSON(t, ts, "empty-items.json", raw)
	if status != http.StatusOK {
		t.Fatalf("空 items 是合法导入，实际 %d body=%v", status, body)
	}
	if int(body["created"].(float64)) != 1 || int(body["addedItems"].(float64)) != 0 {
		t.Fatalf("应建 1 列表 0 条目: %v", body)
	}
	summaries, _ := srv.store.ListPlaylists()
	if len(summaries) != 1 || summaries[0].ItemCount != 0 {
		t.Fatalf("应存在 1 个空列表: %+v", summaries)
	}
	// 无跳过时 skippedVideos 序列化为 [] 而非 null
	if _, ok := body["skippedVideos"].([]any); !ok {
		t.Fatalf("skippedVideos 应为空数组，实际 %v", body["skippedVideos"])
	}
}

func TestImportPlaylistsRequiresStoreAndAuth(t *testing.T) {
	// 未启用持久化 → 503
	_, tsNoStore := newTestServer(t, "")
	status, _ := uploadPlaylistsJSON(t, tsNoStore, "p.json",
		[]byte(`{"version":1,"playlists":[]}`))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("未启用持久化应 503，实际 %d", status)
	}

	// 开启认证但未登录 → 401（认证中间件先于 requireStore 拦截）
	_, tsAuth := newTestServer(t, "pass123")
	status, _ = uploadPlaylistsJSON(t, tsAuth, "p.json",
		[]byte(`{"version":1,"playlists":[]}`))
	if status != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，实际 %d", status)
	}
}
