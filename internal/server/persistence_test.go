package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// newHTTPTestServer 启动测试服务并注册关闭。
func newHTTPTestServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

// newPersistTestServer 构造一个带 SQLite 持久化、含一个测试视频的服务。
func newPersistTestServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	// 放一个测试视频文件，让库里有真实可引用的 ID
	videoPath := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(videoPath, []byte("fake-video"), 0o644); err != nil {
		t.Fatalf("写入测试视频失败: %v", err)
	}

	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	videos := lib.List()
	if len(videos) != 1 {
		t.Fatalf("期望 1 个视频，实际 %d", len(videos))
	}
	videoID := videos[0].ID

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
	ts := newHTTPTestServer(t, srv.Handler())
	return srv, ts, videoID
}

// doJSON 发送带 JSON body 的请求并解析响应。
func doJSON(t *testing.T, ts *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, ts.URL+path, reader)
	req.Header.Set("Content-Type", "application/json")
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", method, path, err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestProgressAPIFullLifecycle(t *testing.T) {
	_, ts, id := newPersistTestServer(t)

	// 初始无记录
	status, body := doJSON(t, ts, http.MethodGet, "/api/v1/videos/"+id+"/progress", nil)
	if status != http.StatusOK || body["progress"] != nil {
		t.Fatalf("初始进度应为 null, status=%d body=%v", status, body)
	}

	// 上报进度
	status, _ = doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]float64{"position": 30, "duration": 120})
	if status != http.StatusOK {
		t.Fatalf("上报进度应 200，实际 %d", status)
	}

	// 读回
	status, body = doJSON(t, ts, http.MethodGet, "/api/v1/videos/"+id+"/progress", nil)
	if status != http.StatusOK {
		t.Fatalf("查询进度应 200，实际 %d", status)
	}
	p, ok := body["progress"].(map[string]any)
	if !ok || p["position"] != 30.0 || p["duration"] != 120.0 {
		t.Fatalf("进度数据不符: %v", body["progress"])
	}

	// 列表接口
	status, body = doJSON(t, ts, http.MethodGet, "/api/v1/progress", nil)
	if status != http.StatusOK {
		t.Fatalf("进度列表应 200，实际 %d", status)
	}
	m, ok := body["progress"].(map[string]any)
	if !ok || len(m) != 1 {
		t.Fatalf("进度列表应有 1 条: %v", body["progress"])
	}

	// 清除
	status, _ = doJSON(t, ts, http.MethodDelete, "/api/v1/videos/"+id+"/progress", nil)
	if status != http.StatusOK {
		t.Fatalf("删除进度应 200，实际 %d", status)
	}
	status, body = doJSON(t, ts, http.MethodGet, "/api/v1/videos/"+id+"/progress", nil)
	if status != http.StatusOK || body["progress"] != nil {
		t.Fatalf("清除后进度应为 null, body=%v", body["progress"])
	}
}

func TestProgressRejectsNegativeAndBadJSON(t *testing.T) {
	_, ts, id := newPersistTestServer(t)

	status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]float64{"position": -1, "duration": 100})
	if status != http.StatusBadRequest {
		t.Fatalf("负 position 应 400，实际 %d", status)
	}

	// 非法 JSON
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/videos/"+id+"/progress",
		bytes.NewReader([]byte("{not-json")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，实际 %d", resp.StatusCode)
	}

	// 不存在的视频 → 404
	status, _ = doJSON(t, ts, http.MethodPut, "/api/v1/videos/nonexistent/progress",
		map[string]float64{"position": 1, "duration": 2})
	if status != http.StatusNotFound {
		t.Fatalf("不存在的视频应 404，实际 %d", status)
	}
}

