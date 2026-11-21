package server

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// maxSubtitleUpload 是单个上传字幕的体积上限（10 MiB）。
const maxSubtitleUpload = 10 << 20

// uploadLangs 是上传时允许的语言标签白名单：Token 为落盘文件名中的语言段
// （须能被 media.resolveLang 解析），Label 为播放页下拉展示名。
var uploadLangs = []struct {
	Token, Label string
}{
	{"zh-Hans", "简体中文"}, {"zh-Hant", "繁体中文"}, {"en", "English"},
	{"ja", "日本語"}, {"ko", "한국어"}, {"fr", "Français"}, {"de", "Deutsch"},
	{"es", "Español"}, {"ru", "Русский"}, {"it", "Italiano"}, {"pt", "Português"},
	{"th", "ไทย"}, {"vi", "Tiếng Việt"}, {"ar", "العربية"}, {"hi", "हिन्दी"},
}

// langAllowed 判断语言 token 是否在上传白名单内（白名单字符本身即安全，杜绝路径注入）。
func langAllowed(lang string) bool {
	for _, l := range uploadLangs {
		if l.Token == lang {
			return true
		}
	}
	return false
}

// handleSubtitleUpload 接收 multipart 上传的字幕文件（字段 file 与 lang），
// 按「视频名.语言.ext」规范命名落到视频同目录，再热刷新字幕索引。
func (s *Server) handleSubtitleUpload(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	// maxMemory 仅决定内存/临时文件分界，体积上限在读取后二次校验
	if err := r.ParseMultipartForm(maxSubtitleUpload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无法解析上传表单"})
		return
	}
	lang := r.FormValue("lang")
	if !langAllowed(lang) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不支持的语言标签"})
		return
	}
	file, info, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少字幕文件"})
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(info.Filename))
	if !media.IsSubtitleExt("x" + ext) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不支持的字幕格式，仅支持 srt/vtt/ass/ssa"})
		return
	}

	// 多读 1 字节用于判断是否超限，避免信任客户端声明的大小
	data, err := io.ReadAll(io.LimitReader(file, maxSubtitleUpload+1))
	if err != nil {
		log.Printf("读取上传字幕失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "读取字幕失败"})
		return
	}
	if len(data) > maxSubtitleUpload {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "字幕文件过大（上限 10MB）"})
		return
	}

	dir := filepath.Dir(v.AbsPath())
	target := filepath.Join(dir, v.Name+"."+lang+ext)
	// 同名冲突：追加 .2/.3 数字后缀，不覆盖已有字幕
	for n := 2; ; n++ {
		if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
			break
		}
		target = filepath.Join(dir, v.Name+"."+lang+"."+strconv.Itoa(n)+ext)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		log.Printf("写入字幕失败 %s: %v", target, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "字幕落盘失败"})
		return
	}

	s.library.RefreshSubtitles(v.ID)
	subs := s.library.Subtitles(v.ID)
	if subs == nil {
		subs = []*media.Subtitle{}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "subtitles": subs})
}

