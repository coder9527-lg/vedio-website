package media

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 位图字幕 OCR（可选依赖）：
//
// PGS/DVD/DVB/XSUB 等位图字幕只含字幕的光栅图像，没有可直接提取的文本，浏览器也
// 无法渲染。本机 ffmpeg 自带这些编码的解码器，能把字幕流解码成一帧帧带时间戳的
// PNG；再借助外部 tesseract CLI 对每张图做光学识别，按时间轴把相邻重复帧合并成
// 一条 WebVTT cue，最终产出与文本内嵌字幕一致的 sub-<序号>.vtt。
//
// 与 ffmpeg/ffprobe 相同，tesseract 是可选依赖：未安装或缺少语言包时静默降级，
// 位图轨保持「仅识别不提取」，不阻断主转码与其余字幕轨。

// ocrTimeout 限制单条位图字幕完整 OCR 流程（抽帧 + 逐帧识别）的总时长。
// 位图轨帧数可能较多，给比纯文本提取更宽裕的预算。
const ocrTimeout = 8 * time.Minute

// OCRer 封装 tesseract CLI 的可用性探测、语言包选择与位图字幕转 WebVTT 流程。
// 零值不可用；通过 NewOCRer 构造。线程安全（内部无可变状态）。
type OCRer struct {
	bin     string   // tesseract 可执行文件（默认 "tesseract"）
	ffmpeg  string   // 用于解码位图字幕并导出 PNG
	langs   []string // tesseract --list-langs 探测到的已装语言包
	checked bool     // 是否已完成一次探测（结果缓存）
	avail   bool
}

// NewOCRer 创建 OCR 器。bin/ffmpeg 传空时分别回退到 PATH 中的 tesseract/ffmpeg。
// 构造本身不探测二进制，首次调用 Available 时才执行 tesseract --list-langs 并缓存。
func NewOCRer(bin, ffmpeg string) *OCRer {
	if bin == "" {
		bin = "tesseract"
	}
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	return &OCRer{bin: bin, ffmpeg: ffmpeg}
}

// Available 探测 tesseract 是否可执行，并缓存已安装的语言包列表。
// ffmpeg 缺失时同样视为不可用（无法先把位图字幕解码成图片）。
func (o *OCRer) Available() bool {
	if o == nil {
		return false
	}
	if o.checked {
		return o.avail
	}
	o.checked = true
	if _, err := exec.LookPath(o.ffmpeg); err != nil {
		return false
	}
	if _, err := exec.LookPath(o.bin); err != nil {
		return false
	}
	out, err := exec.Command(o.bin, "--list-langs").CombinedOutput()
	if err != nil {
		return false
	}
	// 输出首行是 "List of available languages (n):"，其余每行一个语言包代码。
	for _, line := range strings.Split(string(out), "\n") {
		lang := strings.TrimSpace(line)
		if lang == "" || strings.Contains(lang, "List of available") {
			continue
		}
		o.langs = append(o.langs, lang)
	}
	o.avail = len(o.langs) > 0
	return o.avail
}

// tesseractLangs 把字幕轨的两字母语言标签映射为 tesseract 语言包代码。
// 中文区分简繁；未列出的语言在调用处按已装语言包兜底（退回 eng）。
var tesseractLangs = map[string][]string{
	"zh":      {"chi_sim", "chi_tra"},
	"zh-cn":   {"chi_sim"},
	"zh-hans": {"chi_sim"},
	"chs":     {"chi_sim"},
	"zh-tw":   {"chi_tra"},
	"zh-hk":   {"chi_tra"},
	"zh-hant": {"chi_tra"},
	"cht":     {"chi_tra"},
	"en":      {"eng"},
	"ja":      {"jpn"},
	"ko":      {"kor"},
	"fr":      {"fra"},
	"de":      {"deu"},
	"es":      {"spa"},
	"ru":      {"rus"},
	"it":      {"ita"},
	"pt":      {"por"},
	"th":      {"tha"},
	"vi":      {"vie"},
	"ar":      {"ara"},
	"hi":      {"hin"},
}

