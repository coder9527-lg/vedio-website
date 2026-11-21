package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// makeHTTPTestVideoWithSize 生成指定分辨率、6 秒可真实转码的测试视频。
// lavfi 虚拟输入直接作为转码源时，其容器与 filter_complex + var_stream_map 的
// 音频流映射不兼容（ffmpeg 报 Unable to map stream），因此先用 lavfi 编码成
// 普通 mp4，再 stream copy 重封装为目标容器，得到与真实素材一致的文件输入。
func makeHTTPTestVideoWithSize(t *testing.T, path, size string) {
	t.Helper()
	// 中间 mp4 放在目标目录之外，避免被媒体库扫描成第二个视频
	tmpMP4 := filepath.Join(t.TempDir(), "abr-src.mp4")
	gen := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size="+size+":rate=25:duration=6",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac",
		tmpMP4)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("生成中间 mp4 失败: %v\n%s", err, out)
	}
	remux := exec.Command("ffmpeg", "-v", "error", "-y",
		"-i", tmpMP4, "-c", "copy", path)
	if out, err := remux.CombinedOutput(); err != nil {
		t.Fatalf("重封装测试视频失败: %v\n%s", err, out)
	}
}

// newTranscodeHTTPServerWithSize 构造指定源分辨率的转码测试服务。
func newTranscodeHTTPServerWithSize(t *testing.T, size string) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	makeHTTPTestVideoWithSize(t, filepath.Join(root, "movie.mkv"), size)
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

// newTranscodeHTTPServerWithABR 构造指定源分辨率与 ABR 变体档位的转码测试服务。
func newTranscodeHTTPServerWithABR(t *testing.T, size string, abrRaw string) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	makeHTTPTestVideoWithSize(t, filepath.Join(root, "movie.mkv"), size)
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
	tc.SetABRQualities(media.ParseABRQualities(abrRaw))
	srv, err := New(lib, thumbs, tc, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return newHTTPTestServer(t, srv.Handler()), lib.List()[0].ID
}

