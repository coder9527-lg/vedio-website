package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// newPlaylistTestServer 构造带真实 SQLite store、含一部两集剧 + 一部电影的测试服务。
func newPlaylistTestServer(t *testing.T) (*Server, *httptest.Server) {
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

// createPlaylistViaAPI 通过接口创建播放列表并返回其 ID。
func createPlaylistViaAPI(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	status, body := doJSON(t, ts, http.MethodPost, "/api/v1/playlists", map[string]string{"name": name})
	if status != http.StatusCreated {
		t.Fatalf("创建播放列表应 201，实际 %d body=%v", status, body)
	}
	pl, ok := body["playlist"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 playlist: %v", body)
	}
	id, _ := pl["id"].(string)
	if id == "" {
		t.Fatalf("播放列表 ID 为空: %v", body)
	}
	return id
}

func TestPlaylistCRUDLifecycle(t *testing.T) {
	_, ts, _ := newPersistTestServer(t)

	// 初始列表为空
	status, body := doJSON(t, ts, http.MethodGet, "/api/v1/playlists", nil)
	if status != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d", status)
	}
	if pls, _ := body["playlists"].([]any); len(pls) != 0 {
		t.Fatalf("初始应为空列表: %v", body)
	}

	// 创建
	pid := createPlaylistViaAPI(t, ts, "周末合集")

	// 列表摘要含 1 条
	status, body = doJSON(t, ts, http.MethodGet, "/api/v1/playlists", nil)
	pls, _ := body["playlists"].([]any)
	if len(pls) != 1 {
		t.Fatalf("摘要应有 1 条: %v", body)
	}
	summary := pls[0].(map[string]any)
	if summary["name"] != "周末合集" || summary["itemCount"].(float64) != 0 {
		t.Fatalf("摘要内容不符: %v", summary)
	}

	// 详情
	status, body = doJSON(t, ts, http.MethodGet, "/api/v1/playlists/"+pid, nil)
	if status != http.StatusOK {
		t.Fatalf("详情应 200，实际 %d", status)
	}
	if items, _ := body["items"].([]any); len(items) != 0 {
		t.Fatalf("新列表应无条目: %v", body)
	}

	// 重命名
	status, _ = doJSON(t, ts, http.MethodPatch, "/api/v1/playlists/"+pid,
		map[string]string{"name": "假日合集"})
	if status != http.StatusOK {
		t.Fatalf("重命名应 200，实际 %d", status)
	}
	status, body = doJSON(t, ts, http.MethodGet, "/api/v1/playlists/"+pid, nil)
	if got := body["playlist"].(map[string]any)["name"]; got != "假日合集" {
		t.Fatalf("重命名未生效: %v", got)
	}

	// 删除
	status, _ = doJSON(t, ts, http.MethodDelete, "/api/v1/playlists/"+pid, nil)
	if status != http.StatusOK {
		t.Fatalf("删除应 200，实际 %d", status)
	}
	status, _ = doJSON(t, ts, http.MethodGet, "/api/v1/playlists/"+pid, nil)
	if status != http.StatusNotFound {
		t.Fatalf("删除后详情应 404，实际 %d", status)
	}
}

func TestPlaylistValidationAndNotFound(t *testing.T) {
	_, ts, _ := newPersistTestServer(t)

	// 空白名称 → 400
	status, body := doJSON(t, ts, http.MethodPost, "/api/v1/playlists",
		map[string]string{"name": "   "})
	if status != http.StatusBadRequest {
		t.Fatalf("空白名称应 400，实际 %d", status)
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Fatalf("400 应含错误信息: %v", body)
	}

	// 操作不存在的列表 → 404（移除条目按设计幂等，不在此列，单独验证）
	missing := "/api/v1/playlists/deadbeef"
	for _, c := range []struct {
		method, path string
		payload      any
	}{
		{http.MethodGet, missing, nil},
		{http.MethodPatch, missing, map[string]string{"name": "x"}},
		{http.MethodDelete, missing, nil},
		{http.MethodPost, missing + "/items", map[string]string{"videoId": "x"}},
		{http.MethodPost, missing + "/items/x/move", map[string]string{"direction": "up"}},
	} {
		status, _ = doJSON(t, ts, c.method, c.path, c.payload)
		if status != http.StatusNotFound {
			t.Fatalf("%s %s 应 404，实际 %d", c.method, c.path, status)
		}
	}
	// 移除不存在列表的条目：幂等 200
	if status, _ := doJSON(t, ts, http.MethodDelete, missing+"/items/x", nil); status != http.StatusOK {
		t.Fatalf("移除不存在列表的条目应幂等 200，实际 %d", status)
	}

	// 加入不存在的视频 → 404
	pid := createPlaylistViaAPI(t, ts, "临时")
	status, _ = doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+pid+"/items",
		map[string]string{"videoId": "no-such-video"})
	if status != http.StatusNotFound {
		t.Fatalf("不存在视频应 404，实际 %d", status)
	}
}

