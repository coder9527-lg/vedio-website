package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// newSubtitleServer 构造含一个视频与若干字幕文件的测试服务，返回服务与视频 ID。
func newSubtitleServer(t *testing.T, files map[string]string) (*Server, string, string) {
	t.Helper()
	root := t.TempDir()
	full := map[string]string{"movie.mp4": "fake-video"}
	for rel, content := range files {
		full[rel] = content
	}
	for rel, content := range full {
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
	videos := lib.List()
	if len(videos) != 1 {
		t.Fatalf("期望 1 个视频，实际 %d", len(videos))
	}

	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatalf("NewThumbnailer 失败: %v", err)
	}
	subs, err := media.NewSubtitleConverter(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatalf("NewSubtitleConverter 失败: %v", err)
	}
	srv, err := New(lib, thumbs, nil, subs, nil, nil, false)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return srv, videos[0].ID, root
}

func TestSubtitleListAPI(t *testing.T) {
	srv, videoID, _ := newSubtitleServer(t, map[string]string{
		"movie.zh.srt": "1\n00:00:01,000 --> 00:00:02,000\n你好\n",
		"movie.en.vtt": "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHello\n",
	})
	ts := newHTTPTestServer(t, srv.Handler())

	// 未知视频 → 404
	resp, err := http.Get(ts.URL + "/api/v1/videos/unknown/subtitles")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("未知视频期望 404，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/api/v1/videos/" + videoID + "/subtitles")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", resp.StatusCode)
	}
	var data struct {
		Subtitles []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Lang   string `json:"lang"`
			Format string `json:"format"`
		} `json:"subtitles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(data.Subtitles) != 2 {
		t.Fatalf("期望 2 条字幕，实际 %d", len(data.Subtitles))
	}
	// 简中按排序规则应排第一且为默认
	if data.Subtitles[0].Lang != "zh-CN" || data.Subtitles[0].Format != "srt" {
		t.Errorf("首条字幕不符合预期: %+v", data.Subtitles[0])
	}
}

func TestSubtitleTrackVTT(t *testing.T) {
	srv, videoID, _ := newSubtitleServer(t, map[string]string{
		"movie.en.vtt": "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHello\n",
	})
	ts := newHTTPTestServer(t, srv.Handler())

	v := srv.library.List()[0]
	subs := srv.library.FindSubtitles(v)
	if len(subs) != 1 {
		t.Fatalf("期望 1 条字幕，实际 %d", len(subs))
	}
	subID := subs[0].ID

	resp, err := http.Get(ts.URL + "/subtitles/" + videoID + "/" + subID)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/vtt") {
		t.Errorf("Content-Type 异常: %q", ct)
	}
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	if !strings.HasPrefix(string(body[:n]), "WEBVTT") {
		t.Errorf("VTT 内容异常: %q", string(body[:n]))
	}

	// 未知字幕 ID → 404
	resp2, err := http.Get(ts.URL + "/subtitles/" + videoID + "/deadbeefdeadbeef")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("未知字幕期望 404，实际 %d", resp2.StatusCode)
	}
	resp2.Body.Close()
}

func TestSubtitleTrackSRTConversion(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("本机无 ffmpeg，跳过 SRT 转换测试")
	}
	srv, videoID, _ := newSubtitleServer(t, map[string]string{
		"movie.zh.srt": "1\n00:00:01,000 --> 00:00:03,000\n你好\n\n2\n00:00:04,000 --> 00:00:06,000\n世界\n",
	})
	ts := newHTTPTestServer(t, srv.Handler())

	sub := srv.library.FindSubtitles(srv.library.List()[0])[0]
	resp, err := http.Get(ts.URL + "/subtitles/" + videoID + "/" + sub.ID)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	if !strings.HasPrefix(string(body), "WEBVTT") {
		t.Errorf("转换结果不是 VTT: %q", string(body[:min(32, len(body))]))
	}
}

func TestSubtitleTrackRequiresAuth(t *testing.T) {
	// 开启访问密码后，未登录访问字幕 API 应返回 401 JSON
	_, ts := newTestServer(t, "pass123")
	resp, err := http.Get(ts.URL + "/api/v1/videos/whatever/subtitles")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("未登录期望 401，实际 %d", resp.StatusCode)
	}
}
