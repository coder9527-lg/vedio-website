package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedLibraryVideo 在测试服务的媒体库中写入一个视频并增量扫描入库，返回其 ID。
func seedLibraryVideo(t *testing.T, srv *Server, name string) string {
	t.Helper()
	root := srv.library.Root()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("创建媒体目录失败: %v", err)
	}
	if err := os.WriteFile(p, []byte("fake-video"), 0o644); err != nil {
		t.Fatalf("写入测试视频失败: %v", err)
	}
	if _, err := srv.library.Scan(); err != nil {
		t.Fatalf("增量扫描失败: %v", err)
	}
	if srv.library.Len() == 0 {
		t.Fatalf("扫描后媒体库仍为空")
	}
	return srv.library.List()[0].ID
}

// writeCacheTestFile 写入缓存文件并把 mtime 调到过去，脱离 10 分钟保护期。
func writeCacheTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建缓存目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入缓存文件失败: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes 失败: %v", err)
	}
}

func TestCacheHealthPageRendersWithoutStore(t *testing.T) {
	srv, ts := newTestServer(t, "")
	seedLibraryVideo(t, srv, "movie.mp4")
	tmdbDir := t.TempDir()
	srv.library.SetScraperImageDir(tmdbDir)
	// 各伪造一个过保护期孤儿，验证页面区块渲染
	writeCacheTestFile(t, filepath.Join(srv.thumbs.Dir(), "0123456789abcdef.jpg"), "orphan")
	writeCacheTestFile(t, filepath.Join(tmdbDir, "fedcba9876543210", "poster.jpg"), "orphan")

	code, body := getHTML(t, ts, "/cache/health")
	if code != http.StatusOK {
		t.Fatalf("缓存健康页应 200，实际 %d", code)
	}
	for _, want := range []string{"缓存健康检查", "缩略图缓存", "TMDB 刮削缓存", "cache-kind-section", "cache-clean-btn"} {
		if !strings.Contains(body, want) {
			t.Fatalf("页面缺少 %q", want)
		}
	}
}

func TestCacheHealthNavEntry(t *testing.T) {
	_, ts := newTestServer(t, "") // store=nil：导航项位于 StoreOn 块外，仍应渲染

	code, body := getHTML(t, ts, "/")
	if code != http.StatusOK {
		t.Fatalf("首页应 200，实际 %d", code)
	}
	if !strings.Contains(body, "/cache/health") {
		t.Fatalf("首页「更多」下拉应包含缓存健康检查入口")
	}
}

func TestCacheThumbCleanupAPI(t *testing.T) {
	srv, ts := newTestServer(t, "")
	id := seedLibraryVideo(t, srv, "movie.mp4")

	hit := filepath.Join(srv.thumbs.Dir(), id+".jpg")
	orphan := filepath.Join(srv.thumbs.Dir(), "0123456789abcdef.jpg")
	writeCacheTestFile(t, hit, "hit")
	writeCacheTestFile(t, orphan, "orphan") // 6 字节

	code, body := doJSON(t, ts, http.MethodPost, "/api/v1/cache/thumb/orphans", nil)
	if code != http.StatusOK {
		t.Fatalf("清理应 200，实际 %d body=%v", code, body)
	}
	if body["removed"] != float64(1) || body["reclaimed"] != float64(6) {
		t.Fatalf("响应计数不符合预期: %v", body)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("孤儿文件应已删除")
	}
	if _, err := os.Stat(hit); err != nil {
		t.Fatalf("命中文件应保留: %v", err)
	}
}

func TestCacheTMDBCleanupAPI(t *testing.T) {
	srv, ts := newTestServer(t, "")
	id := seedLibraryVideo(t, srv, "movie.mp4")
	tmdbDir := t.TempDir()
	srv.library.SetScraperImageDir(tmdbDir)

	hitDir := filepath.Join(tmdbDir, id)
	orphanDir := filepath.Join(tmdbDir, "0123456789abcdef")
	writeCacheTestFile(t, filepath.Join(hitDir, "poster.jpg"), "hit")
	writeCacheTestFile(t, filepath.Join(orphanDir, "poster.jpg"), "orphan")
	writeCacheTestFile(t, filepath.Join(orphanDir, "metadata.json"), "{}")
	// 目录 mtime 调到过去，脱离保护期
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(orphanDir, old, old)
	_ = os.Chtimes(hitDir, old, old)

	code, body := doJSON(t, ts, http.MethodPost, "/api/v1/cache/tmdb/orphans", nil)
	if code != http.StatusOK {
		t.Fatalf("清理应 200，实际 %d body=%v", code, body)
	}
	if body["removed"] != float64(1) || body["reclaimed"] != float64(8) { // "orphan"(6)+"{}"(2)
		t.Fatalf("响应计数不符合预期: %v", body)
	}
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("孤儿子目录应被整体删除")
	}
	if _, err := os.Stat(hitDir); err != nil {
		t.Fatalf("命中目录应保留: %v", err)
	}
}

func TestCacheCleanupEmptyLibrary409(t *testing.T) {
	_, ts := newTestServer(t, "") // 未写入任何视频，空库

	for _, path := range []string{
		"/api/v1/cache/thumb/orphans",
		"/api/v1/cache/tmdb/orphans",
	} {
		code, body := doJSON(t, ts, http.MethodPost, path, nil)
		if code != http.StatusConflict {
			t.Fatalf("%s 空库应 409，实际 %d", path, code)
		}
		if _, ok := body["error"]; !ok {
			t.Fatalf("409 响应应含 error 字段: %v", body)
		}
	}
}

func TestCacheCleanupAuthRequired(t *testing.T) {
	srv, ts := newTestServer(t, "secret")
	seedLibraryVideo(t, srv, "movie.mp4")

	// API 未登录 → 401 JSON
	code, body := doJSON(t, ts, http.MethodPost, "/api/v1/cache/thumb/orphans", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("未登录 API 应 401，实际 %d", code)
	}
	if body["error"] != "unauthorized" {
		t.Fatalf("401 响应不符合预期: %v", body)
	}

	// 页面未登录 → 303 跳登录
	resp, err := noRedirectClient.Get(ts.URL + "/cache/health")
	if err != nil {
		t.Fatalf("GET 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("未登录页面应 303，实际 %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/login") {
		t.Fatalf("应跳转登录页，实际 %s", loc)
	}
}
