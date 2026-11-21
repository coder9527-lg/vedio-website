package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestHealthzReturnsOK(t *testing.T) {
	_, ts := newTestServer(t, "")

	resp := get(t, ts, "/healthz")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz 应返回 200，实际 %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status 应为 ok，实际 %q", body["status"])
	}
}

func TestHealthzAnonymousWhenAuthEnabled(t *testing.T) {
	// 开启访问密码后，探针仍须匿名可达，否则容器编排会误判重启
	_, ts := newTestServer(t, "secret-pass")

	resp := get(t, ts, "/healthz")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("开启认证后 healthz 仍应匿名返回 200，实际 %d", resp.StatusCode)
	}
}

func TestIsPublicPath(t *testing.T) {
	cases := map[string]bool{
		"/healthz":               true,
		"/login":                 true,
		"/static/css/style.css":  true,
		"/favicon.ico":           true,
		"/thumb-placeholder.svg": true,
		"/share/abc123":          true,
		"/share/abc123/download": true,
		"/":                      false,
		"/api/v1/videos":         false,
		"/watch/abc":             false,
	}
	for path, want := range cases {
		if got := isPublicPath(path); got != want {
			t.Errorf("isPublicPath(%q) = %v，期望 %v", path, got, want)
		}
	}
}
