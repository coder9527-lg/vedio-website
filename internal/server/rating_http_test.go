package server

import (
	"net/http"
	"testing"
)

// TestRatingAPIWriteGetDelete 评分 API 的写入、覆盖、读取与清除全链路。
func TestRatingAPIWriteGetDelete(t *testing.T) {
	srv, ids := newVideoDetailPersistServer(t)
	ts := newHTTPTestServer(t, srv.Handler())
	id := ids["a.mp4"]
	endpoint := "/api/v1/videos/" + id + "/rating"

	// 初始无评分
	code, body := doJSON(t, ts, http.MethodGet, endpoint, nil)
	if code != http.StatusOK || body["stars"] != float64(0) {
		t.Fatalf("初始评分期望 200/stars=0，实际 %d/%v", code, body["stars"])
	}

	// 写入 4 星
	code, body = doJSON(t, ts, http.MethodPut, endpoint, map[string]int{"stars": 4})
	if code != http.StatusOK || body["stars"] != float64(4) {
		t.Fatalf("写入评分期望 200/stars=4，实际 %d/%v", code, body["stars"])
	}

	// 覆盖为 2 星
	code, _ = doJSON(t, ts, http.MethodPut, endpoint, map[string]int{"stars": 2})
	if code != http.StatusOK {
		t.Fatalf("覆盖评分期望 200，实际 %d", code)
	}
	code, body = doJSON(t, ts, http.MethodGet, endpoint, nil)
	if body["stars"] != float64(2) {
		t.Fatalf("覆盖后评分期望 2，实际 %v", body["stars"])
	}

	// 清除
	code, _ = doJSON(t, ts, http.MethodDelete, endpoint, nil)
	if code != http.StatusOK {
		t.Fatalf("清除评分期望 200，实际 %d", code)
	}
	code, body = doJSON(t, ts, http.MethodGet, endpoint, nil)
	if body["stars"] != float64(0) {
		t.Fatalf("清除后评分期望 0，实际 %v", body["stars"])
	}
}

// TestRatingAPIRejectsInvalidStars 非法档位返回 400，不写库。
func TestRatingAPIRejectsInvalidStars(t *testing.T) {
	srv, ids := newVideoDetailPersistServer(t)
	ts := newHTTPTestServer(t, srv.Handler())
	endpoint := "/api/v1/videos/" + ids["a.mp4"] + "/rating"

	for _, stars := range []int{0, -1, 6, 99} {
		code, _ := doJSON(t, ts, http.MethodPut, endpoint, map[string]int{"stars": stars})
		if code != http.StatusBadRequest {
			t.Errorf("stars=%d 期望 400，实际 %d", stars, code)
		}
	}
}

// TestRatingAPIVideoNotFound 未知视频 ID 返回 404。
func TestRatingAPIVideoNotFound(t *testing.T) {
	srv, _ := newVideoDetailPersistServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, _ := doJSON(t, ts, http.MethodPut,
		"/api/v1/videos/missing/rating", map[string]int{"stars": 3})
	if code != http.StatusNotFound {
		t.Errorf("未知视频评分期望 404，实际 %d", code)
	}
}

// TestRatingAPIDisabledWithoutStore 未启用持久化时评分接口统一 503。
func TestRatingAPIDisabledWithoutStore(t *testing.T) {
	srv, ids := newVideoDetailTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())
	endpoint := "/api/v1/videos/" + ids["films/alpha.mp4"] + "/rating"

	for _, tc := range []struct {
		method string
	}{
		{http.MethodGet}, {http.MethodPut}, {http.MethodDelete},
	} {
		code, _ := doJSON(t, ts, tc.method, endpoint, map[string]int{"stars": 3})
		if code != http.StatusServiceUnavailable {
			t.Errorf("%s 无 store 期望 503，实际 %d", tc.method, code)
		}
	}
}

// TestDetailPageRatingWidget 详情页渲染星级组件且反映当前评分。
func TestDetailPageRatingWidget(t *testing.T) {
	srv, ids := newVideoDetailPersistServer(t)
	ts := newHTTPTestServer(t, srv.Handler())
	id := ids["a.mp4"]

	// 未评分：组件在、文案为未评分
	_, html := getHTML(t, ts, "/video/"+id)
	containsAll(t, html,
		`data-vd-rating`,
		`data-stars="0"`,
		"未评分",
	)

	if err := srv.store.SetRating(id, 5); err != nil {
		t.Fatal(err)
	}
	_, html = getHTML(t, ts, "/video/"+id)
	containsAll(t, html,
		`data-stars="5"`,
		"已评 5 星",
	)
}

// TestHighRatedPage 「我的高分」页只收录 4 星及以上并按星级倒序。
func TestHighRatedPage(t *testing.T) {
	srv, ids := newVideoDetailPersistServer(t)
	ts := newHTTPTestServer(t, srv.Handler())
	a, b := ids["a.mp4"], ids["b.mp4"]

	if err := srv.store.SetRating(a, 5); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.SetRating(b, 3); err != nil {
		t.Fatal(err)
	}

	code, html := getHTML(t, ts, "/my-ratings")
	if code != http.StatusOK {
		t.Fatalf("高分页状态码 = %d", code)
	}
	containsAll(t, html,
		"我的高分",
		`href="/video/`+a+`"`,
		"★★★★★",
	)
	// 3 星影片不应进入高分视图
	if contains(html, `href="/video/`+b+`"`) {
		t.Error("3 星影片不应出现在我的高分页")
	}
}

// TestHighRatedPageDisabledWithoutStore 无持久化时高分页渲染降级提示。
func TestHighRatedPageDisabledWithoutStore(t *testing.T) {
	srv, _ := newVideoDetailTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/my-ratings")
	if code != http.StatusOK {
		t.Fatalf("无 store 高分页状态码 = %d", code)
	}
	containsAll(t, html,
		"个人评分需要持久化支持",
		"DB_PATH",
	)
}

// TestHomeRatingBadge 首页电影卡片对已评分视频渲染评分角标。
func TestHomeRatingBadge(t *testing.T) {
	srv, ids := newVideoDetailPersistServer(t)
	ts := newHTTPTestServer(t, srv.Handler())
	if err := srv.store.SetRating(ids["a.mp4"], 4); err != nil {
		t.Fatal(err)
	}

	_, html := getHTML(t, ts, "/")
	containsAll(t, html,
		"rating-badge",
		"★★★★",
		`href="/my-ratings"`,
	)
}
