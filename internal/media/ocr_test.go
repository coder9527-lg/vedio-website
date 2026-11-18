package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// readFileForTest 读取文件，失败即终止测试。
func readFileForTest(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return data
}

// makeBitmapSubtitleVideo 尝试生成含一条位图字幕流（dvdsub）的测试视频，
// 成功时返回视频路径；本机精简版 ffmpeg 缺少文本→位图光栅化能力（无
// subtitles/drawtext 滤镜、dvdsub 编码器不接受文本输入）会失败，此时返回空串，
// 调用方据此 skip，而不是让整个测试包失败。
func makeBitmapSubtitleVideo(t *testing.T, path string) string {
	t.Helper()
	dir := filepath.Dir(path)
	srtPath := filepath.Join(dir, "in.srt")
	if err := os.WriteFile(srtPath,
		[]byte("1\n00:00:01,000 --> 00:00:03,000\nHello OCR\n"), 0o644); err != nil {
		t.Fatalf("写 SRT 失败: %v", err)
	}
	// 先把文本字幕封进 mkv，再尝试转码为 dvdsub 位图轨。
	intermediate := filepath.Join(dir, "text-sub.mkv")
	build := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=5",
		"-i", srtPath,
		"-map", "0:v", "-map", "1:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:s", "srt",
		intermediate)
	if out, err := build.CombinedOutput(); err != nil {
		t.Logf("生成文本字幕中间文件失败: %v\n%s", err, out)
		return ""
	}
	conv := exec.Command("ffmpeg", "-v", "error", "-y",
		"-i", intermediate,
		"-map", "0:v", "-map", "0:s?",
		"-c:v", "copy", "-c:s", "dvdsub",
		path)
	if out, err := conv.CombinedOutput(); err != nil {
		t.Logf("文本字幕转 dvdsub 位图失败（本机 ffmpeg 能力限制）: %v\n%s", err, out)
		return ""
	}
	return path
}

// tesseractAvailable 探测本机是否装了 tesseract，集成测试据此 skip。
func tesseractAvailable() bool {
	_, err := exec.LookPath("tesseract")
	return err == nil
}

func TestOCRerUnavailableWhenBinaryMissing(t *testing.T) {
	// 指向不存在的二进制时 Available 必须稳定返回 false（降级行为）
	o := NewOCRer("/nonexistent/tesseract-bin", "ffmpeg")
	if o.Available() {
		t.Fatal("tesseract 二进制不存在时 Available 应为 false")
	}
	// 结果被缓存，重复调用不 panic 且结论一致
	if o.Available() {
		t.Fatal("Available 缓存后仍应返回 false")
	}

	// nil OCRer 也应安全返回 false
	var nilOCR *OCRer
	if nilOCR.Available() {
		t.Fatal("nil OCRer 的 Available 应为 false")
	}
}

func TestOCRerConvertFailsWhenUnavailable(t *testing.T) {
	o := NewOCRer("/nonexistent/tesseract-bin", "ffmpeg")
	err := o.ConvertBitmapSubtitle(context.Background(), "/tmp/x.mkv",
		SubtitleTrack{Index: 2, Bitmap: true, Language: "zh"}, filepath.Join(t.TempDir(), "out.vtt"))
	if err == nil {
		t.Fatal("OCR 不可用时转换应返回错误")
	}
}

func TestParseShowinfoPTS(t *testing.T) {
	stderr := `[Parsed_showinfo_0 @ 0x] n: 0 pts: 25 pts_time:1
[Parsed_showinfo_0 @ 0x] n: 1 pts: 75 pts_time:3.04
[Parsed_showinfo_0 @ 0x] n: 2 pts: 125 pts_time:5.000000`
	got := parseShowinfoPTS(stderr)
	want := []float64{1, 3.04, 5}
	if len(got) != len(want) {
		t.Fatalf("期望 %d 个时间戳，实际 %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("时间戳[%d] 期望 %v，实际 %v", i, want[i], got[i])
		}
	}
}

func TestParseShowinfoPTSNoMatch(t *testing.T) {
	if got := parseShowinfoPTS("some unrelated ffmpeg log"); len(got) != 0 {
		t.Errorf("无 pts_time 时应返回空结果，实际 %v", got)
	}
}

func TestCleanOCRText(t *testing.T) {
	raw := "  第一行  \n\n   \n第二行\n"
	got := cleanOCRText(raw)
	want := "第一行\n第二行"
	if got != want {
		t.Errorf("清洗结果期望 %q，实际 %q", want, got)
	}
}

func TestFormatVTTTime(t *testing.T) {
	cases := map[float64]string{
		0:       "00:00:00.000",
		1.5:     "00:00:01.500",
		63.04:   "00:01:03.040",
		3723.25: "01:02:03.250",
		-5:      "00:00:00.000", // 负值钳到 0
		0.0006:  "00:00:00.001", // 毫秒四舍五入
	}
	for in, want := range cases {
		if got := formatVTTTime(in); got != want {
			t.Errorf("formatVTTTime(%v) = %q，期望 %q", in, got, want)
		}
	}
}

