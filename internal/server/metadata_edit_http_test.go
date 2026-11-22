package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// newMetadataEditServer 构造含一个视频、且登记了可写刮削缓存目录的测试服务（手动编辑可用）。
func newMetadataEditServer(t *testing.T) (*Server, *httptest.Server, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.mp4"), []byte("fake-video"), 0o644); err != nil {
		t.Fatalf("写视频失败: %v", err)
	}
	lib := media.NewLibrary(root, media.DefaultExts)
	cacheDir := t.TempDir()
	lib.SetScraperImageDir(cacheDir)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatalf("NewThumbnailer 失败: %v", err)
	}
	srv, err := New(lib, thumbs, nil, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return srv, newHTTPTestServer(t, srv.Handler()), lib.List()[0].ID, cacheDir
}

// TestMetadataEditHTTPSaveAndReadback 验证 PUT 保存后 GET 能读回手动值，且 manual.json 落盘。
func TestMetadataEditHTTPSaveAndReadback(t *testing.T) {
	srv, ts, id, cacheDir := newMetadataEditServer(t)

	body := map[string]any{
		"title":     "网页手动片名",
		"original":  "Manual Original",
		"overview":  "手动简介",
		"year":      "2024",
		"runtime":   130,
		"rating":    9.2,
		"genres":    []string{"剧情", "悬疑"},
		"directors": []string{"导演甲"},
		"actors":    []string{"演员乙", "演员丙"},
		"studio":    "手动厂牌",
	}
	status, resp := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/metadata", body)
	if status != http.StatusOK {
		t.Fatalf("保存应 200，实际 %d body=%v", status, resp)
	}
	if resp["title"] != "网页手动片名" || resp["runtime"] != 130.0 || resp["rating"] != 9.2 {
		t.Fatalf("响应合并元信息不符: %+v", resp)
	}

	// GET 读回
	status, resp = doJSON(t, ts, http.MethodGet, "/api/v1/videos/"+id+"/metadata", nil)
	if status != http.StatusOK || resp["title"] != "网页手动片名" {
		t.Fatalf("读回失败 status=%d body=%v", status, resp)
	}
	if genres, _ := resp["genres"].([]any); len(genres) != 2 {
		t.Fatalf("类型切片读回不符: %v", resp["genres"])
	}

	// manual.json 真实落盘
	manualPath := filepath.Join(cacheDir, id, "manual.json")
	raw, err := os.ReadFile(manualPath)
	if err != nil {
		t.Fatalf("manual.json 未生成: %v", err)
	}
	if !strings.Contains(string(raw), "网页手动片名") {
		t.Fatalf("manual.json 内容不符: %s", raw)
	}

	// 库内存元信息同步生效
	if md := srv.library.Metadata(id); md == nil || md.Title != "网页手动片名" {
		t.Fatalf("库内元信息未同步: %+v", md)
	}
}

// TestMetadataEditHTTPValidation 验证非法 JSON、越界字段、未知视频的状态码。
func TestMetadataEditHTTPValidation(t *testing.T) {
	_, ts, id, _ := newMetadataEditServer(t)

	// 负片长 → 400
	status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/metadata",
		map[string]any{"runtime": -5})
	if status != http.StatusBadRequest {
		t.Fatalf("负片长应 400，实际 %d", status)
	}
	// 评分超上限 → 400
	status, _ = doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/metadata",
		map[string]any{"rating": 10.5})
	if status != http.StatusBadRequest {
		t.Fatalf("越界评分应 400，实际 %d", status)
	}
	// 不存在的视频 → 404
	status, _ = doJSON(t, ts, http.MethodPut, "/api/v1/videos/ghost/metadata",
		map[string]any{"title": "x"})
	if status != http.StatusNotFound {
		t.Fatalf("未知视频应 404，实际 %d", status)
	}
	// 非法 JSON → 400
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/videos/"+id+"/metadata",
		strings.NewReader("{broken"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，实际 %d", resp.StatusCode)
	}
}

// TestMetadataEditHTTPDisabledWithoutCacheDir 验证未配置缓存目录时 PUT 返回 503，
// 且播放页不渲染编辑入口（与刮削开关解耦：本夹具 scraper 本就为 nil）。
func TestMetadataEditHTTPDisabledWithoutCacheDir(t *testing.T) {
	// newMetadataTestServer 的库未登记缓存目录
	srv, id, _ := newMetadataTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	if srv.metadataEditEnabled() {
		t.Fatal("未登记缓存目录时手动编辑应不可用")
	}
	status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/metadata",
		map[string]any{"title": "x"})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("未配置缓存目录应 503，实际 %d", status)
	}

	// 播放页不应出现编辑按钮与弹窗
	code, html := getHTML(t, ts, "/watch/"+id)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	if strings.Contains(html, `id="metadata-edit-btn"`) || strings.Contains(html, `id="metadata-edit-modal"`) {
		t.Fatal("手动编辑不可用时播放页不应渲染编辑入口")
	}
}

// TestMetadataEditHTTPWatchPageEntry 验证可用时播放页渲染编辑按钮、弹窗与提交脚本。
func TestMetadataEditHTTPWatchPageEntry(t *testing.T) {
	_, ts, id, _ := newMetadataEditServer(t)
	code, html := getHTML(t, ts, "/watch/"+id)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		`id="metadata-edit-btn"`,
		`id="metadata-edit-modal"`,
		`id="me-form"`,
		`id="me-title"`,
		`id="me-overview"`,
		`method: 'PUT'`,
		"/metadata",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少手动编辑注入内容 %q", want)
		}
	}
}

// TestMetadataEditHTTPClearFallsBack 验证全空提交会删除覆盖文件并回退到底值。
func TestMetadataEditHTTPClearFallsBack(t *testing.T) {
	_, ts, id, cacheDir := newMetadataEditServer(t)
	manualPath := filepath.Join(cacheDir, id, "manual.json")

	// 先保存一个手动值
	if status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/metadata",
		map[string]any{"title": "临时手动值"}); status != http.StatusOK {
		t.Fatalf("首次保存应 200，实际 %d", status)
	}
	if _, err := os.Stat(manualPath); err != nil {
		t.Fatalf("manual.json 应已存在: %v", err)
	}

	// 全空提交：切片字段用 null（省略），标量给零值，后端判定 manualEmpty 后删文件
	status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/metadata", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("全空提交应 200，实际 %d", status)
	}
	if _, err := os.Stat(manualPath); !os.IsNotExist(err) {
		t.Fatalf("全空提交后 manual.json 应被删除，stat err=%v", err)
	}
	// 该视频本无 NFO/刮削底值，清空后 GET 回到 404
	if status, _ := doJSON(t, ts, http.MethodGet, "/api/v1/videos/"+id+"/metadata", nil); status != http.StatusNotFound {
		t.Fatalf("清空且无底值后 GET 应 404，实际 %d", status)
	}
}
