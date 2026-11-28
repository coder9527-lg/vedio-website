package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// 转码未启用时：管理页渲染不可用提示，列表接口 503。
func TestTranscodeAdminDisabled(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.mkv"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	thumbs, _ := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	srv, err := New(lib, thumbs, nil, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ts := newHTTPTestServer(t, srv.Handler())
	videoID := lib.List()[0].ID

	resp, err := http.Get(ts.URL + "/transcode/admin")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("管理页期望 200，实际 %d", resp.StatusCode)
	}
	if !contains(body, "转码功能未启用") {
		t.Error("未启用时应渲染不可用提示")
	}

	resp, err = http.Get(ts.URL + "/api/v1/transcode/sessions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("未启用时列表接口期望 503，实际 %d", resp.StatusCode)
	}

	resp, err = http.Post(ts.URL+"/api/v1/transcode/sessions/"+videoID+"/stop?q=720p", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("未启用时终止接口期望 503，实际 %d", resp.StatusCode)
	}
}

// 启用转码器但无会话时：页面渲染管理骨架，列表返回空会话数组。
func TestTranscodeAdminEmptyList(t *testing.T) {
	if !ffmpegInPath() {
		t.Skip("环境无 ffmpeg，跳过转码管理接口测试")
	}
	root := t.TempDir()
	makeHTTPTestVideo(t, filepath.Join(root, "movie.mkv"))
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	tc, err := media.NewTranscoder(t.TempDir(), "ffmpeg", "ffprobe", 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tc.Close)
	srv, err := New(lib, thumbs, tc, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ts := newHTTPTestServer(t, srv.Handler())

	resp, err := http.Get(ts.URL + "/transcode/admin")
	if err != nil {
		t.Fatal(err)
	}
	page := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !contains(page, "转码任务") || !contains(page, "data-tc-rows") {
		t.Errorf("管理页骨架渲染异常 status=%d", resp.StatusCode)
	}

	resp, err = http.Get(ts.URL + "/api/v1/transcode/sessions")
	if err != nil {
		t.Fatal(err)
	}
	var snap media.TranscodeSummary
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("解析快照失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列表接口期望 200，实际 %d", resp.StatusCode)
	}
	if !snap.Available || snap.MaxConcurrent != 2 {
		t.Errorf("快照配置异常: available=%v max=%d", snap.Available, snap.MaxConcurrent)
	}
	if len(snap.Sessions) != 0 {
		t.Errorf("无会话时期望空数组，实际 %d 条", len(snap.Sessions))
	}
}

// 终止不存在的会话：视频存在但无该档位会话返回 404；视频不存在同样 404。
func TestTranscodeAdminStopNotFound(t *testing.T) {
	if !ffmpegInPath() {
		t.Skip("环境无 ffmpeg，跳过转码管理接口测试")
	}
	root := t.TempDir()
	makeHTTPTestVideo(t, filepath.Join(root, "movie.mkv"))
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	thumbs, _ := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	tc, err := media.NewTranscoder(t.TempDir(), "ffmpeg", "ffprobe", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tc.Close)
	srv, err := New(lib, thumbs, tc, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ts := newHTTPTestServer(t, srv.Handler())
	videoID := lib.List()[0].ID

	resp, err := http.Post(ts.URL+"/api/v1/transcode/sessions/"+videoID+"/stop?q=720p", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("无会话时期望 404，实际 %d", resp.StatusCode)
	}

	resp, err = http.Post(ts.URL+"/api/v1/transcode/sessions/does-not-exist/stop?q=720p", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("视频不存在时期望 404，实际 %d", resp.StatusCode)
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
