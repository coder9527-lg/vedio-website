package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/store"
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

// postHistoryDelete 以表单方式提交单条删除，返回不跟随重定向的响应。
func postHistoryDelete(t *testing.T, ts *httptest.Server, form url.Values) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/history/event/delete",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("POST /history/event/delete 失败: %v", err)
	}
	return resp
}

// TestHistoryDeleteEventInjection 锁死单条删除的前端契约：删除表单与角标注入锚点。
func TestHistoryDeleteEventInjection(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)
	if _, err := srv.store.StartWatchEvent(id, time.Now().UTC()); err != nil {
		t.Fatalf("造事件失败: %v", err)
	}

	code, body := getHTML(t, ts, "/history")
	if code != http.StatusOK {
		t.Fatalf("/history 应 200，实际 %d", code)
	}
	for _, want := range []string{
		`action="/history/event/delete"`,
		`method="post"`,
		`class="history-del-btn"`,
		`class="history-card-wrap"`,
		`name="id"`,
		`不影响续播进度`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("历史页缺少单条删除注入内容 %q", want)
		}
	}
}

// TestHistoryDeleteSingleEvent 验证删单次会话：303 跳回、目标消失另一条保留、progress 不动。
func TestHistoryDeleteSingleEvent(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)
	// 同视频两条会话事件
	ev1, err := srv.store.StartWatchEvent(id, time.Now().UTC())
	if err != nil {
		t.Fatalf("造事件 ev1 失败: %v", err)
	}
	ev2, err := srv.store.StartWatchEvent(id, time.Now().Add(-time.Hour).UTC())
	if err != nil {
		t.Fatalf("造事件 ev2 失败: %v", err)
	}
	// 写一条续播进度，证明删除会话不级联清理
	if err := srv.store.SaveProgress(id, store.ProgressUpdate{Position: 42, Duration: 100}); err != nil {
		t.Fatalf("SaveProgress 失败: %v", err)
	}

	form := url.Values{}
	form.Set("id", strconv.FormatInt(ev1, 10))
	resp := postHistoryDelete(t, ts, form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/history" {
		t.Fatalf("合法删除应 303 回 /history，实际 %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	// ev1 已删、ev2 保留：页面只剩一张卡片、一个删除表单
	_, body := getHTML(t, ts, "/history")
	if strings.Count(body, "history-card-wrap") != 1 {
		t.Errorf("删除后应剩 1 张卡片，实际 %d", strings.Count(body, "history-card-wrap"))
	}
	if !strings.Contains(body, strconv.FormatInt(ev2, 10)) {
		t.Error("保留的事件 ev2 应仍在页面上")
	}
	if strings.Contains(body, `value="`+strconv.FormatInt(ev1, 10)+`"`) {
		t.Error("被删事件 ev1 不应再出现在页面")
	}
	// progress 不受影响：续播进度条仍渲染
	if p, _ := srv.store.GetProgress(id); p == nil || p.Position != 42 {
		t.Errorf("删除会话不应影响 progress，得到 %+v", p)
	}
}

// TestHistoryDeleteEventBadRequest 覆盖非法 id 与缺失 id 的 400 分支。
func TestHistoryDeleteEventBadRequest(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)
	if _, err := srv.store.StartWatchEvent(id, time.Now().UTC()); err != nil {
		t.Fatalf("造事件失败: %v", err)
	}

	for _, bad := range []url.Values{
		{"id": []string{"abc"}}, // 非数字
		{"id": []string{"0"}},   // 非正数
		{},                      // 缺失 id
	} {
		resp := postHistoryDelete(t, ts, bad)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("非法表单 %v 应 400，实际 %d", bad, resp.StatusCode)
		}
	}

	// 非法请求不应误删数据：卡片仍在
	_, body := getHTML(t, ts, "/history")
	if strings.Count(body, "history-card-wrap") != 1 {
		t.Errorf("非法删除后数据应保持 1 条，实际 %d", strings.Count(body, "history-card-wrap"))
	}
}

// TestHistoryDeleteDisabledWhenStoreOff 锁死无持久化时的服务可用性：返回 503 而非 panic。
func TestHistoryDeleteDisabledWhenStoreOff(t *testing.T) {
	_, ts := newTestServer(t, "")
	form := url.Values{}
	form.Set("id", "1")
	resp := postHistoryDelete(t, ts, form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("无持久化时应 503，实际 %d", resp.StatusCode)
	}
}
