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

// 转码器未注入时状态接口返回 503，按档位的播放列表与共享字幕路由返回 404。
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
		"/hls/" + videoID + "/720p/index.m3u8",
		"/hls/" + videoID + "/720p/master.m3u8",
		"/hls/" + videoID + "/subs/sub-0.vtt",
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
		"/hls/" + videoID + "/720p/index.m3u8",
		"/hls/" + videoID + "/720p/master.m3u8",
		"/hls/" + videoID + "/720p/out-0-00000.ts",
		"/hls/" + videoID + "/subs/sub-0.vtt",
		"/hls/" + videoID + "/720p/../ffmpeg.log",
		"/hls/unknown/720p/index.m3u8",
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

// 真实转码端到端：带 ?q= 的状态接口启动会话，按档位的入口播放列表与产物 Content-Type 正确。
func TestTranscodeHTTPEndToEnd(t *testing.T) {
	if !ffmpegInPath() {
		t.Skip("环境无 ffmpeg，跳过转码 HTTP 集成测试")
	}
	ts, videoID := newTranscodeHTTPServer(t)
	statusURL := ts.URL + "/api/v1/videos/" + videoID + "/transcode?q=720p"

	// 触发转码并轮询直到产出播放列表
	deadline := time.Now().Add(60 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(statusURL)
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
	resp, err := http.Get(ts.URL + "/hls/" + videoID + "/720p/index.m3u8")
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
	resp2, err := http.Get(ts.URL + "/hls/" + videoID + "/720p/ffmpeg.log")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("ffmpeg.log 期望 404，实际 %d", resp2.StatusCode)
	}
}

// 非法清晰度档位由后端归一化为 720p：状态接口不报错，且能取到 720p 会话的播放列表。
func TestTranscodeInvalidQualityFallback(t *testing.T) {
	if !ffmpegInPath() {
		t.Skip("环境无 ffmpeg，跳过转码 HTTP 集成测试")
	}
	ts, videoID := newTranscodeHTTPServer(t)

	deadline := time.Now().Add(60 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/v1/videos/" + videoID + "/transcode?q=4k")
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("非法档位应被归一化而非报错，实际状态 %d", resp.StatusCode)
		}
		var st struct {
			State       string `json:"state"`
			HasPlaylist bool   `json:"hasPlaylist"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if st.HasPlaylist {
			ready = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !ready {
		t.Fatal("超时未等到归一化档位的播放列表")
	}

	resp, err := http.Get(ts.URL + "/hls/" + videoID + "/720p/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("归一化到 720p 后播放列表期望 200，实际 %d", resp.StatusCode)
	}
}

// 不同清晰度档位的会话与产物目录互相隔离：480p 未启动时其路由 404，启动后独立可访问。
func TestTranscodeQualityDirectoryIsolation(t *testing.T) {
	if !ffmpegInPath() {
		t.Skip("环境无 ffmpeg，跳过转码 HTTP 集成测试")
	}
	ts, videoID := newTranscodeHTTPServer(t)

	// 先只启动 720p
	waitPlaylist(t, ts, videoID, "720p")

	// 480p 尚未启动，对应路由必须 404（不能串读到 720p 的产物）
	resp, err := http.Get(ts.URL + "/hls/" + videoID + "/480p/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未启动的 480p 档位期望 404，实际 %d", resp.StatusCode)
	}

	// 启动 480p 后独立可访问
	waitPlaylist(t, ts, videoID, "480p")
	resp2, err := http.Get(ts.URL + "/hls/" + videoID + "/480p/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("480p 播放列表期望 200，实际 %d", resp2.StatusCode)
	}
}

// 共享内嵌字幕：字幕产物位于视频级 subs/ 目录，不随档位变化，文件名白名单外一律 404。
func TestTranscodeSharedSubtitleRoute(t *testing.T) {
	if !ffmpegInPath() {
		t.Skip("环境无 ffmpeg，跳过转码 HTTP 集成测试")
	}
	ts, videoID := newTranscodeHTTPServer(t)
	waitPlaylist(t, ts, videoID, "720p")

	// 测试视频无内嵌字幕，subs 路由应 404（验证路由与白名单生效，而非误放行）
	for _, p := range []string{
		"/hls/" + videoID + "/subs/sub-0.vtt",
		"/hls/" + videoID + "/subs/../720p/ffmpeg.log",
		"/hls/" + videoID + "/subs/evil.txt",
	} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s 期望 404，实际 %d", p, resp.StatusCode)
		}
	}
}

// waitPlaylist 轮询指定档位的状态接口直到产出播放列表或超时。
func waitPlaylist(t *testing.T, ts *httptest.Server, videoID, quality string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/v1/videos/" + videoID + "/transcode?q=" + quality)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			time.Sleep(300 * time.Millisecond)
			continue
		}
		var st struct {
			State       string `json:"state"`
			HasPlaylist bool   `json:"hasPlaylist"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if st.State == "failed" {
			t.Fatalf("档位 %s 转码失败", quality)
		}
		if st.HasPlaylist {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("超时未等到档位 %s 的播放列表", quality)
}