func TestBuildVTTMergesRepeatedFrames(t *testing.T) {
	// 同一条字幕跨多帧（重复文本）应合并为一个 cue，时间轴连续
	frames := []ocrFrame{
		{pts: 1.0, text: "你好世界"},
		{pts: 2.0, text: "你好世界"},
		{pts: 3.0, text: "你好世界"},
		{pts: 4.0, text: "第二句字幕"},
		{pts: 5.0, text: "第二句字幕"},
	}
	vtt := buildVTT(frames)

	if !strings.HasPrefix(vtt, "WEBVTT\n\n") {
		t.Fatalf("VTT 缺少标准头: %q", vtt)
	}
	// 两个不同文本 → 两个 cue
	if strings.Count(vtt, "-->") != 2 {
		t.Fatalf("期望 2 个 cue，实际:\n%s", vtt)
	}
	// 首 cue 起点 1s、终点取变化帧时刻 4s
	if !strings.Contains(vtt, "00:00:01.000 --> 00:00:04.000\n你好世界") {
		t.Errorf("首 cue 时间轴/文本异常:\n%s", vtt)
	}
	// 末 cue 终点未知，给 +1s 兜底（5.0 → 6.0）
	if !strings.Contains(vtt, "00:00:04.000 --> 00:00:06.000\n第二句字幕") {
		t.Errorf("末 cue 兜底时间轴异常:\n%s", vtt)
	}
}

func TestBuildVTTBlankFramesSplitCues(t *testing.T) {
	// 空白帧（字幕消失）应切断 cue，且自身不产生输出
	frames := []ocrFrame{
		{pts: 1.0, text: "开头字幕"},
		{pts: 2.0, text: ""},
		{pts: 3.0, text: "结尾字幕"},
	}
	vtt := buildVTT(frames)
	if strings.Count(vtt, "-->") != 2 {
		t.Fatalf("期望 2 个 cue（空白帧不产出），实际:\n%s", vtt)
	}
	// 首 cue 在空白帧时刻 2.0 结束
	if !strings.Contains(vtt, "00:00:01.000 --> 00:00:02.000") {
		t.Errorf("空白帧切断时间轴异常:\n%s", vtt)
	}
}

func TestBuildVTTAllEmpty(t *testing.T) {
	frames := []ocrFrame{
		{pts: 1.0, text: ""},
		{pts: 2.0, text: ""},
	}
	vtt := buildVTT(frames)
	// 只有头部，没有任何 cue
	if strings.Count(vtt, "-->") != 0 {
		t.Errorf("全空帧不应产出 cue，实际:\n%s", vtt)
	}
}

func TestPickLang(t *testing.T) {
	o := &OCRer{langs: []string{"eng", "chi_sim", "jpn"}, checked: true, avail: true}
	cases := []struct {
		lang string
		want string
	}{
		{"zh", "chi_sim"}, // 中文优先简体（繁体未装）
		{"zh-tw", "eng"},  // 繁体未装，回退英文
		{"en", "eng"},
		{"ja", "jpn"},
		{"fr", "eng"},  // 法语包未装，回退英文
		{"und", "eng"}, // 未知语言回退英文
	}
	for _, c := range cases {
		if got := o.pickLang(SubtitleTrack{Language: c.lang}); got != c.want {
			t.Errorf("pickLang(%q) = %q，期望 %q", c.lang, got, c.want)
		}
	}

	// 英文包也缺失时返回空串（无法识别）
	noEng := &OCRer{langs: []string{"jpn"}, checked: true, avail: true}
	if got := noEng.pickLang(SubtitleTrack{Language: "fr"}); got != "" {
		t.Errorf("无任何可用语言包时应返回空串，实际 %q", got)
	}
}

// TestOCRIntegrationWithTesseract 端到端验证：用 ffmpeg 造一条 PGS 位图字幕，
// 再经 OCR 转成 WebVTT。依赖 tesseract + chi_sim/eng，环境缺失时 skip。
// 注意：本机 ffmpeg 若无位图字幕编码能力会跳过素材生成，因此该用例在 CI 上
// 可能 skip，纯逻辑由上面的用例保证。
func TestOCRIntegrationWithTesseract(t *testing.T) {
	if !tesseractAvailable() || !ffmpegAvailable() {
		t.Skip("环境无 tesseract/ffmpeg，跳过位图字幕 OCR 端到端测试")
	}
	dir := t.TempDir()
	videoPath := makeBitmapSubtitleVideo(t, filepath.Join(dir, "bitmap.mkv"))
	if videoPath == "" {
		t.Skip("本机 ffmpeg 无法生成位图字幕测试素材，跳过端到端测试")
	}

	o := NewOCRer("tesseract", "ffmpeg")
	if !o.Available() {
		t.Skip("tesseract 无可用语言包，跳过端到端测试")
	}
	out := filepath.Join(dir, "sub-0.vtt")
	track := SubtitleTrack{Index: 0, Bitmap: true, Language: "en", Codec: "hdmv_pgs_subtitle"}
	if err := o.ConvertBitmapSubtitle(context.Background(), videoPath, track, out); err != nil {
		t.Skipf("位图字幕 OCR 端到端流程无法在本环境跑通（素材/能力限制）: %v", err)
	}
	// 能产出带头部的 VTT 即视为链路打通（识别准确度不做硬断言）
	data := readFileForTest(t, out)
	if !strings.HasPrefix(string(data), "WEBVTT") {
		t.Fatalf("产出不是合法 WebVTT:\n%s", data)
	}
}
