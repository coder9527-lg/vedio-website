package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// getHTML 发起 GET 请求并返回状态码与响应体字符串。
func getHTML(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := noRedirectClient.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s 失败: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, readAllString(t, resp.Body)
}

func TestStatsPageRenders(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)

	// 首次会话上报：累计 5 秒、会话开始
	if code, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]any{"position": 30, "duration": 100, "watchDelta": 5, "sessionStart": true}); code != http.StatusOK {
		t.Fatalf("首次上报应 200，实际 %d", code)
	}
	// 再次上报达到 95% 以上，触发看完
	if code, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]any{"position": 96, "duration": 100, "watchDelta": 7, "sessionStart": false}); code != http.StatusOK {
		t.Fatalf("二次上报应 200，实际 %d", code)
	}
	if err := srv.store.SetFavorite(id, true); err != nil {
		t.Fatal(err)
	}

	code, body := getHTML(t, ts, "/stats")
	if code != http.StatusOK {
		t.Fatalf("/stats 应 200，实际 %d", code)
	}
	for _, want := range []string{
		"观影统计",
		"总观看时长",
		"stat-card",
		"已看完",
		"收藏数",
		"最近观看",
		"✓ 已看完",
		"导出观影数据",
		"/api/v1/export/progress.csv",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("统计页缺少 %q", want)
		}
	}
}

func TestProgressPutAccumulatesWatchTime(t *testing.T) {
	_, ts, id := newPersistTestServer(t)

	// 首次上报：会话开始，增量 5
	doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]any{"position": 10, "duration": 100, "watchDelta": 5, "sessionStart": true})
	// 同会话上报：增量 7，不带 sessionStart
	doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]any{"position": 17, "duration": 100, "watchDelta": 7, "sessionStart": false})

	code, body := doJSON(t, ts, http.MethodGet, "/api/v1/videos/"+id+"/progress", nil)
	if code != http.StatusOK {
		t.Fatalf("查询进度应 200，实际 %d", code)
	}
	p, ok := body["progress"].(map[string]any)
	if !ok {
		t.Fatalf("进度数据异常: %v", body)
	}
	if p["watchTimeSeconds"] != 12.0 {
		t.Fatalf("累计观看时长应为 12，got %v", p["watchTimeSeconds"])
	}
	if p["playCount"] != 1.0 {
		t.Fatalf("播放次数应为 1，got %v", p["playCount"])
	}
	if p["completed"] != false {
		t.Fatalf("未达阈值不应看完，got %v", p["completed"])
	}
}

func TestStatsPageEmptyWhenStoreDisabled(t *testing.T) {
	_, ts := newTestServer(t, "") // store=nil

	code, body := getHTML(t, ts, "/stats")
	if code != http.StatusOK {
		t.Fatalf("无 DB 时 /stats 也应 200 渲染空态，实际 %d", code)
	}
	if !strings.Contains(body, "未启用持久化") {
		t.Fatal("应显示未启用持久化空态提示")
	}
	if strings.Contains(body, "stat-card") {
		t.Fatal("无 DB 时不应渲染统计卡片")
	}
	if strings.Contains(body, "export-csv-btn") {
		t.Fatal("无 DB 空态不应渲染 CSV 导出入口")
	}
}

func TestProgressPutRejectsNegativeDelta(t *testing.T) {
	_, ts, id := newPersistTestServer(t)

	code, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]any{"position": 10, "duration": 100, "watchDelta": -1})
	if code != http.StatusBadRequest {
		t.Fatalf("负 watchDelta 应 400，实际 %d", code)
	}
}
