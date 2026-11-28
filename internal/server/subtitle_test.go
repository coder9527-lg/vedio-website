package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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
	srv, err := New(lib, thumbs, nil, nil, nil, nil, false)
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

// newSubtitleUploadServer 构造带单个视频的内存库与测试服务器，返回视频 ID 与媒体根目录。
func newSubtitleUploadServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	root := t.TempDir()
	videoPath := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(videoPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	vid := lib.List()[0]
	thumbs, _ := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	srv, err := New(lib, thumbs, nil, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return newHTTPTestServer(t, srv.Handler()), root, vid.ID
}

// uploadSubtitle 构造 multipart 上传请求并返回响应。
func uploadSubtitle(t *testing.T, ts *httptest.Server, videoID, filename, lang string, content []byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if lang != "" {
		_ = mw.WriteField("lang", lang)
	}
	if filename != "" {
		part, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/videos/"+videoID+"/subtitles", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// listSubtitleIDs 拉取字幕列表并返回全部字幕 ID。
func listSubtitleIDs(t *testing.T, ts *httptest.Server, videoID string) []string {
	t.Helper()
	resp, err := http.Get(ts.URL + "/api/v1/videos/" + videoID + "/subtitles")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("字幕列表状态码 = %d", resp.StatusCode)
	}
	var payload struct {
		Subtitles []struct {
			ID   string `json:"id"`
			Lang string `json:"lang"`
		} `json:"subtitles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(payload.Subtitles))
	for _, s := range payload.Subtitles {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestSubtitleUpload(t *testing.T) {
	ts, root, vid := newSubtitleUploadServer(t)
	srt := []byte("1\n00:00:01,000 --> 00:00:02,000\n你好\n")

	// happy path：srt + zh-Hans，落盘命名为 Movie.zh-Hans.srt 且立即可被发现
	resp := uploadSubtitle(t, ts, vid, "随便什么名字.srt", "zh-Hans", srt)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("上传状态码 = %d，body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()
	if _, err := os.Stat(filepath.Join(root, "Movie.zh-Hans.srt")); err != nil {
		t.Fatalf("规范命名的字幕文件应存在: %v", err)
	}
	if ids := listSubtitleIDs(t, ts, vid); len(ids) != 1 {
		t.Fatalf("上传后期望 1 个字幕，实际 %d", len(ids))
	}

	// 四种受支持格式全部接受
	for _, c := range []struct{ name, wantFile string }{
		{"a.vtt", "Movie.en.vtt"},
		{"a.ass", "Movie.ja.ass"},
		{"a.ssa", "Movie.ko.ssa"},
	} {
		resp := uploadSubtitle(t, ts, vid, c.name, strings.Split(c.wantFile, ".")[1], srt)
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("%s 上传应 201，got %d", c.name, resp.StatusCode)
		}
		resp.Body.Close()
		if _, err := os.Stat(filepath.Join(root, c.wantFile)); err != nil {
			t.Errorf("期望落盘 %s: %v", c.wantFile, err)
		}
	}

	// 同名冲突：再传一次 zh-Hans srt，应加 .2 后缀且两条都在列表
	resp = uploadSubtitle(t, ts, vid, "dup.srt", "zh-Hans", srt)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("冲突上传应 201，got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if _, err := os.Stat(filepath.Join(root, "Movie.zh-Hans.2.srt")); err != nil {
		t.Fatalf("冲突文件应加数字后缀: %v", err)
	}
	if ids := listSubtitleIDs(t, ts, vid); len(ids) != 5 {
		t.Fatalf("冲突后期望 5 个字幕，实际 %d", len(ids))
	}

	// 拒收序列：错误格式、缺文件、非法语言（含路径注入串）
	for _, c := range []struct {
		filename, lang string
		wantStatus     int
	}{
		{"a.txt", "zh-Hans", http.StatusBadRequest},
		{"", "zh-Hans", http.StatusBadRequest},
		{"a.srt", "../evil", http.StatusBadRequest},
		{"a.srt", "fr-CA-unknown", http.StatusBadRequest},
	} {
		resp := uploadSubtitle(t, ts, vid, c.filename, c.lang, srt)
		if resp.StatusCode != c.wantStatus {
			t.Errorf("filename=%q lang=%q 期望 %d，got %d", c.filename, c.lang, c.wantStatus, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// ghost 视频 → 404
	resp = uploadSubtitle(t, ts, "ghost-id", "a.srt", "zh-Hans", srt)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("ghost 视频应 404，got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 超 10MiB → 413，且不产生落盘文件
	big := bytes.Repeat([]byte("x"), maxSubtitleUpload+1)
	resp = uploadSubtitle(t, ts, vid, "big.srt", "en", big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("超大字幕应 413，got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if entries, _ := os.ReadDir(root); len(entries) != 6 { // 1 视频 + 5 字幕，超大文件不落盘
		t.Fatalf("超大上传不应落盘，目录条目数 = %d", len(entries))
	}
}

func TestSubtitleDelete(t *testing.T) {
	ts, root, vid := newSubtitleUploadServer(t)
	srt := []byte("1\n00:00:01,000 --> 00:00:02,000\n你好\n")

	// 无字幕时删除不存在 sid → 404
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/videos/"+vid+"/subtitles/nope", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在字幕应 404，got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 上传两个字幕
	uploadSubtitle(t, ts, vid, "a.srt", "zh-Hans", srt).Body.Close()
	uploadSubtitle(t, ts, vid, "b.srt", "en", srt).Body.Close()
	ids := listSubtitleIDs(t, ts, vid)
	if len(ids) != 2 {
		t.Fatalf("期望 2 个字幕，实际 %d", len(ids))
	}

	// 删除命中的第一个：200、文件消失、列表减 1
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/videos/"+vid+"/subtitles/"+ids[0], nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("删除应 200，got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := listSubtitleIDs(t, ts, vid); len(got) != 1 || got[0] == ids[0] {
		t.Fatalf("删除后列表异常: %v", got)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 2 { // 1 视频 + 1 字幕
		t.Fatalf("删除后目录条目数 = %d，期望 2", len(entries))
	}

	// 再删同一 sid：索引已刷新，应 404
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/videos/"+vid+"/subtitles/"+ids[0], nil)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("已删除字幕再删应 404，got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// ghost 视频 → 404
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/videos/ghost-id/subtitles/x", nil)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("ghost 视频应 404，got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSubtitleManagerInjection(t *testing.T) {
	// store=nil、无字幕视频：字幕菜单与管理弹窗仍须渲染（菜单常显）
	ts, _, vid := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		`id="sub-menu-wrap"`,
		`id="sub-mgr-modal"`,
		`id="sub-upload-lang"`,
		`id="sub-upload-file"`,
		`id="sub-upload-btn"`,
		"管理字幕…",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少注入内容 %q", want)
		}
	}
}

// TestSubtitleDelayModuleInjection 字幕音画同步模块是独立共享脚本，
// 无论是否存在外挂字幕都必须渲染，并暴露 mount/applyAll 供字幕菜单回调。
func TestSubtitleDelayModuleInjection(t *testing.T) {
	// 无字幕视频：走第三分支，共享延迟模块仍在（面板挂载由另两个字幕分支负责）
	ts, _, vid := newSubtitleUploadServer(t)
	_, html := getHTML(t, ts, "/watch/"+vid)
	for _, want := range []string{
		"window.SubDelay = (() => {",
		"data-sub-delay-minus",
		"data-sub-delay-plus",
		"data-sub-delay-reset",
		// 回归：本脚本在字幕菜单之后，必须自检挂载，否则菜单首次 buildMenu 时模块还未定义
		"mount();\n      applyAll();",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("无字幕播放页缺少字幕延迟模块标记 %q", want)
		}
	}
}

// TestSubtitleDelayModuleWithExternalSubs 外挂字幕分支必须挂载延迟面板并在切轨时重放偏移。
func TestSubtitleDelayModuleWithExternalSubs(t *testing.T) {
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mkv")
	if err := os.WriteFile(videoPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "movie.zh.srt"),
		[]byte("1\n00:00:01,000 --> 00:00:02,000\n你好\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	vid := lib.List()[0].ID
	thumbs, _ := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	srv, err := New(lib, thumbs, nil, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ts := newHTTPTestServer(t, srv.Handler())

	_, html := getHTML(t, ts, "/watch/"+vid)
	for _, want := range []string{
		"window.SubDelay = (() => {",
		"if (window.SubDelay) window.SubDelay.mount();",
		"if (window.SubDelay) window.SubDelay.applyAll();",
		// PlayerPrefs 对白名单字段 subDelay 的有限数值校验
		"typeof obj.subDelay === 'number' && isFinite(obj.subDelay)",
		// 偏移经 VTTCue 时间轴平移生效
		"cue.startTime += delta",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("外挂字幕播放页缺少 %q", want)
		}
	}
}
