package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// dataHealthGhostID 是测试中统一使用的悬空视频 ID（不可能出现在三视频夹具中）。
const dataHealthGhostID = "ghost-orphan-id"

// seedGhostRecords 直接操作测试库，向七张表各写入一条悬空记录，
// 并返回期望的体检报告（每表 1 条孤儿）。标签关联独占一个标签，用于验证空标签回收。
func seedGhostRecords(t *testing.T, srv *Server) store.OrphanReport {
	t.Helper()
	now := time.Now().UTC()
	db := srv.store.DB()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("造数失败: %v SQL=%s", err, query)
		}
	}
	exec(`INSERT INTO progress(video_id, position, duration, watch_time_seconds, play_count, completed, updated_at)
	      VALUES(?, 0, 0, 0, 0, 0, ?)`, dataHealthGhostID, now)
	exec(`INSERT INTO favorites(video_id, added_at) VALUES(?, ?)`, dataHealthGhostID, now)
	exec(`INSERT INTO playlists(id, name, created_at, updated_at) VALUES('pl-ghost', '孤儿列表', ?, ?)`, now, now)
	exec(`INSERT INTO playlist_item(playlist_id, video_id, sort_order, added_at) VALUES('pl-ghost', ?, 0, ?)`,
		dataHealthGhostID, now)
	exec(`INSERT INTO bookmarks(video_id, position, note, created_at) VALUES(?, 12, '', ?)`,
		dataHealthGhostID, now)
	exec(`INSERT INTO skip_settings(video_id, intro_seconds, outro_seconds, enabled, updated_at)
	      VALUES(?, 10, 20, 1, ?)`, dataHealthGhostID, now)
	exec(`INSERT INTO watch_events(video_id, started_at, updated_at, watch_seconds, position, duration)
	      VALUES(?, ?, ?, 30, 12, 100)`, dataHealthGhostID, now, now)
	if _, _, err := srv.store.SetVideoTag(dataHealthGhostID, "孤儿专属标签"); err != nil {
		t.Fatalf("写入孤儿标签失败: %v", err)
	}
	return store.OrphanReport{
		Progress: 1, Favorites: 1, PlaylistItem: 1, Bookmarks: 1,
		SkipSettings: 1, WatchEvents: 1, VideoTag: 1,
	}
}

// cleanViaAPI 调按表清理接口，返回状态码与解析后的 JSON。
func cleanViaAPI(t *testing.T, ts *httptest.Server, table string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, ts, http.MethodPost, "/api/v1/data-health/clean", map[string]string{"table": table})
}

// fetchHealthPage 拉取体检页 HTML，返回状态码与正文。
func fetchHealthPage(t *testing.T, ts *httptest.Server) (int, string) {
	t.Helper()
	resp := get(t, ts, "/data/health")
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取体检页失败: %v", err)
	}
	return resp.StatusCode, string(raw)
}

func TestDataHealthRequiresPersistence(t *testing.T) {
	_, ts := newTestServer(t, "")

	resp := get(t, ts, "/data/health")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未启用持久化时页面应 503，实际 %d", resp.StatusCode)
	}
	for _, path := range []string{"/api/v1/data-health/clean", "/api/v1/data-health/clean-all"} {
		status, body := doJSON(t, ts, http.MethodPost, path, map[string]string{"table": "progress"})
		if status != http.StatusServiceUnavailable {
			t.Fatalf("%s 未启用持久化应 503，实际 %d body=%v", path, status, body)
		}
	}
}

func TestDataHealthRequiresAuth(t *testing.T) {
	_, ts := newTestServer(t, "pass123")

	// 页面请求未登录：302 跳登录
	resp := get(t, ts, "/data/health")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("未登录访问页面应 302 跳登录，实际 %d loc=%s",
			resp.StatusCode, resp.Header.Get("Location"))
	}
	// API 未登录：401 JSON
	for _, path := range []string{"/api/v1/data-health/clean", "/api/v1/data-health/clean-all"} {
		status, body := doJSON(t, ts, http.MethodPost, path, map[string]string{"table": "progress"})
		if status != http.StatusUnauthorized {
			t.Fatalf("%s 未登录应 401，实际 %d body=%v", path, status, body)
		}
	}
}

