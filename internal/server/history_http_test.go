package server

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHistoryPageEmptyWhenStoreDisabled(t *testing.T) {
	_, ts := newTestServer(t, "")

	code, body := getHTML(t, ts, "/history")
	if code != http.StatusOK {
		t.Fatalf("无持久化时也应 200，实际 %d", code)
	}
	if !strings.Contains(body, "未启用持久化") {
		t.Error("应显示未启用持久化空态文案")
	}
	if strings.Contains(body, "shelf-card") {
		t.Error("无持久化时不应渲染历史卡片")
	}
}

func TestHistoryRecordsSessionAndGroups(t *testing.T) {
	_, ts, id := newPersistTestServer(t)

	// 同一会话：首次上报 sessionStart=true，第二次 false
	if code, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]any{"position": 10.0, "duration": 100.0, "watchDelta": 10.0, "sessionStart": true}); code != http.StatusOK {
		t.Fatalf("首次上报应 200，实际 %d", code)
	}
	if code, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]any{"position": 35.0, "duration": 100.0, "watchDelta": 25.0, "sessionStart": false}); code != http.StatusOK {
		t.Fatalf("二次上报应 200，实际 %d", code)
	}

	code, body := getHTML(t, ts, "/history")
	if code != http.StatusOK {
		t.Fatalf("/history 应 200，实际 %d", code)
	}
	if !strings.Contains(body, "shelf-card") {
		t.Error("应渲染 1 张历史卡片")
	}
	if !strings.Contains(body, "今天") {
		t.Error("当天事件应归入「今天」分组")
	}
	if !strings.Contains(body, "本次观看") {
		t.Error("卡片应显示本次观看时长")
	}
	if !strings.Contains(body, "清空全部历史") {
		t.Error("有数据时应显示清空按钮")
	}
	if strings.Count(body, "shelf-card") != 1 {
		t.Errorf("同一会话应只有 1 行事件，实际卡片数 %d", strings.Count(body, "shelf-card"))
	}
}

func TestHistoryClearRedirectsAndEmpties(t *testing.T) {
	_, ts, id := newPersistTestServer(t)
	doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]any{"position": 5.0, "duration": 100.0, "watchDelta": 5.0, "sessionStart": true})

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/history/clear", nil)
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("清空请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/history" {
		t.Fatalf("清空应 303 回 /history，实际 %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	_, body := getHTML(t, ts, "/history")
	if strings.Contains(body, "shelf-card") {
		t.Error("清空后不应再有历史卡片")
	}
	if !strings.Contains(body, "还没有观看记录") {
		t.Error("清空后应显示无记录空态")
	}
}

func TestHistorySkipsDanglingEvent(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)

	// 直接插入一条引用已不存在视频的悬空事件，页面应跳过而非 500
	if _, err := srv.store.StartWatchEvent("missing-video-id", time.Now().UTC()); err != nil {
		t.Fatalf("插入悬空事件失败: %v", err)
	}

	code, body := getHTML(t, ts, "/history")
	if code != http.StatusOK {
		t.Fatalf("含悬空事件时也应 200，实际 %d", code)
	}
	if strings.Contains(body, "shelf-card") {
		t.Error("悬空事件不应渲染卡片")
	}
}
