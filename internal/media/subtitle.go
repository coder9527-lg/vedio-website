package media

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SubtitleExts 是受支持的外挂字幕扩展名（浏览器仅原生支持 VTT，其余按需转换）。
var SubtitleExts = map[string]bool{
	".srt": true, ".vtt": true, ".ass": true, ".ssa": true,
}

// Subtitle 表示一个与视频关联的外挂字幕文件。
type Subtitle struct {
	ID      string `json:"id"`     // 由相对路径派生的稳定 ID
	Name    string `json:"name"`   // 展示名称（语言 + 标记）
	Lang    string `json:"lang"`   // BCP-47 语言标签，无法识别时为空
	Format  string `json:"format"` // 扩展名（不含点），如 srt / ass / vtt
	SDH     bool   `json:"sdh"`    // 听障字幕（含音效描述）
	Forced  bool   `json:"forced"` // 强制字幕（仅外来语言片段）
	Default bool   `json:"default"`
	RelPath string `json:"-"` // 相对于媒体库根目录的路径，仅用于日志
	absPath string
}

// AbsPath 返回字幕文件的绝对路径。
func (s *Subtitle) AbsPath() string { return s.absPath }

// subtitleDirs 是视频目录下可能存放字幕的子目录名（大小写不敏感）。
var subtitleDirs = map[string]bool{"subs": true, "subtitles": true, "字幕": true}

// langAliases 把文件名里常见的语言标识归一为 BCP-47 标签。
var langAliases = map[string]string{
	"zh": "zh-CN", "chs": "zh-CN", "cht": "zh-TW", "chi": "zh-CN", "zho": "zh-CN",
	"cn": "zh-CN", "sc": "zh-CN", "tc": "zh-TW", "chinese": "zh-CN",
	"中文": "zh-CN", "简体": "zh-CN", "繁體": "zh-TW", "繁体": "zh-TW",
	"en": "en", "eng": "en", "english": "en", "英语": "en",
	"ja": "ja", "jpn": "ja", "japanese": "ja", "日语": "ja",
	"ko": "ko", "kor": "ko", "korean": "ko", "韩语": "ko",
	"fr": "fr", "fre": "fr", "fra": "fr", "french": "fr",
	"de": "de", "ger": "de", "deu": "de", "german": "de",
	"es": "es", "spa": "es", "spanish": "es",
	"ru": "ru", "rus": "ru", "russian": "ru",
	"it": "it", "ita": "it", "italian": "it",
	"pt": "pt", "por": "pt", "portuguese": "pt",
	"th": "th", "tha": "th", "thai": "th",
	"vi": "vi", "vie": "vi", "vietnamese": "vi",
}

// langNames 是展示用的语言中文名。
var langNames = map[string]string{
	"zh-CN": "简体中文", "zh-TW": "繁體中文", "en": "英语",
	"ja": "日语", "ko": "韩语", "fr": "法语", "de": "德语",
	"es": "西班牙语", "ru": "俄语", "it": "意大利语", "pt": "葡萄牙语",
	"th": "泰语", "vi": "越南语",
}

// FindSubtitles 在视频同目录及 subs/subtitles 子目录中发现外挂字幕。
// 命名兼容 Jellyfin/Emby 风格：movie.srt、movie.en.srt、movie.zh-CN.forced.ass 等。
func (l *Library) FindSubtitles(v *Video) []*Subtitle {
	base := strings.TrimSuffix(filepath.Base(v.AbsPath()), v.Ext)
	dir := filepath.Dir(v.AbsPath())

	candidates := []string{dir}
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() && subtitleDirs[strings.ToLower(e.Name())] {
				candidates = append(candidates, filepath.Join(dir, e.Name()))
			}
		}
	}

	var out []*Subtitle
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
			if !SubtitleExts[ext] || !subtitleBaseMatch(strings.TrimSuffix(name, filepath.Ext(name)), base) {
				continue
			}
			path := filepath.Join(cdir, name)
			rel, err := filepath.Rel(l.root, path)
			if err != nil {
				continue
			}
			id := makeID(filepath.ToSlash(rel))
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, parseSubtitle(id, path, ext, base, rel))
		}
	}

	sortSubtitles(out)
	// 默认轨道：排序后的第一条非强制字幕
	for _, s := range out {
		if !s.Forced {
			s.Default = true
			break
		}
	}
	return out
}

// GetSubtitle 重新发现并按 ID 查找字幕，避免把任意路径暴露给处理器。
func (l *Library) GetSubtitle(v *Video, subID string) (*Subtitle, bool) {
	for _, s := range l.FindSubtitles(v) {
		if s.ID == subID {
			return s, true
		}
	}
	return nil, false
}

// subtitleBaseMatch 判断无扩展名的字幕文件名是否以视频名为前缀
// （完全同名，或后跟 '.' / ' ' / '-' / '_' 分隔的语言/标记段）。
func subtitleBaseMatch(stem, videoBase string) bool {
	if stem == videoBase {
		return true
	}
	if len(stem) <= len(videoBase) || !strings.HasPrefix(stem, videoBase) {
		return false
	}
	switch stem[len(videoBase)] {
	case '.', ' ', '-', '_':
		return true
	}
	return false
}

// parseSubtitle 从文件名后缀中解析语言与 SDH/强制标记。
func parseSubtitle(id, path, ext, videoBase, rel string) *Subtitle {
	stem := strings.TrimSuffix(filepath.Base(path), ext)
	suffix := strings.TrimPrefix(stem, videoBase)
	suffix = strings.TrimLeft(suffix, ". -_")

	s := &Subtitle{
		ID:      id,
		Format:  strings.TrimPrefix(ext, "."),
		RelPath: filepath.ToSlash(rel),
		absPath: path,
	}

	tokens := strings.FieldsFunc(strings.ToLower(suffix), func(r rune) bool {
		return r == '.' || r == ' ' || r == '-' || r == '_'
	})
	for _, tok := range tokens {
		if lang, ok := langAliases[tok]; ok && s.Lang == "" {
			s.Lang = lang
		}
		switch tok {
		case "sdh", "cc", "hi":
			s.SDH = true
		case "forced", "force":
			s.Forced = true
		}
	}

	s.Name = subtitleDisplayName(s, suffix)
	return s
}

func subtitleDisplayName(s *Subtitle, rawSuffix string) string {
	var b strings.Builder
	if s.Lang != "" {
		b.WriteString(langNames[s.Lang])
	} else if rawSuffix != "" {
		b.WriteString(rawSuffix)
	} else {
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

// sortSubtitles 排序：普通字幕优先于强制字幕，非 SDH 优先于 SDH，
// 中文优先于英文优先于其他可识别语言，最后按名称兜底。
func sortSubtitles(list []*Subtitle) {
	langRank := func(lang string) int {
		switch lang {
		case "":
			return 3
		case "zh-CN", "zh-TW":
			return 0
		case "en":
			return 1
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
		return a.Name < b.Name
	})
}