// waitAutoMaster 轮询 auto 状态接口直到 master 就绪，返回 master 文本内容。
func waitAutoMaster(t *testing.T, ts *httptest.Server, videoID string) string {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/v1/videos/" + videoID + "/transcode?q=auto")
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
			Quality     string `json:"quality"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if st.State == "failed" {
			t.Fatal("auto 转码失败")
		}
		if st.Quality != "" && st.Quality != media.QualityAuto {
			t.Fatalf("状态回显档位 = %q，期望 auto", st.Quality)
		}
		if st.HasPlaylist {
			mresp, err := http.Get(ts.URL + "/hls/" + videoID + "/auto/master.m3u8")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(mresp.Body)
			mresp.Body.Close()
			if mresp.StatusCode != http.StatusOK {
				t.Fatalf("auto master 期望 200，实际 %d", mresp.StatusCode)
			}
			if ct := mresp.Header.Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
				t.Fatalf("master Content-Type = %q", ct)
			}
			if cc := mresp.Header.Get("Cache-Control"); cc != "no-store" {
				t.Fatalf("master Cache-Control = %q，期望 no-store", cc)
			}
			return string(body)
		}
		time.Sleep(400 * time.Millisecond)
	}
	t.Fatal("超时未等到 auto master")
	return ""
}

// TestTranscodeABRAuto1080p 验证 1080p 源的 auto 会话产出多变体 master，
// 纯音频 STREAM-INF 被剔除，变体播放列表/分片可读，且与手动档目录隔离。
func TestTranscodeABRAuto1080p(t *testing.T) {
	if !ffmpegInPath() {
		t.Skip("环境无 ffmpeg，跳过 ABR 集成测试")
	}
	ts, videoID := newTranscodeHTTPServerWithSize(t, "1920x1080")

	master := waitAutoMaster(t, ts, videoID)

	// master 含 3 个带 RESOLUTION 的视频变体，且无纯音频（无 RESOLUTION）STREAM-INF
	if n := strings.Count(master, "RESOLUTION="); n != 3 {
		t.Fatalf("期望 3 个视频变体 RESOLUTION，实际 %d:\n%s", n, master)
	}
	for _, line := range strings.Split(master, "\n") {
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") && !strings.Contains(line, "RESOLUTION") {
			t.Fatalf("master 残留纯音频 STREAM-INF:\n%s", master)
		}
	}
	// BANDWIDTH/CODECS 元信息齐全
	if !strings.Contains(master, "BANDWIDTH=") || !strings.Contains(master, "CODECS=") {
		t.Fatalf("master 缺少 BANDWIDTH/CODECS:\n%s", master)
	}

	// 三个变体播放列表与首个分片均可读
	for _, variant := range []string{"out-0.m3u8", "out-1.m3u8", "out-2.m3u8"} {
		resp, err := http.Get(ts.URL + "/hls/" + videoID + "/auto/" + variant)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s 期望 200，实际 %d", variant, resp.StatusCode)
		}
		seg := ""
		for _, l := range strings.Split(string(body), "\n") {
			if strings.HasSuffix(l, ".ts") {
				seg = l
				break
			}
		}
		if seg == "" {
			t.Fatalf("%s 中未找到分片", variant)
		}
		sresp, err := http.Get(ts.URL + "/hls/" + videoID + "/auto/" + seg)
		if err != nil {
			t.Fatal(err)
		}
		sresp.Body.Close()
		if sresp.StatusCode != http.StatusOK {
			t.Fatalf("分片 %s 期望 200，实际 %d", seg, sresp.StatusCode)
		}
	}

	// 白名单外与路径穿越一律 404
	for _, p := range []string{
		"/hls/" + videoID + "/auto/ffmpeg.log",
		"/hls/" + videoID + "/auto/../ffmpeg.log",
		"/hls/" + videoID + "/auto/evil.txt",
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

	// 目录隔离：auto 就绪不代表手动 720p 会话可读
	resp, err := http.Get(ts.URL + "/hls/" + videoID + "/720p/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未启动的 720p 手动档期望 404，实际 %d", resp.StatusCode)
	}
}

// TestTranscodeABRAutoSmallSource 验证小分辨率源 auto 退化为单变体仍可播且不放大。
func TestTranscodeABRAutoSmallSource(t *testing.T) {
	if !ffmpegInPath() {
		t.Skip("环境无 ffmpeg，跳过 ABR 集成测试")
	}
	ts, videoID := newTranscodeHTTPServerWithSize(t, "320x240")

	master := waitAutoMaster(t, ts, videoID)
	// 320x240 低于全部档位，兜底单变体，master 仅 1 个 RESOLUTION
	if n := strings.Count(master, "RESOLUTION="); n != 1 {
		t.Fatalf("小源期望 1 个兜底变体，实际 %d:\n%s", n, master)
	}
	// 唯一变体仍可访问
	resp, err := http.Get(ts.URL + "/hls/" + videoID + "/auto/out-0.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("兜底变体播放列表期望 200，实际 %d", resp.StatusCode)
	}
}

// TestTranscodeABRAutoAndManualIsolation 验证 auto 与手动档会话键独立、可并存。
func TestTranscodeABRAutoAndManualIsolation(t *testing.T) {
	if !ffmpegInPath() {
		t.Skip("环境无 ffmpeg，跳过 ABR 集成测试")
	}
	ts, videoID := newTranscodeHTTPServerWithSize(t, "1920x1080")

	waitAutoMaster(t, ts, videoID)
	waitPlaylist(t, ts, videoID, "480p")

	// 两类入口各自 200
	for _, p := range []string{
		"/hls/" + videoID + "/auto/master.m3u8",
		"/hls/" + videoID + "/480p/index.m3u8",
	} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s 期望 200，实际 %d", p, resp.StatusCode)
		}
	}
}

// TestTranscodeABRConfiguredQualities 验证 TRANSCODE_ABR_QUALITIES 减档后
// 1080 源也只产出配置的 720p/480p 两个变体，master 不再含 1080 分辨率。
func TestTranscodeABRConfiguredQualities(t *testing.T) {
	if !ffmpegInPath() {
		t.Skip("环境无 ffmpeg，跳过 ABR 集成测试")
	}
	ts, videoID := newTranscodeHTTPServerWithABR(t, "1920x1080", "720p,480p")

	master := waitAutoMaster(t, ts, videoID)
	if n := strings.Count(master, "RESOLUTION="); n != 2 {
		t.Fatalf("配置两档期望 2 个变体，实际 %d:\n%s", n, master)
	}
	if strings.Contains(master, "RESOLUTION=1920x1080") {
		t.Fatalf("减档后不应再产出 1080p 变体:\n%s", master)
	}
	if !strings.Contains(master, "RESOLUTION=1280x720") || !strings.Contains(master, "RESOLUTION=854x480") {
		t.Fatalf("master 应含 720p/480p 两档:\n%s", master)
	}
	// 两个变体播放列表均可访问，第三个不应存在
	for _, variant := range []string{"out-0.m3u8", "out-1.m3u8"} {
		resp, err := http.Get(ts.URL + "/hls/" + videoID + "/auto/" + variant)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s 期望 200，实际 %d", variant, resp.StatusCode)
		}
	}
	resp, err := http.Get(ts.URL + "/hls/" + videoID + "/auto/out-2.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未配置第三档时 out-2.m3u8 期望 404，实际 %d", resp.StatusCode)
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