// handleSubtitleDelete 删除一个外挂字幕文件并热刷新索引。
// 仅允许删除 library 已登记、且位于媒体根目录之内的字幕，防御路径穿越。
func (s *Server) handleSubtitleDelete(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	sub, ok := s.library.GetSubtitle(v.ID, r.PathValue("sid"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "字幕不存在"})
		return
	}
	rel, err := filepath.Rel(s.library.Root(), sub.AbsPath())
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法的字幕路径"})
		return
	}
	if err := os.Remove(sub.AbsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("删除字幕失败 %s: %v", sub.RelPath, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "删除字幕失败"})
		return
	}
	s.library.RefreshSubtitles(v.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleSubtitleList 返回视频的外挂字幕列表 JSON，供播放页渲染字幕菜单与 <track>。
func (s *Server) handleSubtitleList(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	subs := s.library.Subtitles(v.ID)
	if subs == nil {
		subs = []*media.Subtitle{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"subtitles": subs})
}

// handleSubtitleTrack 返回单个字幕，统一转为 WebVTT（浏览器 <track> 仅可靠支持 VTT）。
// 原生 .vtt 直接透传；.srt/.ass/.ssa 在内存中做轻量转换，失败回退占位 VTT 而非 500，
// 避免单个坏字幕影响播放页其余功能。
func (s *Server) handleSubtitleTrack(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	sub, ok := s.library.GetSubtitle(v.ID, r.PathValue("sid"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	raw, err := os.ReadFile(sub.AbsPath())
	if err != nil {
		log.Printf("读取字幕失败 %s: %v", sub.RelPath, err)
		http.NotFound(w, r)
		return
	}

	vtt := convertToVTT(raw, sub.Format)
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	// 字幕随 sidecar 文件变化，用 no-cache 让浏览器每次校验，体积通常很小
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(vtt)
}

var srtTimeRE = regexp.MustCompile(`(\d{2}):(\d{2}):(\d{2})[,.](\d{3})`)

// convertToVTT 把外挂字幕统一转成 WebVTT 字节流。
func convertToVTT(raw []byte, format string) []byte {
	switch format {
	case "vtt":
		// 已是 WebVTT：确保文件头存在，个别工具产出的 VTT 缺头时补上
		if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("WEBVTT")) {
			return raw
		}
		return append([]byte("WEBVTT\n\n"), raw...)
	case "ass", "ssa":
		return assToVTT(raw)
	default: // srt 及其余情况按 SubRip 处理
		return srtToVTT(raw)
	}
}

// srtToVTT 转换 SubRip：加 WEBVTT 头，时间戳逗号改点号。
func srtToVTT(raw []byte) []byte {
	text := srtTimeRE.ReplaceAllString(string(raw), "$1:$2:$3.$4")
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		// 跳过纯序号行（SRT 每个 cue 前的数字编号，VTT 不需要）
		if isDigits(line) {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// assDialogueRE 匹配 ASS/SSA 对话行：Format 中逗号分隔的前 9 个字段
// （Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect），其余为文本。
var assDialogueRE = regexp.MustCompile(`^Dialogue:\s*(.*)$`)

// assToVTT 提取 ASS/SSA 的 Dialogue 行转为 WebVTT。
// 只做基础文本提取：时间轴与文字保留，样式/定位/绘图指令无法映射到 VTT，予以剥离。
func assToVTT(raw []byte) []byte {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		m := assDialogueRE.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		// 按逗号切成 10 段：前 9 个为字段，第 10 段起是可能含逗号的台词
		parts := strings.SplitN(m[1], ",", 10)
		if len(parts) < 10 {
			continue
		}
		start := assTimeToVTT(parts[1])
		end := assTimeToVTT(parts[2])
		if start == "" || end == "" {
			continue
		}
		text := cleanASSText(parts[9])
		if text == "" {
			continue
		}
		b.WriteString(start)
		b.WriteString(" --> ")
		b.WriteString(end)
		b.WriteByte('\n')
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	return []byte(b.String())
}

// assTimeToVTT 把 ASS 时间 H:MM:SS.cc（百分秒）转成 VTT 的 HH:MM:SS.mmm（毫秒）。
func assTimeToVTT(t string) string {
	t = strings.TrimSpace(t)
	cols := strings.Split(t, ":")
	if len(cols) != 3 {
		return ""
	}
	h, m := cols[0], cols[1]
	secParts := strings.SplitN(cols[2], ".", 2)
	if len(secParts) != 2 {
		return ""
	}
	s, cs := secParts[0], secParts[1]
	if len(cs) == 1 {
		cs += "0"
	}
	if len(cs) > 2 {
		cs = cs[:2]
	}
	return padLeft(h, 2) + ":" + padLeft(m, 2) + ":" + padLeft(s, 2) + "." + cs + "0"
}

func padLeft(s string, n int) string {
	for len(s) < n {
		s = "0" + s
	}
	return s
}

var assOverrideRE = regexp.MustCompile(`\{[^}]*\}`)

// cleanASSText 去掉 ASS 样式覆盖块与换行指令，转成 VTT 可用的纯文本。
func cleanASSText(text string) string {
	text = assOverrideRE.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\\N", "\n")
	text = strings.ReplaceAll(text, "\\n", "\n")
	text = strings.ReplaceAll(text, "\\h", " ")
	return strings.TrimSpace(text)
}
