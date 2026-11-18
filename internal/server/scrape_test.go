package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// newScrapeTestServer 用模拟 TMDB 服务组装一个带刮削器的应用。
func newScrapeTestServer(t *testing.T) (srv *Server, apiTS, imgTS *httptest.Server, videoID, imageDir string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	videoID = lib.List()[0].ID

	apiTS = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	imgTS = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("img:" + filepath.Base(r.URL.Path)))
	}))
	t.Cleanup(apiTS.Close)
	t.Cleanup(imgTS.Close)

	client := media.NewTMDBClient("key", "zh-CN", apiTS.URL, imgTS.URL)
	imageDir = filepath.Join(t.TempDir(), "tmdb")
	scraper, err := media.NewScraper(client, lib, imageDir)
	if err != nil {
		t.Fatal(err)
	}
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	srv, err = New(lib, thumbs, nil, nil, nil, scraper, false)
	if err != nil {
		t.Fatal(err)
	}
	return srv, apiTS, imgTS, videoID, imageDir
}

func TestScrapeDisabledReturns503(t *testing.T) {
	// 不传 scraper 的普通服务
	srv, _, _ := newMetadataTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/videos/" + "x" + "/scrape", ""},
		{http.MethodPost, "/api/v1/videos/x/scrape", `{"tmdbId":1}`},
	} {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.path, bytes.NewBufferString(tc.body))
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

func TestScrapeSearchAndApply(t *testing.T) {
	srv, _, _, videoID, imageDir := newScrapeTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	// 搜索候选
	status, body := getBody(t, ts, "/api/v1/videos/"+videoID+"/scrape")
	if status != http.StatusOK {
		t.Fatalf("搜索状态码 = %d，body=%s", status, body)
	}
	var searchResp struct {
		Count int `json:"count"`
		Hits  []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(body), &searchResp); err != nil || searchResp.Count != 1 || searchResp.Hits[0].ID != 555 {
		t.Fatalf("搜索响应解析错误: %v body=%s", err, body)
	}

	// 选定候选并应用
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/videos/"+videoID+"/scrape",
		bytes.NewBufferString(`{"tmdbId":555}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		t.Fatalf("Apply 状态码 = %d，body=%s", resp.StatusCode, buf[:n])
	}
	var md media.Metadata
	if err := json.NewDecoder(resp.Body).Decode(&md); err != nil {
		t.Fatal(err)
	}
	if md.Title != "模拟电影" || md.Source != "tmdb" || md.Runtime != 99 || len(md.Actors) != 1 {
		t.Errorf("刮削元信息错误: %+v", md)
	}
	if md.PosterPath == "" || filepath.Dir(md.PosterPath) != filepath.Join(imageDir, videoID) {
		t.Errorf("海报应下载到缓存目录: %q", md.PosterPath)
	}

	// 海报通过 /artwork 提供（图片位于媒体根目录之外的 TMDB 缓存目录）
	status, body = getBody(t, ts, "/artwork/"+videoID+"/poster")
	if status != http.StatusOK || body != "img:p.jpg" {
		t.Errorf("跨目录海报服务错误: status=%d body=%q", status, body)
	}
	status, body = getBody(t, ts, "/artwork/"+videoID+"/backdrop")
	if status != http.StatusOK || body != "img:b.jpg" {
		t.Errorf("跨目录背景图服务错误: status=%d body=%q", status, body)
	}
}

func TestScrapeApplyRejectsBadRequest(t *testing.T) {
	srv, _, _, videoID, _ := newScrapeTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	for _, body := range []string{`not-json`, `{"tmdbId":0}`, `{"tmdbId":-1}`} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/videos/"+videoID+"/scrape",
			bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body=%q 期望 400，实际 %d", body, resp.StatusCode)
		}
	}
}

func TestScrapeUnknownVideo404(t *testing.T) {
	srv, _, _, _, _ := newScrapeTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	status, _ := getBody(t, ts, "/api/v1/videos/unknown/scrape")
	if status != http.StatusNotFound {
		t.Errorf("未知视频搜索期望 404，实际 %d", status)
	}
}

func TestArtworkRejectsPathOutsideRoots(t *testing.T) {
	srv, _, _, videoID, _ := newScrapeTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	// 先刮削拿到海报，再确认媒体根与缓存目录之外的文件无法通过任何方式触达：
	// artwork 端点只认 Metadata 登记的路径，外部路径无法注入，
	// 未知 kind 与穿越尝试均返回 404（与 NFO 流程同一套白名单逻辑）。
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/videos/"+videoID+"/scrape",
		bytes.NewBufferString(`{"tmdbId":555}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	for _, path := range []string{
		"/artwork/" + videoID + "/etc",
		"/artwork/" + videoID + "/poster/..%2f..%2f..%2fetc%2fpasswd",
	} {
		status, _ := getBody(t, ts, path)
		if status != http.StatusNotFound {
			t.Errorf("%s 期望 404，实际 %d", path, status)
		}
	}
}