func TestPlaylistAddDedupeRemoveAndMove(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	videos := srv.library.List()
	if len(videos) != 3 {
		t.Fatalf("夹具应有 3 个视频，实际 %d", len(videos))
	}
	v1, v2, v3 := videos[0].ID, videos[1].ID, videos[2].ID
	pid := createPlaylistViaAPI(t, ts, "排序测试")

	add := func(vid string) (int, map[string]any) {
		return doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+pid+"/items",
			map[string]string{"videoId": vid})
	}

	// 依次加入 v1 v2 v3
	if status, body := add(v1); status != http.StatusCreated || body["added"] != true {
		t.Fatalf("首次加入应 201 added=true: %d %v", status, body)
	}
	add(v2)
	add(v3)

	// 重复加入幂等：200 added=false
	if status, body := add(v2); status != http.StatusOK || body["added"] != false {
		t.Fatalf("重复加入应 200 added=false: %d %v", status, body)
	}

	// 详情顺序 v1 v2 v3
	order := func() []string {
		_, body := doJSON(t, ts, http.MethodGet, "/api/v1/playlists/"+pid, nil)
		items, _ := body["items"].([]any)
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.(map[string]any)["videoId"].(string))
		}
		return out
	}
	if got := order(); len(got) != 3 || got[0] != v1 || got[1] != v2 || got[2] != v3 {
		t.Fatalf("初始顺序不符: %v", got)
	}

	// v3 上移 → v1 v3 v2
	status, body := doJSON(t, ts, http.MethodPost,
		"/api/v1/playlists/"+pid+"/items/"+v3+"/move",
		map[string]string{"direction": "up"})
	if status != http.StatusOK {
		t.Fatalf("上移应 200，实际 %d", status)
	}
	moved, _ := body["items"].([]any)
	if len(moved) != 3 || moved[1].(map[string]any)["videoId"] != v3 {
		t.Fatalf("上移结果不符: %v", body)
	}
	if got := order(); got[1] != v3 {
		t.Fatalf("详情顺序未更新: %v", got)
	}

	// 非法 direction → 400
	if status, _ := doJSON(t, ts, http.MethodPost,
		"/api/v1/playlists/"+pid+"/items/"+v3+"/move",
		map[string]string{"direction": "sideways"}); status != http.StatusBadRequest {
		t.Fatalf("非法 direction 应 400，实际 %d", status)
	}

	// 移动不存在的条目 → 404
	if status, _ := doJSON(t, ts, http.MethodPost,
		"/api/v1/playlists/"+pid+"/items/ghost/move",
		map[string]string{"direction": "up"}); status != http.StatusNotFound {
		t.Fatalf("移动不存在条目应 404，实际 %d", status)
	}

	// 移除 v3，剩余 v1 v2 且重新编号
	if status, _ := doJSON(t, ts, http.MethodDelete,
		"/api/v1/playlists/"+pid+"/items/"+v3, nil); status != http.StatusOK {
		t.Fatalf("移除应 200，实际 %d", status)
	}
	if got := order(); len(got) != 2 || got[0] != v1 || got[1] != v2 {
		t.Fatalf("移除后顺序不符: %v", got)
	}
	// 再次移除幂等，不报错
	if status, _ := doJSON(t, ts, http.MethodDelete,
		"/api/v1/playlists/"+pid+"/items/"+v3, nil); status != http.StatusOK {
		t.Fatalf("重复移除应幂等 200，实际 %d", status)
	}
}

func TestPlaylistShowAddDedupesInOrder(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	shows := srv.library.ListShows()
	if len(shows) != 1 {
		t.Fatalf("夹具应有 1 部剧，实际 %d", len(shows))
	}
	show := shows[0]
	pid := createPlaylistViaAPI(t, ts, "整剧")

	// 整剧一键加入：两集按季集顺序
	status, body := doJSON(t, ts, http.MethodPost, "/api/v1/shows/"+show.ID+"/playlist",
		map[string]string{"playlistId": pid})
	if status != http.StatusCreated {
		t.Fatalf("整剧加入应 201，实际 %d body=%v", status, body)
	}
	if body["added"].(float64) != 2 || body["skipped"].(float64) != 0 || body["total"].(float64) != 2 {
		t.Fatalf("整剧加入计数不符: %v", body)
	}

	// 详情顺序与剧集季集序一致
	_, detail := doJSON(t, ts, http.MethodGet, "/api/v1/playlists/"+pid, nil)
	items, _ := detail["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("应有 2 个条目: %v", detail)
	}
	wantIDs := make([]string, 0, 2)
	for _, season := range show.Seasons {
		for _, ep := range season.Episodes {
			wantIDs = append(wantIDs, ep.Video.ID)
		}
	}
	for i, want := range wantIDs {
		if items[i].(map[string]any)["videoId"] != want {
			t.Fatalf("第 %d 个条目顺序不符，want=%s detail=%v", i, want, items)
		}
	}

	// 再加入一次：全部跳过，幂等
	status, body = doJSON(t, ts, http.MethodPost, "/api/v1/shows/"+show.ID+"/playlist",
		map[string]string{"playlistId": pid})
	if status != http.StatusOK || body["added"].(float64) != 0 || body["skipped"].(float64) != 2 {
		t.Fatalf("重复整剧加入应全跳过: %d %v", status, body)
	}

	// 回归：带持久化的剧集页会渲染内联脚本，showID 不得被 printf 二次转义混入字面引号
	page := get(t, ts, "/shows/"+show.ID)
	pageHTML := readAll(t, page)
	page.Body.Close()
	if !strings.Contains(pageHTML, `const showID = "`+show.ID+`"`) ||
		strings.Contains(pageHTML, `const showID = "\"`) {
		t.Error("剧集页 showID 渲染异常，存在 printf 百分号q 二次转义")
	}

	// 不存在的剧集 → 404
	status, _ = doJSON(t, ts, http.MethodPost, "/api/v1/shows/ghost/playlist",
		map[string]string{"playlistId": pid})
	if status != http.StatusNotFound {
		t.Fatalf("不存在剧集应 404，实际 %d", status)
	}
}

