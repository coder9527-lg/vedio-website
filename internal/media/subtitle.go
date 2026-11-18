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

// subtitleDirs 是视频目录下可能集中存放字幕的子目录名（大小写不敏感），
// 兼容 Jellyfin/Emby 把字幕放在 subs/subtitles 子目录的组织方式。
var subtitleDirs = map[string]bool{
	"subs": true, "subtitles": true, "字幕": true,
}

// Subtitle 表示与视频同目录（或其字幕子目录）的一个外挂字幕文件。
type Subtitle struct {
	ID      string `json:"id"`      // 视频内唯一的字幕 ID（由相对路径派生）
	Name    string `json:"name"`    // 展示名，如「简体中文」「English（强制）」
	Lang    string `json:"lang"`    // 语言代码（zh/zh-Hans/en/...），无法识别时为 "und"
	Format  string `json:"format"`  // srt | vtt | ass | ssa
	RelPath string `json:"relPath"` // 相对媒体根目录的路径
	SDH     bool   `json:"sdh"`     // 听障字幕（含音效描述）
	Forced  bool   `json:"forced"`  // 强制字幕（仅外来语言片段）
	Default bool   `json:"default"` // 是否建议默认开启（排序后的首选轨道）
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

// findSubtitles 查找与视频关联的外挂字幕：
//   - 视频同目录，以及其中的 subs/subtitles/字幕 子目录；
//   - 命名兼容 Jellyfin/Emby：Movie.srt、Movie.en.srt、Movie.zh-CN.forced.ass、
//     Movie.default.srt，分隔符支持 '.' ' ' '-' '_'；
//   - 解析语言、SDH（sdh/cc/hi）、强制（forced/force）标记。
func findSubtitles(root string, v *Video) []*Subtitle {
	dir := filepath.Dir(v.AbsPath())
	candidates := []string{dir}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() && subtitleDirs[strings.ToLower(e.Name())] {
				candidates = append(candidates, filepath.Join(dir, e.Name()))
			}
		}
	}

	var subs []*Subtitle
	seen := map[string]bool{}
	for _, cdir := range candidates {
		files, err := os.ReadDir(cdir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			ext := strings.ToLower(filepath.Ext(name))
			if !DefaultSubExts[ext] {
				continue
			}
			stem := strings.TrimSuffix(name, filepath.Ext(name))
			if !subtitleStemMatch(stem, v.Name) {
				continue
			}
			abs := filepath.Join(cdir, name)
			rel, err := filepath.Rel(root, abs)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			id := makeSubID(rel)
			if seen[id] { // 同名子目录与根目录去重
				continue
			}
			seen[id] = true
			subs = append(subs, parseSubtitle(id, abs, rel, ext, stem, v.Name))
		}
	}

	sortSubtitles(subs)
	// 默认轨道：排序后的第一条非强制字幕（强制字幕只在外语片段出现，不应默认开启）
	for _, s := range subs {
		if !s.Forced {
			s.Default = true
			break
		}
	}
	return subs
}

// subtitleStemMatch 判断字幕文件名（去扩展名）是否与视频同名，或为「同名+分隔符+标签」。
// 标签首段必须是已知语言、default/sdh/forced 等标记，避免 Movie.part1.srt 被误挂载。
func subtitleStemMatch(stem, videoBase string) bool {
	if stem == videoBase {
		return true
	}
	if len(stem) <= len(videoBase) || !strings.HasPrefix(stem, videoBase) {
		return false
	}
	switch stem[len(videoBase)] {
	case '.', ' ', '-', '_':
	default:
		return false
	}
	tokens := splitSubTokens(strings.TrimPrefix(stem, videoBase))
	if len(tokens) == 0 {
		return false
	}
	first := normalizeToken(tokens[0])
	if first == "default" || first == "sdh" || first == "cc" || first == "hi" ||
		first == "forced" || first == "force" {
		return true
	}
	_, ok := languageNames[first]
	return ok
}

// parseSubtitle 从文件名后缀解析语言、SDH/强制标记与展示名。
func parseSubtitle(id, abs, rel, ext, stem, videoBase string) *Subtitle {
	s := &Subtitle{
		ID:      id,
		Format:  strings.TrimPrefix(ext, "."),
		RelPath: rel,
		absPath: abs,
	}
	tokens := splitSubTokens(strings.TrimPrefix(stem, videoBase))
	rawSuffix := strings.Join(tokens, ".")
	for _, tok := range tokens {
		t := normalizeToken(tok)
		if s.Lang == "" || s.Lang == "und" {
			if code, ok := resolveLang(t); ok {
				s.Lang = code
			}
		}
		switch t {
		case "sdh", "cc", "hi":
			s.SDH = true
		case "forced", "force":
			s.Forced = true
		case "default":
			s.Default = true
		}
	}
	if s.Lang == "" {
		s.Lang = "und"
	}
	s.Name = subtitleDisplayName(s, rawSuffix)
	return s
}

// splitSubTokens 按 . 空格 - _ 拆分后缀标签。
func splitSubTokens(suffix string) []string {
	return strings.FieldsFunc(suffix, func(r rune) bool {
		return r == '.' || r == ' ' || r == '-' || r == '_'
	})
}

// normalizeToken 统一小写并把下划线转为连字符（zh_cn → zh-cn）。
func normalizeToken(tok string) string {
	return strings.ReplaceAll(strings.ToLower(tok), "_", "-")
}

