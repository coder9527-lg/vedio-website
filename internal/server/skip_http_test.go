package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skipFrom 从响应体取出 skip 对象，不存在或为 null 时返回 nil。
func skipFrom(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	raw, ok := body["skip"]
	if !ok || raw == nil {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("skip 字段类型异常: %T", raw)
	}
	return m
}

// TestSkipAPIFullLifecycle 验证 GET 空态、PUT 写入与回读、UPSERT 覆盖开关、DELETE 清除。
func TestSkipAPIFullLifecycle(t *testing.T) {
	_, ts, id := newPersistTestServer(t)
	path := "/api/v1/videos/" + id + "/skip"

	// 初始无记录：{"skip": null}
	code, body := doJSON(t, ts, http.MethodGet, path, nil)
	if code != http.StatusOK || body["skip"] != nil {
		t.Fatalf("初始 GET 期望 200 且 skip=null，实际 %d %v", code, body)
	}

	// 首次写入，省略 enabled 字段时默认 true
	code, body = doJSON(t, ts, http.MethodPut, path,
		map[string]any{"introSeconds": 90, "outroSeconds": 60, "duration": 1000})
	if code != http.StatusOK {
		t.Fatalf("首次 PUT 期望 200，实际 %d %v", code, body)
	}
	first := skipFrom(t, body)
	if first == nil || first["introSeconds"] != 90.0 ||
		first["outroSeconds"] != 60.0 || first["enabled"] != true {
		t.Fatalf("首次 PUT 回显不符: %v", body)
	}

	// GET 回读一致
	code, body = doJSON(t, ts, http.MethodGet, path, nil)
	if code != http.StatusOK || skipFrom(t, body)["introSeconds"] != 90.0 {
		t.Fatalf("GET 回读不符，实际 %d %v", code, body)
	}

	// 二次写入覆盖并停用
	off := false
	code, body = doJSON(t, ts, http.MethodPut, path,
		map[string]any{"introSeconds": 120, "outroSeconds": 30, "enabled": off, "duration": 1000})
	if code != http.StatusOK {
		t.Fatalf("覆盖 PUT 期望 200，实际 %d %v", code, body)
	}
	updated := skipFrom(t, body)
	if updated["enabled"] != false || updated["introSeconds"] != 120.0 {
		t.Fatalf("覆盖 PUT 回显不符: %v", body)
	}
	_, body = doJSON(t, ts, http.MethodGet, path, nil)
	if skipFrom(t, body)["enabled"] != false {
		t.Fatalf("GET 应反映停用状态，实际 %v", body)
	}

	// DELETE 后再查为空
	code, body = doJSON(t, ts, http.MethodDelete, path, nil)
	if code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("DELETE 期望 200 status=ok，实际 %d %v", code, body)
	}
	code, body = doJSON(t, ts, http.MethodGet, path, nil)
	if code != http.StatusOK || body["skip"] != nil {
		t.Fatalf("清除后 GET 期望 skip=null，实际 %d %v", code, body)
	}
}

// TestSkipAPIValidation 覆盖 400 校验序列与 duration 未知时放行的规则。
func TestSkipAPIValidation(t *testing.T) {
	_, ts, id := newPersistTestServer(t)
	path := "/api/v1/videos/" + id + "/skip"

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"负片头", map[string]any{"introSeconds": -1, "outroSeconds": 10}, http.StatusBadRequest},
		{"负片尾", map[string]any{"introSeconds": 10, "outroSeconds": -1}, http.StatusBadRequest},
		{"片头大于总时长", map[string]any{"introSeconds": 1000, "outroSeconds": 0, "duration": 1000}, http.StatusBadRequest},
		{"片头片尾重叠", map[string]any{"introSeconds": 600, "outroSeconds": 500, "duration": 1000}, http.StatusBadRequest},
		{"时长未知放行", map[string]any{"introSeconds": 9999, "outroSeconds": 9999, "duration": 0}, http.StatusOK},
		{"无 duration 字段放行", map[string]any{"introSeconds": 30, "outroSeconds": 20}, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _ := doJSON(t, ts, http.MethodPut, path, c.body)
			if code != c.want {
				t.Fatalf("%s 期望 %d，实际 %d", c.name, c.want, code)
			}
		})
	}

	// 非法 JSON 直接构造 PUT 请求（POST 未注册会得到 405）
	req, _ := http.NewRequest(http.MethodPut, ts.URL+path, strings.NewReader("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("非法 JSON 请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 JSON 期望 400，实际 %d", resp.StatusCode)
	}
}

// TestSkipAPIVideoNotFound 验证对不存在的视频 PUT 返回 404。
func TestSkipAPIVideoNotFound(t *testing.T) {
	_, ts, _ := newPersistTestServer(t)
	code, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/ghost-video/skip",
		map[string]any{"introSeconds": 10, "outroSeconds": 10})
	if code != http.StatusNotFound {
		t.Fatalf("ghost 视频 PUT 期望 404，实际 %d", code)
	}
}

// TestWatchPageInjectsSkipSetting 验证播放页 data-* 注入与停用/降级渲染。
func TestWatchPageInjectsSkipSetting(t *testing.T) {
	_, ts, id := newPersistTestServer(t)
	path := "/api/v1/videos/" + id + "/skip"

	on := true
	if code, _ := doJSON(t, ts, http.MethodPut, path,
		map[string]any{"introSeconds": 90, "outroSeconds": 60, "enabled": on, "duration": 1000}); code != http.StatusOK {
		t.Fatalf("准备跳过设置失败: %d", code)
	}

	code, html := getHTML(t, ts, "/watch/"+id)
	if code != http.StatusOK {
		t.Fatalf("播放页期望 200，实际 %d", code)
	}
	for _, want := range []string{
		`id="skip-modal"`,
		`id="skip-open-btn"`,
		`data-intro="90"`,
		`data-outro="60"`,
		`data-enabled="1"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少注入片段 %q", want)
		}
	}

	// 停用后 data-enabled 翻为 0，数据仍注入
	off := false
	if code, _ := doJSON(t, ts, http.MethodPut, path,
		map[string]any{"introSeconds": 90, "outroSeconds": 60, "enabled": off, "duration": 1000}); code != http.StatusOK {
		t.Fatalf("停用更新失败: %d", code)
	}
	_, html = getHTML(t, ts, "/watch/"+id)
	if !strings.Contains(html, `data-enabled="0"`) {
		t.Error("停用后播放页应包含 data-enabled=\"0\"")
	}

	// 无持久化的服务：其库为空，需先放入视频并重扫，再用它自己的 id 访问播放页
	srvNoDB, tsNoDB := newTestServer(t, "")
	noDBPath := filepath.Join(srvNoDB.library.Root(), "plain.mp4")
	if err := os.WriteFile(noDBPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("写入无 DB 测试视频失败: %v", err)
	}
	if _, err := srvNoDB.library.Scan(); err != nil {
		t.Fatalf("无 DB 服务重扫失败: %v", err)
	}
	noDBID := srvNoDB.library.List()[0].ID
	code, html = getHTML(t, tsNoDB, "/watch/"+noDBID)
	if code != http.StatusOK {
		t.Fatalf("无 DB 播放页期望 200，实际 %d", code)
	}
	if strings.Contains(html, "skip-open-btn") || strings.Contains(html, "skip-modal") {
		t.Error("未启用持久化时播放页不应包含跳过设置入口与弹窗")
	}
}
