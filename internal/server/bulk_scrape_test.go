package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// newFakeScrapeStack 在临时目录构造含 n 个视频的媒体库与模拟 TMDB 服务，
// 返回已装配刮削器的应用。多视频 + 300ms 节流可为批量任务制造可观测的运行窗口。
func newFakeScrapeStack(t *testing.T, n int) *Server {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < n; i++ {
		name := filepath.Join(root, "电影"+string(rune('A'+i))+".mp4")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}

	apiTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search/movie":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{{
					"id": 555, "title": "模拟电影", "original_title": "Fake Movie",
					"overview": "模拟简介", "release_date": "2022-06-01", "vote_average": 8.1,
					"poster_path": "/p.jpg", "backdrop_path": "/b.jpg",
				}},
			})
		case r.URL.Path == "/movie/555":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 555, "title": "模拟电影", "original_title": "Fake Movie",
				"overview": "模拟简介", "release_date": "2022-06-01", "runtime": 99,
				"vote_average": 8.1, "poster_path": "/p.jpg", "backdrop_path": "/b.jpg",
				"genres":               []map[string]any{{"name": "剧情"}},
				"production_companies": []map[string]any{{"name": "模拟制片厂"}},
				"credits": map[string]any{
					"crew": []map[string]any{{"job": "Director", "name": "模拟导演"}},
					"cast": []map[string]any{{"name": "模拟演员"}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	imgTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("img:" + filepath.Base(r.URL.Path)))
	}))
	t.Cleanup(apiTS.Close)
	t.Cleanup(imgTS.Close)

	client := media.NewTMDBClient("key", "zh-CN", apiTS.URL, imgTS.URL)
	scraper, err := media.NewScraper(client, lib, filepath.Join(t.TempDir(), "tmdb"))
	if err != nil {
		t.Fatal(err)
	}
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(lib, thumbs, nil, nil, nil, scraper, false)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// waitBulkAPI 轮询批量进度端点直到任务结束或超时。
func waitBulkAPI(t *testing.T, ts *httptest.Server) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var final map[string]any
	for time.Now().Before(deadline) {
		status, body := getBody(t, ts, "/api/v1/scrape/bulk")
		if status != http.StatusOK {
			t.Fatalf("轮询状态码 = %d body=%s", status, body)
		}
		final = map[string]any{}
		if err := json.Unmarshal([]byte(body), &final); err != nil {
			t.Fatal(err)
		}
		if final["running"] == false {
			return final
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("批量任务超时未结束: %v", final)
	return final
}

// TestBulkScrapeDisabledReturns503 未配置 TMDB 时两个端点都返回 503。
func TestBulkScrapeDisabledReturns503(t *testing.T) {
	srv, _, _ := newMetadataTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/scrape/bulk"},
		{http.MethodGet, "/api/v1/scrape/bulk"},
	} {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s 期望 503，实际 %d", tc.method, tc.path, resp.StatusCode)
		}
	}
}

// TestBulkScrapeStartAndPoll 验证 202 启动、轮询到完成、计数正确，
// 且完成后元信息确实写入媒体库。
func TestBulkScrapeStartAndPoll(t *testing.T) {
	srv, _, _, videoID, _ := newScrapeTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	// 启动：202 Accepted
	resp, err := http.Post(ts.URL+"/api/v1/scrape/bulk", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("启动期望 202，实际 %d", resp.StatusCode)
	}
	var st struct {
		Running bool `json:"running"`
		Total   int  `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !st.Running || st.Total != 1 {
		t.Fatalf("启动状态错误: %+v", st)
	}

	final := waitBulkAPI(t, ts)
	if final["success"].(float64) != 1 || final["done"].(float64) != 1 {
		t.Fatalf("完成计数错误: %v", final)
	}
	if final["finishedAt"] == "" || final["startedAt"] == "" {
		t.Fatalf("应包含起止时间: %v", final)
	}

	// 元信息已写入：元信息端点能读到刮削标题
	status, body := getBody(t, ts, "/api/v1/videos/"+videoID+"/metadata")
	if status != http.StatusOK || !strings.Contains(body, "模拟电影") {
		t.Fatalf("刮削结果应可通过元信息端点读取: status=%d body=%s", status, body)
	}
}

// TestBulkScrapeConflictWhenRunning 验证运行中重复启动返回 409 且响应体携带进度快照。
func TestBulkScrapeConflictWhenRunning(t *testing.T) {
	srv := newFakeScrapeStack(t, 3) // 三次节流窗口保证任务不会瞬间结束
	ts := newHTTPTestServer(t, srv.Handler())

	first, err := http.Post(ts.URL+"/api/v1/scrape/bulk", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("首次启动期望 202，实际 %d", first.StatusCode)
	}

	// 立即再次启动：三个视频之间有两次 300ms 节流，任务必然仍在运行
	second, err := http.Post(ts.URL+"/api/v1/scrape/bulk", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("运行中重复启动期望 409，实际 %d", second.StatusCode)
	}
	var conflict media.BulkStatus
	if err := json.NewDecoder(second.Body).Decode(&conflict); err != nil {
		t.Fatal(err)
	}
	if !conflict.Running || conflict.Total != 3 {
		t.Fatalf("409 响应应携带运行中进度快照: %+v", conflict)
	}

	// 等待任务结束，避免 goroutine 残留影响后续用例
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !srv.bulkScraper.Status().Running {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("批量任务超时未结束: %+v", srv.bulkScraper.Status())
}

// TestBulkScrapeNoMissingVideos 验证库内全部已有元信息时任务立即结束，Total=0。
func TestBulkScrapeNoMissingVideos(t *testing.T) {
	srv, _, _, videoID, _ := newScrapeTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	// 先手动刮削唯一的视频，使其具备元信息
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/videos/"+videoID+"/scrape",
		strings.NewReader(`{"tmdbId":555}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("前置手动刮削失败: %d", resp.StatusCode)
	}

	// 批量任务：没有缺失元信息的视频，goroutine 立即结束
	start, err := http.Post(ts.URL+"/api/v1/scrape/bulk", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	start.Body.Close()
	if start.StatusCode != http.StatusAccepted {
		t.Fatalf("启动期望 202，实际 %d", start.StatusCode)
	}
	final := waitBulkAPI(t, ts)
	if final["total"].(float64) != 0 || final["running"] != false {
		t.Fatalf("无缺失视频时应立即结束且 Total=0: %v", final)
	}
}
