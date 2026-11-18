package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// ffmpegInPath 判断测试环境是否安装 ffmpeg，缺失则跳过转码集成测试。
func ffmpegInPath() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// makeHTTPTestVideo 用 lavfi 生成 6 秒可真实转码的测试视频。
func makeHTTPTestVideo(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=6",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac",
		path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("生成测试视频失败: %v\n%s", err, out)
	}
}

// newTranscodeHTTPServer 构造启用真实转码器的测试服务，返回测试服务与视频 ID。
func newTranscodeHTTPServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	makeHTTPTestVideo(t, filepath.Join(root, "movie.mkv"))
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	tc, err := media.NewTranscoder(t.TempDir(), "ffmpeg", "ffprobe", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tc.Close)
	srv, err := New(lib, thumbs, tc, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return newHTTPTestServer(t, srv.Handler()), lib.List()[0].ID
}

// 转码器未注入时状态接口返回 503，播放列表与资源路由返回 404。
func TestTranscodeDisabledEndpoints(t *testing.T) {
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

	resp, err := http.Get(ts.URL + "/api/v1/videos/" + videoID + "/transcode")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("未启用转码时期望 503，实际 %d", resp.StatusCode)
	}

	for _, p := range []string{
		"/hls/" + videoID + "/index.m3u8",
		"/hls/" + videoID + "/master.m3u8",
		"/hls/" + videoID + "/sub-0.vtt",
	} {
		resp, err = http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s 无转码器时期望 404，实际 %d", p, resp.StatusCode)
		}
	}
}

// 已启用转码但会话尚未启动时，HLS 路由统一 404，且不允许路径穿越。
func TestHLSRoutesWithoutSession(t *testing.T) {
	ts, videoID := newTranscodeHTTPServer(t)
	for _, p := range []string{
		"/hls/" + videoID + "/index.m3u8",
		"/hls/" + videoID + "/master.m3u8",
		"/hls/" + videoID + "/out-0-00000.ts",
		"/hls/" + videoID + "/sub-0.vtt",
		"/hls/" + videoID + "/../ffmpeg.log",
		"/hls/unknown/index.m3u8",
	} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatalf("请求 %s 失败: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s 期望 404，实际 %d", p, resp.StatusCode)
		}
	}
}

// 真实转码端到端：状态接口启动会话，入口播放列表与产物 Content-Type 正确。
func TestTranscodeHTTPEndToEnd(t *testing.T) {
	if !ffmpegInPath() {
		t.Skip("环境无 ffmpeg，跳过转码 HTTP 集成测试")
	}
	ts, videoID := newTranscodeHTTPServer(t)

	// 触发转码并轮询直到产出播放列表
	deadline := time.Now().Add(60 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/v1/videos/" + videoID + "/transcode")
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("状态接口期望 200，实际 %d", resp.StatusCode)
		}
		var st struct {
			State       string `json:"state"`
			HasPlaylist bool   `json:"hasPlaylist"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			t.Fatalf("解析状态失败: %v", err)
		}
		resp.Body.Close()
		if st.State == "failed" {
			t.Fatal("转码会话进入 failed")
		}
		if st.HasPlaylist {
			ready = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !ready {
		t.Fatal("超时未等到播放列表")
	}

	// 入口播放列表：m3u8 类型且禁缓存
	resp, err := http.Get(ts.URL + "/hls/" + videoID + "/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("播放列表期望 200，实际 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Errorf("播放列表 Content-Type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("播放列表 Cache-Control = %q，期望 no-store", cc)
	}

	// 白名单外文件一律 404（日志不可经 HTTP 读取）
	resp2, err := http.Get(ts.URL + "/hls/" + videoID + "/ffmpeg.log")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("ffmpeg.log 期望 404，实际 %d", resp2.StatusCode)
	}
}