func TestDataHealthHealthyPage(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)

	status, html := fetchHealthPage(t, ts)
	if status != http.StatusOK {
		t.Fatalf("健康库体检页应 200，实际 %d", status)
	}
	// 七个分区按钮均在，且全部为禁用态；专属 class 不依赖中文文案判空
	if strings.Count(html, `class="btn-secondary btn-danger dh-clean-btn"`) != 7 {
		t.Fatalf("应渲染 7 个分区清理按钮，实际出现 %d 次",
			strings.Count(html, "dh-clean-btn"))
	}
	if strings.Count(html, "disabled") < 8 { // 7 个分区按钮 + 1 个一键清理
		t.Fatalf("无孤儿时清理按钮应全部禁用，HTML=%s", html)
	}
	if !strings.Contains(html, `data-table="progress"`) {
		t.Fatal("页面缺少 progress 分区标识")
	}
	_ = srv
}

func TestDataHealthReportCountsAndSamples(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	want := seedGhostRecords(t, srv)

	status, html := fetchHealthPage(t, ts)
	if status != http.StatusOK {
		t.Fatalf("体检页应 200，实际 %d", status)
	}
	// 库视频总数为 3（两集剧 + 一部电影），非空库不显示警示条
	if !strings.Contains(html, `data-library-total="3"`) {
		t.Fatalf("页面应带库视频总数 3，HTML=%s", html)
	}
	if strings.Contains(html, "dh-warning") {
		t.Fatal("非空库不应显示空库警示条")
	}
	// 每个分区按钮带 data-count，与造数期望一致
	for _, key := range []string{
		"progress", "favorites", "playlist_item", "bookmarks",
		"skip_settings", "watch_events", "video_tag",
	} {
		needle := `data-table="` + key + `" data-count="1"`
		if !strings.Contains(html, needle) {
			t.Fatalf("分区 %s 缺少计数为 1 的按钮，期望片段 %s", key, needle)
		}
	}
	// 样例 ID 展示在专属标签内
	if !strings.Contains(html, "dh-sample-id") || !strings.Contains(html, dataHealthGhostID) {
		t.Fatalf("页面应展示悬空 video_id 样例")
	}
	// 一键按钮启用且总数为 7
	if !strings.Contains(html, `data-total="7"`) {
		t.Fatalf("一键清理按钮应带孤儿总数 7")
	}
	if want.Total() != 7 {
		t.Fatalf("造数期望总数应为 7，实际 %d", want.Total())
	}
}

func TestDataHealthCleanByTableIsolation(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	seedGhostRecords(t, srv)

	// 先清理 progress，返回 removed=1
	status, body := cleanViaAPI(t, ts, "progress")
	if status != http.StatusOK || body["removed"] != float64(1) || body["table"] != "progress" {
		t.Fatalf("清理 progress 响应不符: status=%d body=%v", status, body)
	}
	// 再次体检：progress 按钮计数归零且禁用，其余 6 表仍为 1
	_, html := fetchHealthPage(t, ts)
	if !strings.Contains(html, `data-table="progress" data-count="0"`) {
		t.Fatal("progress 清理后计数应归零")
	}
	for _, key := range []string{"favorites", "playlist_item", "bookmarks", "skip_settings", "watch_events", "video_tag"} {
		if !strings.Contains(html, `data-table="`+key+`" data-count="1"`) {
			t.Fatalf("分区 %s 不应受 progress 清理影响", key)
		}
	}
	// 幂等：重复清理 progress 返回 removed=0
	status, body = cleanViaAPI(t, ts, "progress")
	if status != http.StatusOK || body["removed"] != float64(0) {
		t.Fatalf("重复清理应 removed=0: status=%d body=%v", status, body)
	}

	// 依次清完剩余六表，每次只影响目标表
	for _, key := range []string{"favorites", "playlist_item", "bookmarks", "skip_settings", "watch_events", "video_tag"} {
		status, body = cleanViaAPI(t, ts, key)
		if status != http.StatusOK || body["removed"] != float64(1) {
			t.Fatalf("清理 %s 应 removed=1: status=%d body=%v", key, status, body)
		}
	}
	_, html = fetchHealthPage(t, ts)
	if strings.Contains(html, `data-count="1"`) || !strings.Contains(html, `data-total="0"`) {
		t.Fatalf("全部清理后不应再有孤儿计数")
	}
	// 播放列表表头保留
	pls, err := srv.store.ListPlaylists()
	if err != nil || len(pls) != 1 {
		t.Fatalf("播放列表表头应保留: %+v err=%v", pls, err)
	}
	// video_tag 清理后独占空标签被回收
	tags, err := srv.store.ListTags()
	if err != nil {
		t.Fatalf("查询标签失败: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("孤儿专属标签应被回收，实际 %+v", tags)
	}
}

func TestDataHealthCleanAll(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	seedGhostRecords(t, srv)

	status, body := doJSON(t, ts, http.MethodPost, "/api/v1/data-health/clean-all", map[string]string{})
	if status != http.StatusOK || body["removed"] != float64(7) {
		t.Fatalf("一键清理应 removed=7: status=%d body=%v", status, body)
	}
	detail, ok := body["detail"].(map[string]any)
	if !ok || len(detail) != 7 {
		t.Fatalf("响应应含七表清理明细: %v", body)
	}
	// 用真实库 ID 集合复核：七表孤儿全部归零
	videos := srv.library.List()
	existing := make(map[string]bool, len(videos))
	for _, v := range videos {
		existing[v.ID] = true
	}
	report, err := srv.store.OrphanReport(existing)
	if err != nil || report.Total() != 0 {
		t.Fatalf("一键清理后应无孤儿: report=%+v err=%v", report, err)
	}
}

func TestDataHealthCleanInvalidTable(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	seedGhostRecords(t, srv)

	for _, table := range []string{"", "users; DROP TABLE progress--", "shares", "PLAYLISTS"} {
		status, body := cleanViaAPI(t, ts, table)
		if status != http.StatusBadRequest {
			t.Fatalf("非法表名 %q 应 400，实际 %d body=%v", table, status, body)
		}
		if _, ok := body["error"]; !ok {
			t.Fatalf("400 响应应含 error 字段: %v", body)
		}
	}
	// 库表完好：体检仍能返回 7 条孤儿
	existing, _ := srv.existingVideoIDs()
	report, err := srv.store.OrphanReport(existing)
	if err != nil || report.Total() != 7 {
		t.Fatalf("非法请求不应改动数据: report=%+v err=%v", report, err)
	}
}

func TestDataHealthCleanBadJSON(t *testing.T) {
	_, ts := newPlaylistTestServer(t)

	cases := []string{
		"not-json",
		`{"table":"progress","extra":1}`, // 未知字段
		`[1,2,3]`,                        // 非对象
	}
	for _, raw := range cases {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/data-health/clean",
			strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := noRedirectClient.Do(req)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("坏 JSON %q 应 400，实际 %d", raw, resp.StatusCode)
		}
	}
}

