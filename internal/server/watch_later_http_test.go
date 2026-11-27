package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchLaterAPILifecycle 验证加入（幂等）、状态查询、列表、移除的完整 API 链路。
func TestWatchLaterAPILifecycle(t *testing.T) {
	_, ts, id := newPersistTestServer(t)

	// 初始状态：不在清单
	status, body := doJSON(t, ts, http.MethodGet, "/api/v1/videos/"+id+"/watch-later", nil)
	if status != http.StatusOK || body["watchLater"] != false {
		t.Fatalf("初始状态应为 false, status=%d body=%v", status, body)
	}

	// 加入
	status, body = doJSON(t, ts, http.MethodPost, "/api/v1/videos/"+id+"/watch-later", nil)
	if status != http.StatusOK || body["watchLater"] != true {
		t.Fatalf("加入后应返回 true, status=%d body=%v", status, body)
	}
	// 重复加入幂等，仍为 200
	if status, _ := doJSON(t, ts, http.MethodPost, "/api/v1/videos/"+id+"/watch-later", nil); status != http.StatusOK {
		t.Fatalf("重复加入应幂等返回 200，实际 %d", status)
	}

	// 再查为 true
	if status, body := doJSON(t, ts, http.MethodGet, "/api/v1/videos/"+id+"/watch-later", nil); status != http.StatusOK || body["watchLater"] != true {
		t.Fatalf("加入后查询应为 true, status=%d body=%v", status, body)
	}

	// 列表接口包含该 ID 且计数为 1
	status, body = doJSON(t, ts, http.MethodGet, "/watch-later/list", nil)
	if status != http.StatusOK || body["count"] != float64(1) {
		t.Fatalf("列表计数应为 1, status=%d body=%v", status, body)
	}
	ids, _ := body["videoIds"].([]any)
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("列表应包含视频 ID %s，实际 %v", id, ids)
	}

	// 移除
	status, body = doJSON(t, ts, http.MethodDelete, "/api/v1/videos/"+id+"/watch-later", nil)
	if status != http.StatusOK || body["watchLater"] != false {
		t.Fatalf("移除后应返回 false, status=%d body=%v", status, body)
	}
	// 再查为 false
	if status, body := doJSON(t, ts, http.MethodGet, "/api/v1/videos/"+id+"/watch-later", nil); status != http.StatusOK || body["watchLater"] != false {
		t.Fatalf("移除后查询应为 false, status=%d body=%v", status, body)
	}
}

// TestWatchLaterAddUnknownVideo 加入媒体库中不存在的视频 ID 应 404。
func TestWatchLaterAddUnknownVideo(t *testing.T) {
	_, ts, _ := newPersistTestServer(t)

	status, _ := doJSON(t, ts, http.MethodPost, "/api/v1/videos/does-not-exist/watch-later", nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的视频加入应 404，实际 %d", status)
	}
}

// TestWatchLaterPageRendered 加入后独立页面应渲染卡片、移除按钮与计数。
func TestWatchLaterPageRendered(t *testing.T) {
	_, ts, id := newPersistTestServer(t)

	if status, _ := doJSON(t, ts, http.MethodPost, "/api/v1/videos/"+id+"/watch-later", nil); status != http.StatusOK {
		t.Fatalf("加入稍后再看失败，状态 %d", status)
	}

	code, html := getHTML(t, ts, "/watch-later")
	if code != http.StatusOK {
		t.Fatalf("稍后再看页状态码 = %d", code)
	}
	containsAll(t, html,
		"🕓 稍后再看",
		`data-wl-list`,
		`data-wl-card`,
		`data-wl-remove`,
		`data-wl-count`,
		`/watch/`+id,
	)
}