func TestFavoriteAPIToggle(t *testing.T) {
	_, ts, id := newPersistTestServer(t)

	// 初始未收藏
	status, body := doJSON(t, ts, http.MethodGet, "/api/v1/videos/"+id+"/favorite", nil)
	if status != http.StatusOK || body["favorite"] != false {
		t.Fatalf("初始应未收藏: %v", body)
	}

	// 空 body → toggle，应变为 true
	status, body = doJSON(t, ts, http.MethodPost, "/api/v1/videos/"+id+"/favorite",
		map[string]any{})
	if status != http.StatusOK || body["favorite"] != true {
		t.Fatalf("toggle 后应收藏: status=%d body=%v", status, body)
	}

	// 显式指定 false
	status, body = doJSON(t, ts, http.MethodPost, "/api/v1/videos/"+id+"/favorite",
		map[string]bool{"favorite": false})
	if status != http.StatusOK || body["favorite"] != false {
		t.Fatalf("显式取消应未收藏: status=%d body=%v", status, body)
	}

	// 显式 true 后查列表
	doJSON(t, ts, http.MethodPost, "/api/v1/videos/"+id+"/favorite",
		map[string]bool{"favorite": true})
	status, body = doJSON(t, ts, http.MethodGet, "/api/v1/favorites", nil)
	if status != http.StatusOK {
		t.Fatalf("收藏列表应 200，实际 %d", status)
	}
	ids, ok := body["favorites"].([]any)
	if !ok || len(ids) != 1 || ids[0] != id {
		t.Fatalf("收藏列表内容不符: %v", body["favorites"])
	}
}

func TestPersistenceDisabledReturns503(t *testing.T) {
	// newTestServer 传入的 store 为 nil
	_, ts := newTestServer(t, "")

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/progress"},
		{http.MethodGet, "/api/v1/favorites"},
		{http.MethodPut, "/api/v1/videos/x/progress"},
		{http.MethodPost, "/api/v1/videos/x/favorite"},
		// 播放列表 10 条路由在持久化关闭时同样应 503
		{http.MethodGet, "/api/v1/playlists"},
		{http.MethodPost, "/api/v1/playlists"},
		{http.MethodGet, "/api/v1/playlists/x"},
		{http.MethodPatch, "/api/v1/playlists/x"},
		{http.MethodDelete, "/api/v1/playlists/x"},
		{http.MethodPost, "/api/v1/playlists/x/items"},
		{http.MethodDelete, "/api/v1/playlists/x/items/y"},
		{http.MethodPost, "/api/v1/playlists/x/items/y/move"},
		{http.MethodPost, "/api/v1/shows/x/playlist"},
		{http.MethodGet, "/api/v1/videos/x/playlists"},
	} {
		status, _ := doJSON(t, ts, c.method, c.path, map[string]any{})
		if status != http.StatusServiceUnavailable {
			t.Fatalf("%s %s 未启用持久化应 503，实际 %d", c.method, c.path, status)
		}
	}
}

func TestRescanPrunesOrphanRecords(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)

	// 给真实视频和一个"已删除"的假 ID 都写进度
	doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]float64{"position": 10, "duration": 100})
	if err := srv.store.SaveProgress("ghost", 5, 50); err != nil {
		t.Fatalf("写入孤儿进度失败: %v", err)
	}
	if err := srv.store.SetFavorite("ghost", true); err != nil {
		t.Fatalf("写入孤儿收藏失败: %v", err)
	}
	// 播放列表：真实视频 + 悬空 ghost 各占一个条目，表头本身应保留
	pl, err := srv.store.CreatePlaylist("重扫测试")
	if err != nil {
		t.Fatalf("创建播放列表失败: %v", err)
	}
	if _, err := srv.store.AddPlaylistItem(pl.ID, id); err != nil {
		t.Fatalf("加入真实视频失败: %v", err)
	}
	if _, err := srv.store.AddPlaylistItem(pl.ID, "ghost"); err != nil {
		t.Fatalf("加入悬空视频失败: %v", err)
	}

	// 触发重扫，应清掉 ghost、保留真实视频
	status, _ := doJSON(t, ts, http.MethodPost, "/api/v1/rescan", nil)
	if status != http.StatusOK {
		t.Fatalf("rescan 应 200，实际 %d", status)
	}
	if p, _ := srv.store.GetProgress("ghost"); p != nil {
		t.Fatal("重扫后孤儿进度应被清理")
	}
	if fav, _ := srv.store.IsFavorite("ghost"); fav {
		t.Fatal("重扫后孤儿收藏应被清理")
	}
	if p, _ := srv.store.GetProgress(id); p == nil {
		t.Fatal("真实视频的进度应保留")
	}

	// 播放列表：悬空条目被清理，真实条目与列表表头保留
	after, err := srv.store.GetPlaylist(pl.ID)
	if err != nil {
		t.Fatalf("重扫后列表表头应保留: %v", err)
	}
	if after == nil {
		t.Fatal("重扫后播放列表不应被删除")
	}
	if len(after.Items) != 1 || after.Items[0].VideoID != id {
		t.Fatalf("重扫后应只剩真实视频条目，实际 %+v", after.Items)
	}
}

