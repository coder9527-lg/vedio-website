package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// newMetadataTestServer 构造含单个视频及其 NFO/海报/背景图的测试服务。
func newMetadataTestServer(t *testing.T) (srv *Server, videoID, root string) {
	t.Helper()
	root = t.TempDir()
	files := map[string]string{
		"films/movie.mp4":         "fake-video",
		"films/movie.nfo":         "<movie><title>服务层电影</title><year>2024</year><plot>简介</plot></movie>",
		"films/movie-poster.jpg":  "poster-bytes",
		"films/movie.fanart.webp": "backdrop-bytes",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("写入文件失败 %s: %v", rel, err)
		}
	}
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	videoID = lib.List()[0].ID

	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatalf("NewThumbnailer 失败: %v", err)
	}
	srv, err = New(lib, thumbs, nil, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return srv, videoID, root
}

func getBody(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestArtworkPosterServed(t *testing.T) {
	srv, id, _ := newMetadataTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	status, body := getBody(t, ts, "/artwork/"+id+"/poster")
	if status != http.StatusOK {
		t.Fatalf("海报状态码 = %d，期望 200", status)
	}
	if body != "poster-bytes" {
		t.Errorf("海报内容错误: %q", body)
	}
}

func TestArtworkBackdropServed(t *testing.T) {
	srv, id, _ := newMetadataTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	status, body := getBody(t, ts, "/artwork/"+id+"/backdrop")
	if status != http.StatusOK {
		t.Fatalf("背景图状态码 = %d，期望 200", status)
	}
	if body != "backdrop-bytes" {
		t.Errorf("背景图内容错误: %q", body)
	}
}

func TestArtworkUnknownVideoAndKind(t *testing.T) {
	srv, id, _ := newMetadataTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	for _, path := range []string{
		"/artwork/nonexistent-id/poster",
		"/artwork/" + id + "/unknown",
		"/artwork/" + id + "/poster/..%2f..%2fsecret.jpg",
	} {
		status, _ := getBody(t, ts, path)
		if status != http.StatusNotFound {
			t.Errorf("%s 状态码 = %d，期望 404", path, status)
		}
	}
}

func TestMetadataAPI(t *testing.T) {
	srv, id, _ := newMetadataTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	status, body := getBody(t, ts, "/api/v1/videos/"+id+"/metadata")
	if status != http.StatusOK {
		t.Fatalf("元信息状态码 = %d，期望 200", status)
	}
	for _, want := range []string{"服务层电影", "2024", "简介", `"source":"nfo"`} {
		if !strings.Contains(body, want) {
			t.Errorf("元信息响应缺少 %q，实际: %s", want, body)
		}
	}
}

func TestMetadataAPINotFoundWithoutNFO(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "plain.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(lib, thumbs, nil, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ts := newHTTPTestServer(t, srv.Handler())

	status, _ := getBody(t, ts, "/api/v1/videos/"+lib.List()[0].ID+"/metadata")
	if status != http.StatusNotFound {
		t.Errorf("无元信息时期望 404，实际 %d", status)
	}
}