// TestWatchLaterPageEmpty 清单为空时渲染空状态，不渲染列表与移除按钮。
func TestWatchLaterPageEmpty(t *testing.T) {
	_, ts, _ := newPersistTestServer(t)

	code, html := getHTML(t, ts, "/watch-later")
	if code != http.StatusOK {
		t.Fatalf("稍后再看页状态码 = %d", code)
	}
	if !strings.Contains(html, "稍后再看清单是空的") {
		t.Error("空清单应渲染空状态文案")
	}
	// 实际移除按钮带 data-wl-remove data-id；脚本里的选择器字符串不算按钮，故断言按钮特征
	if strings.Contains(html, `data-wl-remove data-id=`) {
		t.Error("空清单不应渲染移除按钮")
	}
	if strings.Contains(html, `data-wl-card data-id=`) {
		t.Error("空清单不应渲染卡片")
	}
}

// TestWatchLaterAutoArchiveOnCompletion 上报看完进度（≥95%）后，访问页面应自动归档，卡片消失。
func TestWatchLaterAutoArchiveOnCompletion(t *testing.T) {
	_, ts, id := newPersistTestServer(t)

	if status, _ := doJSON(t, ts, http.MethodPost, "/api/v1/videos/"+id+"/watch-later", nil); status != http.StatusOK {
		t.Fatalf("加入稍后再看失败，状态 %d", status)
	}
	// 上报 96% 进度，触发 completed 黏滞标记
	if status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]float64{"position": 96, "duration": 100}); status != http.StatusOK {
		t.Fatalf("上报进度失败，状态 %d", status)
	}

	code, html := getHTML(t, ts, "/watch-later")
	if code != http.StatusOK {
		t.Fatalf("稍后再看页状态码 = %d", code)
	}
	// 脚本里的 [data-wl-card] 选择器不算卡片，故断言实际卡片特征 data-wl-card data-id
	if strings.Contains(html, `data-wl-card data-id=`) {
		t.Error("已看完的条目应被自动归档，页面不应再有卡片")
	}
	if !strings.Contains(html, "稍后再看清单是空的") {
		t.Error("自动归档后应展示空状态")
	}
	// 后端清单也应已清空
	if status, body := doJSON(t, ts, http.MethodGet, "/watch-later/list", nil); status != http.StatusOK || body["count"] != float64(0) {
		t.Fatalf("归档后列表计数应为 0, status=%d body=%v", status, body)
	}
}

// TestWatchLaterWatchButtonReflectsState 播放页按钮根据持久化状态渲染初始文案与 data-wl。
func TestWatchLaterWatchButtonReflectsState(t *testing.T) {
	_, ts, id := newPersistTestServer(t)

	// 未加入：按钮为加入态
	code, html := getHTML(t, ts, "/watch/"+id)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	if !strings.Contains(html, `id="wl-btn"`) || !strings.Contains(html, `data-wl="false"`) {
		t.Error("未加入时播放页应渲染 data-wl=false 的稍后再看按钮")
	}
	if !strings.Contains(html, "＋ 稍后再看") {
		t.Error("未加入时按钮文案应为「＋ 稍后再看」")
	}

	// 加入后：按钮切为已加入态
	if status, _ := doJSON(t, ts, http.MethodPost, "/api/v1/videos/"+id+"/watch-later", nil); status != http.StatusOK {
		t.Fatalf("加入失败，状态 %d", status)
	}
	_, html = getHTML(t, ts, "/watch/"+id)
	if !strings.Contains(html, `data-wl="true"`) || !strings.Contains(html, "已加入待看") {
		t.Error("加入后播放页按钮应渲染为已加入态")
	}
}

// TestWatchLaterNavEntry 首页「更多」下拉应包含稍后再看导航入口。
func TestWatchLaterNavEntry(t *testing.T) {
	srv, _, _ := newPersistTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/")
	if code != http.StatusOK {
		t.Fatalf("首页状态码 = %d", code)
	}
	if !strings.Contains(html, `href="/watch-later"`) {
		t.Error("首页应包含稍后再看导航入口")
	}
}