// TestHomeShelfRows 验证首页横向行：继续观看按 progress 最近更新排序且过滤无效进度，
// 最近加入按 ModTime 倒序；非默认视图（带筛选参数）不渲染横向行。
func TestHomeShelfRows(t *testing.T) {
	srv, ts, videoID := newPersistTestServer(t)

	// 再造两个视频文件并重扫，库内共 3 个视频
	root := srv.library.Root()
	extra1 := filepath.Join(root, "b-movie.mp4")
	extra2 := filepath.Join(root, "c-show.mp4")
	if err := os.WriteFile(extra1, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extra2, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.library.Scan(); err != nil {
		t.Fatal(err)
	}
	all := srv.library.List()
	if len(all) != 3 {
		t.Fatalf("应有 3 个视频，got %d", len(all))
	}
	idByName := map[string]string{}
	for _, v := range all {
		idByName[v.Name] = v.ID
	}

	// 有效进度（100s/600s）写到 b-movie；片头抖动（2s）写到 c-show，应被过滤
	if err := srv.store.SaveProgress(idByName["b-movie"], 100, 600); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.SaveProgress(idByName["c-show"], 2, 600); err != nil {
		t.Fatal(err)
	}

	// 构建器直接断言
	continueCards := srv.buildContinueShelf()
	if len(continueCards) != 1 || continueCards[0].Video.ID != idByName["b-movie"] {
		t.Fatalf("继续观看行应只剩 b-movie 一条，got %+v", continueCards)
	}
	recent := srv.buildRecentShelf()
	if len(recent) != 3 {
		t.Fatalf("最近加入行应有 3 张卡，got %d", len(recent))
	}

	// 默认首页 HTML 含两个横向行标题
	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := readAllString(t, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("首页应 200，got %d", res.StatusCode)
	}
	if !strings.Contains(body, "继续观看") || !strings.Contains(body, "最近加入") {
		t.Fatal("默认首页应渲染两个横向行标题")
	}
	if !strings.Contains(body, "shelf-row") {
		t.Fatal("默认首页应包含横向滚动容器")
	}

	// 带搜索参数时不渲染横向行
	res2, _ := http.Get(ts.URL + "/?q=xxx")
	body2 := readAllString(t, res2.Body)
	res2.Body.Close()
	if strings.Contains(body2, "shelf-row") {
		t.Fatal("搜索态不应渲染横向行")
	}

	// 无持久化服务不应渲染横向行（静默降级）
	inMemLib := media.NewLibrary(t.TempDir(), media.DefaultExts)
	inMemSrv, err := New(inMemLib, srv.thumbs, nil, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ts2 := newHTTPTestServer(t, inMemSrv.Handler())
	res3, _ := http.Get(ts2.URL + "/")
	body3 := readAllString(t, res3.Body)
	res3.Body.Close()
	if strings.Contains(body3, "shelf-row") {
		t.Fatal("无 DB 模式不应渲染横向行")
	}

	_ = videoID // 夹具自带视频，保持引用清晰
}

// TestHasProgressValue 锁定续播过滤口径。
func TestHasProgressValue(t *testing.T) {
	cases := []struct {
		name          string
		position, dur float64
		want          bool
	}{
		{"片头抖动", 2, 600, false},
		{"正常续播", 100, 600, true},
		{"接近看完", 580, 600, false}, // 96.7% >= 95%
		{"时长未知但位置足够", 30, 0, true},
		{"零位置", 0, 600, false},
	}
	for _, c := range cases {
		if got := hasProgressValue(c.position, c.dur); got != c.want {
			t.Fatalf("%s: want %v got %v", c.name, c.want, got)
		}
	}
}
