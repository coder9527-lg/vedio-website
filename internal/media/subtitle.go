package media

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultSubExts 是默认识别的外挂字幕文件扩展名（均为文本字幕，可转 WebVTT）。
var DefaultSubExts = map[string]bool{
	".srt": true, ".vtt": true, ".ass": true, ".ssa": true,
}

// Subtitle 表示与视频同目录的一个外挂字幕文件。
type Subtitle struct {
	ID      string `json:"id"`      // 视频内唯一的字幕 ID（由相对路径派生）
	Name    string `json:"name"`    // 展示名，如「简体中文」「English」
	Lang    string `json:"lang"`    // 语言代码（zh/zh-Hans/en/...），无法识别时为 "und"
	Format  string `json:"format"`  // srt | vtt | ass | ssa
	RelPath string `json:"relPath"` // 相对媒体根目录的路径
	Default bool   `json:"default"` // 是否建议默认开启（中文/默认后缀字幕）
	absPath string
}

// AbsPath 返回字幕文件的绝对路径。
func (s *Subtitle) AbsPath() string { return s.absPath }

// IsSubtitleExt 判断文件名是否为受支持的外挂字幕。
func IsSubtitleExt(name string) bool {
	return DefaultSubExts[strings.ToLower(filepath.Ext(name))]
}

// videoExtList 返回库中受支持的视频扩展名（小写、含点），顺序稳定。
func videoExtList(l *Library) []string {
	exts := make([]string, 0, len(l.exts))
	for ext := range l.exts {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	return exts
}

// findSubtitles 在视频所在目录查找与视频同名（可带语言后缀）的外挂字幕。
// 命中规则（不区分大小写）：
//   - 精确同名：Movie.srt 对应 Movie.mkv
//   - 语言/标签后缀：Movie.zh.srt、Movie.zh-CN.ass、Movie.default.srt
//
// 其余命名（如 Movie.part1.srt）不自动挂载，避免误关联同目录其他视频。
func findSubtitles(root string, v *Video) []*Subtitle {
	dir := filepath.Dir(v.AbsPath())
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var subs []*Subtitle
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !DefaultSubExts[ext] {
			continue
		}
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		if !subtitleStemMatch(stem, v.Name) {
			continue
		}
		abs := filepath.Join(dir, name)
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		lang, label := parseSubtitleLang(stem, v.Name)
		subs = append(subs, &Subtitle{
			ID:      makeSubID(filepath.ToSlash(rel)),
			Name:    label,
			Lang:    lang,
			Format:  strings.TrimPrefix(ext, "."),
			RelPath: filepath.ToSlash(rel),
			Default: lang == "zh" || lang == "zh-Hans" || lang == "zh-Hant" ||
				strings.EqualFold(stem, v.Name+".default"),
			absPath: abs,
		})
	}

	// 稳定排序：默认字幕优先，其次按相对路径，保证前端展示与测试可预期
	sort.Slice(subs, func(i, j int) bool {
		if subs[i].Default != subs[j].Default {
			return subs[i].Default
		}
		return subs[i].RelPath < subs[j].RelPath
	})
	return subs
}

// subtitleStemMatch 判断字幕文件名（去扩展名）是否与视频同名或为「同名.语言标签」形式。
// 只接受已知语言标签或 default 后缀，避免 Movie.part1.srt 这类无关文件被误挂载。
func subtitleStemMatch(stem, videoBase string) bool {
	if stem == videoBase {
		return true
	}
	prefix := videoBase + "."
	if !strings.HasPrefix(stem, prefix) {
		return false
	}
	suffix := strings.ToLower(strings.TrimPrefix(stem, prefix))
	first := strings.SplitN(suffix, ".", 2)[0]
	first = strings.ReplaceAll(first, "_", "-")
	if first == "default" {
		return true
	}
	if _, ok := languageNames[first]; ok {
		return true
	}
	return false
}

// parseSubtitleLang 从字幕文件名的语言后缀解析语言代码与展示名。
// 如 "Movie.zh-CN" → ("zh-Hans", "简体中文")，"Movie.en" → ("en", "English")。
func parseSubtitleLang(stem, videoBase string) (code, label string) {
	suffix := strings.TrimPrefix(stem, videoBase)
	suffix = strings.TrimPrefix(suffix, ".")
	if suffix == "" {
		return "und", "字幕"
	}
	// 可能有多段后缀（zh-CN.forced），取第一段作为语言
	first := strings.ToLower(strings.SplitN(suffix, ".", 2)[0])
	first = strings.ReplaceAll(first, "_", "-")

	if first == "default" {
		return "und", "默认字幕"
	}
	if name, ok := languageNames[first]; ok {
		return normalizeLang(first), name
	}
	// 未知标签：原样展示，避免丢失信息
	return "und", suffix
}

// normalizeLang 把常见中文写法归一到 BCP-47 风格代码。
func normalizeLang(code string) string {
	switch code {
	case "zh-cn", "zh-hans", "zh-sg", "chs", "sc":
		return "zh-Hans"
	case "zh-tw", "zh-hk", "zh-hant", "cht", "tc":
		return "zh-Hant"
	default:
		return code
	}
}

// languageNames 语言代码 → 中文展示名。
var languageNames = map[string]string{
	"zh": "中文", "zh-cn": "简体中文", "zh-hans": "简体中文", "chs": "简体中文", "sc": "简体中文",
	"zh-tw": "繁体中文", "zh-hk": "繁体中文", "zh-hant": "繁体中文", "cht": "繁体中文", "tc": "繁体中文",
	"en": "English", "eng": "English",
	"ja": "日本語", "jpn": "日本語", "jp": "日本語",
	"ko": "한국어", "kor": "한국어",
	"fr": "Français", "fre": "Français",
	"de": "Deutsch", "ger": "Deutsch",
	"es": "Español", "spa": "Español",
	"ru": "Русский", "rus": "Русский",
	"it": "Italiano", "ita": "Italiano",
	"pt": "Português", "por": "Português",
	"th": "ไทย", "tha": "ไทย",
	"vi": "Tiếng Việt", "vie": "Tiếng Việt",
	"ar": "العربية", "ara": "العربية",
	"hi": "हिन्दी", "hin": "हिन्दी",
}

// makeSubID 用字幕相对路径的 sha1 前 12 位作为 ID。
func makeSubID(rel string) string {
	sum := sha1.Sum([]byte(rel))
	return "s" + hex.EncodeToString(sum[:])[:12]
}