func TestVideoPlaylistMemberships(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	videoID := srv.library.List()[0].ID
	p1 := createPlaylistViaAPI(t, ts, "列表一")
	p2 := createPlaylistViaAPI(t, ts, "列表二")

	doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+p1+"/items",
		map[string]string{"videoId": videoID})
	doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+p2+"/items",
		map[string]string{"videoId": videoID})

	status, body := doJSON(t, ts, http.MethodGet, "/api/v1/videos/"+videoID+"/playlists", nil)
	if status != http.StatusOK {
		t.Fatalf("成员关系应 200，实际 %d", status)
	}
	members, _ := body["playlists"].([]any)
	if len(members) != 2 {
		t.Fatalf("该视频应属于 2 个列表: %v", body)
	}

	// 不存在的视频 → 404
	status, _ = doJSON(t, ts, http.MethodGet, "/api/v1/videos/ghost/playlists", nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在视频应 404，实际 %d", status)
	}
}

func TestWatchPlaylistAutoNextContext(t *testing.T) {
	srv, ts := newPlaylistTestServer(t)
	shows := srv.library.ListShows()
	show := shows[0]
	// 取两集 ID（季集升序）
	var ep1, ep2, movieID string
	for _, season := range show.Seasons {
		for _, ep := range season.Episodes {
			if ep1 == "" {
				ep1 = ep.Video.ID
			} else {
				ep2 = ep.Video.ID
			}
		}
	}
	for _, v := range srv.library.List() {
		if !srv.library.IsShowEpisode(v.ID) {
			movieID = v.ID
		}
	}
	pid := createPlaylistViaAPI(t, ts, "连播")
	doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+pid+"/items",
		map[string]string{"videoId": ep1})
	doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+pid+"/items",
		map[string]string{"videoId": ep2})
	doJSON(t, ts, http.MethodPost, "/api/v1/playlists/"+pid+"/items",
		map[string]string{"videoId": movieID})

	getWatch := func(path string) string {
		resp := get(t, ts, path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s 应 200，实际 %d", path, resp.StatusCode)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(raw)
	}

	// 第一项：连播卡片指向第二项且带 playlist 参数
	html := getWatch("/watch/" + ep1 + "?playlist=" + pid)
	if !strings.Contains(html, "/watch/"+ep2+"?playlist="+pid) {
		t.Fatal("首项的连播 URL 应指向第二项并带 playlist 参数")
	}

	// 末项（电影）：列表连播停止，不出现 auto-next 卡片
	html = getWatch("/watch/" + movieID + "?playlist=" + pid)
	if strings.Contains(html, "data-auto-next") {
		t.Fatal("列表末项不应出现连播卡片")
	}

	// 已删除列表的旧链接：静默回退剧集连播（ep1 仍有下一集 ep2，且不带 playlist 参数）
	doJSON(t, ts, http.MethodDelete, "/api/v1/playlists/"+pid, nil)
	html = getWatch("/watch/" + ep1 + "?playlist=" + pid)
	if !strings.Contains(html, "/watch/"+ep2) {
		t.Fatal("列表失效后应回退剧集连播到 ep2")
	}
	if strings.Contains(html, "?playlist=") {
		t.Fatal("回退的剧集连播 URL 不应带 playlist 参数")
	}
}

// TestPlaylistManageFilterInjection 管理弹窗渲染条目筛选条及前端筛选接线标记。
// 仅校验专属 id/class 是否出现在 HTML 中，实时过滤交互属浏览器范畴，不在此覆盖。
func TestPlaylistManageFilterInjection(t *testing.T) {
	_, ts := newPlaylistTestServer(t)
	code, html := getHTML(t, ts, "/")
	if code != http.StatusOK {
		t.Fatalf("首页状态码 = %d", code)
	}
	containsAll(t, html,
		`class="pl-filter-bar"`,
		`id="pl-filter-input"`,
		`id="pl-filter-count"`,
		// 前端接线标记：缓存全量条目与关键词状态、纯前端过滤函数
		`plCurrentItems`,
		`plFilterKeyword`,
		`plFilteredItems`,
	)
}