func subtitleDisplayName(s *Subtitle, rawSuffix string) string {
	var b strings.Builder
	switch {
	case s.Lang != "und":
		if name, ok := languageNames[langKeyFor(s.Lang)]; ok {
			b.WriteString(name)
		} else {
			b.WriteString(s.Lang)
		}
	case rawSuffix != "":
		b.WriteString(rawSuffix)
	default:
		b.WriteString("字幕")
	}
	if s.Forced {
		b.WriteString("（强制）")
	}
	if s.SDH {
		b.WriteString("（SDH）")
	}
	return b.String()
}

// resolveLang 把文件名中的语言 token（含 ISO 639-2/中文别名/带区域写法）解析为归一语言码。
func resolveLang(tok string) (string, bool) {
	if code, ok := langAliases[tok]; ok {
		return code, true
	}
	if _, ok := languageNames[tok]; ok {
		return normalizeLang(tok), true
	}
	return "", false
}

// langKeyFor 把归一化后的语言代码映射回 languageNames 使用的键。
func langKeyFor(lang string) string {
	switch lang {
	case "zh-Hans":
		return "zh-hans"
	case "zh-Hant":
		return "zh-hant"
	default:
		return strings.ToLower(lang)
	}
}

// langAliases 文件名语言 token → 归一语言码（含 ISO 639-2 与常见别名）。
var langAliases = map[string]string{
	"zh": "zh-Hans", "chs": "zh-Hans", "chi": "zh-Hans", "zho": "zh-Hans",
	"cn": "zh-Hans", "sc": "zh-Hans", "chinese": "zh-Hans",
	"zh-cn": "zh-Hans", "zh-hans": "zh-Hans", "zh-sg": "zh-Hans",
	"cht": "zh-Hant", "tc": "zh-Hant", "zh-tw": "zh-Hant",
	"zh-hk": "zh-Hant", "zh-hant": "zh-Hant",
	"en": "en", "eng": "en", "english": "en",
	"ja": "ja", "jpn": "ja", "japanese": "ja",
	"ko": "ko", "kor": "ko", "korean": "ko",
	"fr": "fr", "fre": "fr", "fra": "fr", "french": "fr",
	"de": "de", "ger": "de", "deu": "de", "german": "de",
	"es": "es", "spa": "es", "spanish": "es",
	"ru": "ru", "rus": "ru", "russian": "ru",
	"it": "it", "ita": "it", "italian": "it",
	"pt": "pt", "por": "pt", "portuguese": "pt",
	"th": "th", "tha": "th", "thai": "th",
	"vi": "vi", "vie": "vi", "vietnamese": "vi",
	"ar": "ar", "ara": "ar", "arabic": "ar",
	"hi": "hi", "hin": "hi", "hindi": "hi",
}

// sortSubtitles 排序：非强制优先、非 SDH 优先、中文 > 英文 > 其他可识别语言 > 未知，最后按路径兜底。
func sortSubtitles(list []*Subtitle) {
	langRank := func(lang string) int {
		switch lang {
		case "zh", "zh-Hans", "zh-Hant":
			return 0
		case "en":
			return 1
		case "und":
			return 3
		default:
			return 2
		}
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Forced != b.Forced {
			return !a.Forced
		}
		if a.SDH != b.SDH {
			return !a.SDH
		}
		if ra, rb := langRank(a.Lang), langRank(b.Lang); ra != rb {
			return ra < rb
		}
		return a.RelPath < b.RelPath
	})
}

// normalizeLang 把常见中文写法归一到 BCP-47 风格代码。
func normalizeLang(code string) string {
	switch code {
	case "zh-cn", "zh-hans", "zh-sg", "chs", "sc", "chi", "zho", "cn", "chinese":
		return "zh-Hans"
	case "zh-tw", "zh-hk", "zh-hant", "cht", "tc":
		return "zh-Hant"
	default:
		return code
	}
}

// languageNames 语言代码（小写）→ 展示名。同时承担「已知语言标签」白名单职责。
var languageNames = map[string]string{
	"zh": "中文", "zh-cn": "简体中文", "zh-hans": "简体中文", "chs": "简体中文", "sc": "简体中文",
	"chi": "简体中文", "zho": "简体中文", "cn": "简体中文", "chinese": "简体中文",
	"zh-tw": "繁体中文", "zh-hk": "繁体中文", "zh-hant": "繁体中文", "cht": "繁体中文", "tc": "繁体中文",
	"en": "English", "eng": "English", "english": "English",
	"ja": "日本語", "jpn": "日本語", "jp": "日本語", "japanese": "日本語",
	"ko": "한국어", "kor": "한국어", "korean": "한국어",
	"fr": "Français", "fre": "Français", "fra": "Français", "french": "Français",
	"de": "Deutsch", "ger": "Deutsch", "deu": "Deutsch", "german": "Deutsch",
	"es": "Español", "spa": "Español", "spanish": "Español",
	"ru": "Русский", "rus": "Русский", "russian": "Русский",
	"it": "Italiano", "ita": "Italiano", "italian": "Italiano",
	"pt": "Português", "por": "Português", "portuguese": "Português",
	"th": "ไทย", "tha": "ไทย", "thai": "ไทย",
	"vi": "Tiếng Việt", "vie": "Tiếng Việt", "vietnamese": "Tiếng Việt",
	"ar": "العربية", "ara": "العربية", "arabic": "العربية",
	"hi": "हिन्दी", "hin": "हिन्दी", "hindi": "हिन्दी",
}

// makeSubID 用字幕相对路径的 sha1 前 12 位作为 ID。
func makeSubID(rel string) string {
	sum := sha1.Sum([]byte(rel))
	return "s" + hex.EncodeToString(sum[:])[:12]
}