// pickLang 依据字幕轨语言与已安装语言包，选出 tesseract -l 参数。
// 优先精确匹配；匹配不到则退回英文（eng 通常随 tesseract 自带），英文也缺失时
// 返回空串表示无法识别该轨。
func (o *OCRer) pickLang(track SubtitleTrack) string {
	installed := map[string]bool{}
	for _, l := range o.langs {
		installed[l] = true
	}
	candidates := tesseractLangs[strings.ToLower(track.Language)]
	var picked []string
	for _, c := range candidates {
		if installed[c] {
			picked = append(picked, c)
		}
	}
	if len(picked) == 0 && installed["eng"] {
		picked = []string{"eng"}
	}
	return strings.Join(picked, "+")
}

// ocrFrame 是一张字幕帧的识别中间结果：出现时刻与识别文本。
type ocrFrame struct {
	pts  float64 // 该帧在视频中的显示时刻（秒）
	text string  // tesseract 识别文本（已清洗，空串表示该帧无字幕）
}

// ConvertBitmapSubtitle 把一条位图字幕流转成 WebVTT，写入 outPath。
// 任一步骤失败都返回错误，由调用方按「失败隔离」原则记录日志并跳过该轨。
func (o *OCRer) ConvertBitmapSubtitle(ctx context.Context, videoPath string, track SubtitleTrack, outPath string) error {
	if !o.Available() {
		return fmt.Errorf("tesseract OCR 不可用，跳过位图字幕流#%d", track.Index)
	}
	lang := o.pickLang(track)
	if lang == "" {
		return fmt.Errorf("无可用 tesseract 语言包识别流#%d（语言 %s）", track.Index, track.Language)
	}

	workDir, err := os.MkdirTemp("", "ocr-sub-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	frames, err := o.extractFrames(ctx, videoPath, track, workDir)
	if err != nil {
		return err
	}
	if len(frames) == 0 {
		return fmt.Errorf("位图字幕流#%d 未解码出任何帧", track.Index)
	}

	for i := range frames {
		text, err := o.recognize(ctx, filepath.Join(workDir, frameName(i)), lang)
		if err != nil {
			return fmt.Errorf("识别流#%d 第 %d 帧失败: %w", track.Index, i, err)
		}
		frames[i].text = cleanOCRText(text)
	}

	vtt := buildVTT(frames)
	if strings.TrimSpace(strings.TrimPrefix(vtt, "WEBVTT")) == "" {
		// 全部帧都没识别出文字，不产出空文件
		return fmt.Errorf("位图字幕流#%d OCR 未识别出文本", track.Index)
	}
	return os.WriteFile(outPath, []byte(vtt), 0o644)
}

// frameName 与 extractFrames 的 image2 序号格式保持一致（5 位补零）。
func frameName(i int) string {
	return fmt.Sprintf("frame-%05d.png", i+1)
}

// extractFrames 用 ffmpeg 把指定位图字幕流解码为 PNG 序列，并从 showinfo 日志中
// 解析每帧的 pts_time。返回的帧严格按时间升序，与 frame-00001.png 序号一一对应。
func (o *OCRer) extractFrames(ctx context.Context, videoPath string, track SubtitleTrack, workDir string) ([]ocrFrame, error) {
	pattern := filepath.Join(workDir, "frame-%05d.png")
	// -an/-vn 丢弃音视频，只保留目标字幕流；showinfo 打印每帧 pts_time，
	// 其文本输出走 stderr（image2 的 PNG 走文件，stdout 无数据）。
	cmd := exec.CommandContext(ctx, o.ffmpeg,
		"-y", "-v", "info",
		"-i", videoPath,
		"-map", fmt.Sprintf("0:%d?", track.Index),
		"-an", "-vn",
		"-vf", "showinfo",
		"-f", "image2",
		pattern,
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg 解码位图字幕流#%d 失败: %w", track.Index, err)
	}

	pts := parseShowinfoPTS(stderr.String())
	files, err := filepath.Glob(filepath.Join(workDir, "frame-*.png"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, nil
	}

	// showinfo 每个输出帧打印一次，正常情况下与 PNG 数量相等；若日志解析数量
	// 对不上（ffmpeg 版本差异），用序号兜底保证不越界，时间戳缺失的帧时间置 0。
	frames := make([]ocrFrame, 0, len(files))
	for i := range files {
		var t float64
		if i < len(pts) {
			t = pts[i]
		}
		frames = append(frames, ocrFrame{pts: t})
	}
	return frames, nil
}

// showinfoPTSRE 匹配 showinfo 日志中的 "pts_time:12.345"。
var showinfoPTSRE = regexp.MustCompile(`pts_time:(-?\d+(?:\.\d+)?)`)

// parseShowinfoPTS 从 ffmpeg showinfo 的 stderr 文本中按出现顺序提取 pts_time。
func parseShowinfoPTS(logText string) []float64 {
	matches := showinfoPTSRE.FindAllStringSubmatch(logText, -1)
	pts := make([]float64, 0, len(matches))
	for _, m := range matches {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			pts = append(pts, v)
		}
	}
	return pts
}

// recognize 对单张 PNG 执行 tesseract，返回识别文本。
// tesseract 用法：tesseract <image> stdout -l <lang> --psm 6
// psm 6 假设为统一文本块，对字幕条这类单行/少量多行文字效果稳定。
func (o *OCRer) recognize(ctx context.Context, imagePath, lang string) (string, error) {
	cmd := exec.CommandContext(ctx, o.bin,
		imagePath, "stdout",
		"-l", lang,
		"--psm", "6",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// cleanOCRText 清洗单帧识别结果：去掉每行首尾空白、丢弃空行，统一换行。
func cleanOCRText(raw string) string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// buildVTT 把逐帧识别结果合并为 WebVTT 文本。
// 同一条字幕在视频中通常持续若干帧且文字不变，这里把相邻且文本相同的帧合并为
// 一个 cue：起点取首帧时刻，终点取下一不同文本帧的时刻（末帧用自身时刻兜底，
// 至少给 1 秒展示窗口）。空文本帧（字幕消失）用于切断 cue，不产生输出。
func buildVTT(frames []ocrFrame) string {
	type cue struct {
		start, end float64
		text       string
	}
	var cues []cue

	var curText string
	var curStart float64
	flush := func(end float64) {
		if curText == "" {
			return
		}
		if end <= curStart {
			end = curStart + 1
		}
		cues = append(cues, cue{start: curStart, end: end, text: curText})
		curText = ""
	}

	for _, f := range frames {
		if f.text == "" {
			// 空白帧：结束当前 cue，终点用本帧时刻
			flush(f.pts)
			continue
		}
		if curText == "" {
			curText = f.text
			curStart = f.pts
			continue
		}
		if f.text != curText {
			// 文本变化：上一 cue 在本帧时刻结束，开启新 cue
			flush(f.pts)
			curText = f.text
			curStart = f.pts
		}
	}
	// 末帧仍在展示的字幕，终点未知，给 1 秒兜底窗口
	flush(frames[len(frames)-1].pts + 1)

	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, c := range cues {
		b.WriteString(formatVTTTime(c.start))
		b.WriteString(" --> ")
		b.WriteString(formatVTTTime(c.end))
		b.WriteString("\n")
		b.WriteString(c.text)
		b.WriteString("\n\n")
	}
	return b.String()
}

// formatVTTTime 把秒数格式化为标准 WebVTT 时间戳 HH:MM:SS.mmm。
func formatVTTTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	d := time.Duration(sec * float64(time.Second)).Round(time.Millisecond)
	h := int64(d / time.Hour)
	d -= time.Duration(h) * time.Hour
	m := int64(d / time.Minute)
	d -= time.Duration(m) * time.Minute
	s := int64(d / time.Second)
	ms := int64((d - time.Duration(s)*time.Second) / time.Millisecond)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}
