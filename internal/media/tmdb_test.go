package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// newFakeTMDB 启动模拟 TMDB API 与图片服务，记录搜索关键字，返回固定响应。
func newFakeTMDB(t *testing.T) (*TMDBClient, *httptest.Server, *[]string) {
	t.Helper()
	var queries []string
	mux := http.NewServeMux()

	// 独立图片服务：任意路径返回固定字节，路径末段即 TMDB 图片文件名
	imgTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("image-bytes-" + filepath.Base(r.URL.Path)))
	}))
	t.Cleanup(imgTS.Close)

	mux.HandleFunc("/search/movie", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("query"))
		if r.URL.Query().Get("api_key") != "test-key" {
			http.Error(w, "auth fail", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{
					"id": 999, "title": "流浪地球", "original_title": "The Wandering Earth",
					"overview": "太阳即将毁灭", "release_date": "2019-02-05",
					"vote_average": 7.9, "poster_path": "/p.jpg", "backdrop_path": "/b.jpg",
				},
				{
					"id": 1000, "title": "流浪地球2", "original_title": "The Wandering Earth II",
					"release_date": "2023-01-22", "vote_average": 8.3,
				},
			},
		})
	})

	mux.HandleFunc("/movie/999", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 999, "title": "流浪地球", "original_title": "The Wandering Earth",
			"overview": "太阳即将毁灭，人类开启计划", "release_date": "2019-02-05",
			"runtime": 125, "vote_average": 7.9,
			"poster_path": "/p.jpg", "backdrop_path": "/b.jpg",
			"genres":               []map[string]any{{"name": "科幻"}, {"name": "冒险"}},
			"production_companies": []map[string]any{{"name": "中影"}},
			"credits": map[string]any{
				"crew": []map[string]any{{"job": "Director", "name": "郭帆"}},
				"cast": []map[string]any{{"name": "吴京"}, {"name": "屈楚萧"}},
			},
		})
	})

	// 图片 URL 形如 <imageURL>/w500/p.jpg，用前缀匹配覆盖各尺寸
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	c := NewTMDBClient("test-key", "zh-CN", ts.URL, imgTS.URL)
	return c, ts, &queries
}

func TestNewTMDBClientDisabledWithoutKey(t *testing.T) {
	if c := NewTMDBClient("", "zh-CN", "", ""); c != nil {
		t.Errorf("空 API Key 应返回 nil，实际 %+v", c)
	}
}

func TestTMDBSearchMovie(t *testing.T) {
	c, _, queries := newFakeTMDB(t)
	hits, err := c.SearchMovie(context.Background(), "流浪地球", "2019")
	if err != nil {
		t.Fatalf("SearchMovie 失败: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("期望 2 个候选，实际 %d", len(hits))
	}
	h := hits[0]
	if h.ID != 999 || h.Title != "流浪地球" || h.Year != "2019" || h.Rating != 7.9 {
		t.Errorf("候选字段解析错误: %+v", h)
	}
	if h.PosterPath != "/p.jpg" || h.BackdropPath != "/b.jpg" {
		t.Errorf("图片路径解析错误: %+v", h)
	}
	if len(*queries) != 1 || (*queries)[0] != "流浪地球" {
		t.Errorf("搜索关键字透传错误: %v", *queries)
	}
}

func TestTMDBSearchRejectsEmptyKeyword(t *testing.T) {
	c, _, _ := newFakeTMDB(t)
	if _, err := c.SearchMovie(context.Background(), "  ", ""); err == nil {
		t.Error("空关键字应报错")
	}
}

func TestTMDBSurfacesUpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"status_message":"bad key"}`, http.StatusUnauthorized)
	}))
	defer ts.Close()
	c := NewTMDBClient("bad", "zh-CN", ts.URL, ts.URL)
	if _, err := c.SearchMovie(context.Background(), "x", ""); err == nil {
		t.Error("上游 401 应转为错误")
	}
}

func TestTMDBMovieDetail(t *testing.T) {
	c, _, _ := newFakeTMDB(t)
	md, poster, backdrop, err := c.MovieDetail(context.Background(), 999)
	if err != nil {
		t.Fatalf("MovieDetail 失败: %v", err)
	}
	if md.Title != "流浪地球" || md.Original != "The Wandering Earth" || md.Year != "2019" {
		t.Errorf("基础字段错误: %+v", md)
	}
	if md.Runtime != 125 || md.Rating != 7.9 || md.Studio != "中影" || md.Source != "tmdb" {
		t.Errorf("片长/评分/出品/来源错误: %+v", md)
	}
	if len(md.Genres) != 2 || md.Genres[0] != "科幻" || len(md.Directors) != 1 || md.Directors[0] != "郭帆" {
		t.Errorf("类型/导演错误: %+v", md)
	}
	if len(md.Actors) != 2 || md.Actors[0] != "吴京" {
		t.Errorf("主演错误: %+v", md.Actors)
	}
	if poster != "/p.jpg" || backdrop != "/b.jpg" {
		t.Errorf("图片相对路径错误: %q %q", poster, backdrop)
	}
}

func TestTMDBDownloadImage(t *testing.T) {
	c, ts, _ := newFakeTMDB(t)
	dest := filepath.Join(t.TempDir(), "poster.jpg")
	if err := c.downloadImage(context.Background(), "/p.jpg", "w500", dest); err != nil {
		t.Fatalf("downloadImage 失败: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "image-bytes-p.jpg" {
		t.Errorf("图片内容错误: %q", got)
	}
	// 模拟图片 404
	badTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(func() { badTS.Close() })
	c2 := NewTMDBClient("k", "zh-CN", ts.URL, badTS.URL)
	if err := c2.downloadImage(context.Background(), "/x.jpg", "w500", filepath.Join(t.TempDir(), "x.jpg")); err == nil {
		t.Error("图片 404 应返回错误")
	}
}

func TestPosterURL(t *testing.T) {
	c, _, _ := newFakeTMDB(t)
	got := c.PosterURL("/abc.jpg")
	want := c.imageURL + "/w342/abc.jpg"
	if got != want {
		t.Errorf("PosterURL = %q，期望 %q", got, want)
	}
	if c.PosterURL("") != "" {
		t.Error("空路径应返回空串")
	}
}
