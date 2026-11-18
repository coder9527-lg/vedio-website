package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

func TestSRTToVTT(t *testing.T) {
	srt := []byte("1\n00:00:01,000 --> 00:00:02,500\n你好\n\n2\n00:00:03,000 --> 00:00:04,000\nWorld\n")
	got := string(convertToVTT(srt, "srt"))
	if !strings.HasPrefix(got, "WEBVTT\n") {
		t.Fatalf("VTT 缺少文件头: %q", got)
	}
	if strings.Contains(got, "\n1\n") {
		t.Errorf("SRT 序号行应被移除: %q", got)
	}
	if !strings.Contains(got, "00:00:01.000 --> 00:00:02.500") {
		t.Errorf("时间戳逗号应改为点号: %q", got)
	}
	if !strings.Contains(got, "你好") || !strings.Contains(got, "World") {
		t.Errorf("字幕文本丢失: %q", got)
	}
}

func TestVSSToVTTPassthrough(t *testing.T) {
	vtt := []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHi\n")
	if got := string(convertToVTT(vtt, "vtt")); got != string(vtt) {
		t.Errorf("原生 VTT 应原样透传，got=%q", got)
	}
}

func TestASSToVTT(t *testing.T) {
	ass := []byte(strings.Join([]string{
		"[Script Info]",
		"Title: test",
		"",
		"[Events]",
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text",
		"Dialogue: 0,0:00:01.00,0:00:02.50,Default,,0,0,0,,{\\an8}第一句",
		"Dialogue: 0,0:00:03.00,0:00:04.00,Default,,0,0,0,,第二行\\N第三行",
	}, "\r\n"))
	got := string(convertToVTT(ass, "ass"))
	if !strings.HasPrefix(got, "WEBVTT\n") {
		t.Fatalf("缺少 VTT 头: %q", got)
	}
	if !strings.Contains(got, "00:00:01.000 --> 00:00:02.500") {
		t.Errorf("ASS 时间转换错误: %q", got)
	}
	if strings.Contains(got, "{\\an8}") {
		t.Errorf("ASS 样式覆盖块应被剥离: %q", got)
	}
	if !strings.Contains(got, "第一句") {
		t.Errorf("对白文本丢失: %q", got)
	}
	if !strings.Contains(got, "第二行\n第三行") {
		t.Errorf("ASS \\\\N 换行应转换: %q", got)
	}
	if strings.Contains(got, "Script Info") {
		t.Errorf("非 Dialogue 行不应进入 VTT: %q", got)
	}
}

func TestSubtitleHTTPEndpoints(t *testing.T) {
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mkv")
	if err := os.WriteFile(videoPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	srtPath := filepath.Join(root, "movie.zh.srt")
	if err := os.WriteFile(srtPath,
		[]byte("1\n00:00:01,000 --> 00:00:02,000\n你好\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assPath := filepath.Join(root, "movie.en.ass")
	if err := os.WriteFile(assPath,
		[]byte("[Events]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	vid := lib.List()[0]
	subtitles := lib.Subtitles(vid.ID)
	if len(subtitles) != 2 {
		t.Fatalf("期望 2 个字幕，实际 %d", len(subtitles))
	}

	thumbs, _ := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	srv, err := New(lib, thumbs, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ts := newHTTPTestServer(t, srv.Handler())

	// 列表接口
	resp, err := http.Get(ts.URL + "/api/v1/videos/" + vid.ID + "/subtitles")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("字幕列表状态码 = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 各字幕均能取到 VTT
	for _, sub := range subtitles {
		resp, err := http.Get(ts.URL + "/subtitles/" + vid.ID + "/" + sub.ID + "/track.vtt")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("字幕轨道状态码 = %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/vtt") {
			t.Fatalf("Content-Type 应为 text/vtt，got %q", ct)
		}
		if !strings.HasPrefix(string(body), "WEBVTT") {
			t.Fatalf("响应应为 WebVTT，got %q", string(body)[:min(40, len(body))])
		}
	}

	// 不存在的字幕 ID → 404，且路径穿越被拒
	for _, bad := range []string{
		"/subtitles/" + vid.ID + "/nonexistent/track.vtt",
		"/subtitles/" + vid.ID + "/..%2f..%2fetc/track.vtt",
	} {
		resp, err := http.Get(ts.URL + bad)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s 应返回 404，got %d", bad, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestASSTimeToVTT(t *testing.T) {
	cases := map[string]string{
		"0:00:01.50":  "00:00:01.500",
		"1:02:03.05":  "01:02:03.050",
		"10:20:30.00": "10:20:30.000",
	}
	for in, want := range cases {
		if got := assTimeToVTT(in); got != want {
			t.Errorf("assTimeToVTT(%q)=%q, want %q", in, got, want)
		}
	}
	if assTimeToVTT("bad") != "" {
		t.Error("非法时间应返回空串")
	}
}