func TestDataHealthEmptyLibraryGuard(t *testing.T) {
	// 空媒体目录 + 真实 store：模拟媒体库为空的场景
	root := t.TempDir()
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("空库扫描失败: %v", err)
	}
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir() + "/empty.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(lib, thumbs, nil, st, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ts := newHTTPTestServer(t, srv.Handler())

	// 往 progress 与 bookmarks 各写一行，空库口径下它们全部算孤儿
	if err := st.SaveProgress("vid-a", store.ProgressUpdate{Position: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(
		`INSERT INTO bookmarks(video_id, position, note, created_at) VALUES('vid-b', 5, '', ?)`,
		time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	status, html := fetchHealthPage(t, ts)
	if status != http.StatusOK {
		t.Fatalf("空库体检页应 200，实际 %d", status)
	}
	if !strings.Contains(html, `data-library-total="0"`) || !strings.Contains(html, "dh-warning") {
		t.Fatalf("空库应显示 0 视频与警示条")
	}
	if !strings.Contains(html, `data-table="progress" data-count="1"`) ||
		!strings.Contains(html, `data-table="bookmarks" data-count="1"`) {
		t.Fatalf("空库计数应等于整表行数")
	}

	// 一键清理后两表清空
	status, body := doJSON(t, ts, http.MethodPost, "/api/v1/data-health/clean-all", map[string]string{})
	if status != http.StatusOK || body["removed"] != float64(2) {
		t.Fatalf("空库一键清理应 removed=2: status=%d body=%v", status, body)
	}
	report, err := st.OrphanReport(nil)
	if err != nil || report.Total() != 0 {
		t.Fatalf("清理后应全空: report=%+v err=%v", report, err)
	}
}

func TestDataHealthMethodRouting(t *testing.T) {
	_, ts := newPlaylistTestServer(t)

	// 清理接口只注册了 POST：Go 增强 ServeMux 对异方法的精确路径请求返回 404，
	// 断言该框架行为，防止后续误改成可被 GET 触发的清理动作
	for _, path := range []string{"/api/v1/data-health/clean", "/api/v1/data-health/clean-all"} {
		resp := get(t, ts, path)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s GET 应 404（仅 POST 路由），实际 %d", path, resp.StatusCode)
		}
	}
}
