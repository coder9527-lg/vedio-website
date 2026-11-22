package media

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Metadata 是视频的刮削元信息（来自 NFO 文件、本地海报或在线刮削器）。
// 全部字段均为可选，零值表示该信息缺失；前端按是否为空决定展示。
type Metadata struct {
	Title        string   `json:"title,omitempty"`        // 正式片名（缺失时回退到文件名）
	Original     string   `json:"original,omitempty"`     // 原始/外文片名
	Overview     string   `json:"overview,omitempty"`     // 剧情简介
	Year         string   `json:"year,omitempty"`         // 年份（字符串，兼容 NFO 的各种写法）
	Runtime      int      `json:"runtime,omitempty"`      // 片长（分钟）
	Rating       float64  `json:"rating,omitempty"`       // 评分（0-10）
	Genres       []string `json:"genres,omitempty"`       // 类型
	Directors    []string `json:"directors,omitempty"`    // 导演
	Actors       []string `json:"actors,omitempty"`       // 主演（名字）
	Studio       string   `json:"studio,omitempty"`       // 出品方/制片厂
	PosterPath   string   `json:"posterPath,omitempty"`   // 海报绝对路径（本地文件）
	BackdropPath string   `json:"backdropPath,omitempty"` // 背景图绝对路径（fanart）
	Source       string   `json:"source,omitempty"`       // 元信息来源：nfo / tmdb
	// PosterRel 是海报相对媒体根目录的路径，仅用于判断与去重，不直接对外暴露
}

// nfoMovie 对应 Kodi/Jellyfin 风格的 movie.nfo / <片名>.nfo 结构。
// 只解析我们关心的字段，未知字段由 encoding/xml 自动忽略。
type nfoMovie struct {
	XMLName   xml.Name    `xml:"movie"`
	Title     string      `xml:"title"`
	Original  string      `xml:"originaltitle"`
	Plot      string      `xml:"plot"`
	Outline   string      `xml:"outline"`
	Year      string      `xml:"year"`
	Premiered string      `xml:"premiered"`
	Runtime   string      `xml:"runtime"`
	Rating    nfoRating   `xml:"ratings>rating"`
	RatingTop nfoRating   `xml:"rating"` // 旧版 NFO 直接写 <rating>
	Genre     []string    `xml:"genre"`
	Director  []string    `xml:"director"`
	Studio    string      `xml:"studio"`
	Art       nfoArt      `xml:"art"`
	Actor     []nfoActor  `xml:"actor"`
	UniqueIDs []nfoUnique `xml:"uniqueid"`
}

// nfoRating 同时兼容新版 <rating><value>8.5</value></rating> 与旧版 <rating>7.2</rating>。
type nfoRating struct {
	Value string `xml:"value"`
}

// UnmarshalXML 允许 rating 元素直接承载分数文本（Kodi 旧版 NFO 写法）。
func (r *nfoRating) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var raw struct {
		Value string `xml:"value"`
		Plain string `xml:",chardata"`
	}
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	if raw.Value != "" {
		r.Value = raw.Value
	} else {
		r.Value = strings.TrimSpace(raw.Plain)
	}
	return nil
}

type nfoArt struct {
	Poster string `xml:"poster"`
	Fanart string `xml:"fanart"`
}

type nfoActor struct {
	Name string `xml:"name"`
}

type nfoUnique struct {
	Type    string `xml:"type,attr"`
	Default bool   `xml:"default,attr"`
	Value   string `xml:",chardata"`
}

// IsArtworkExt 判断文件名是否为受支持的海报/背景图。
func IsArtworkExt(name string) bool {
	return posterExts[strings.ToLower(filepath.Ext(name))]
}

// IsNFOExt 判断文件名是否为 NFO 元信息文件。
func IsNFOExt(name string) bool {
	return strings.ToLower(filepath.Ext(name)) == ".nfo"
}

// IsMetadataSidecar 判断文件是否为元信息附属文件（NFO 或海报图片），
// 供 Watcher 决定变更后要刷新哪些视频的元信息。
func IsMetadataSidecar(name string) bool {
	return IsNFOExt(name) || IsArtworkExt(name)
}

// posterExts 受支持的海报图片扩展名。
var posterExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
}

// posterFileMarkers 文件名（去扩展名、小写）中出现这些标记时视为海报候选。
// 顺序即优先级：显式 poster 优先，其次 Kodi 的 -poster 后缀，再次 folder 图。
var posterFileMarkers = []string{"poster", "cover", "folder", "thumb", "albumcover"}

// backdropMarkers 标记背景图（fanart）。
var backdropMarkers = []string{"fanart", "backdrop", "background"}

// empty 判断元信息是否不含任何可用字段（NFO 与图片全部缺失时的零值状态）。
func (m *Metadata) empty() bool {
	return m.Title == "" && m.Original == "" && m.Overview == "" && m.Year == "" &&
		m.Runtime == 0 && m.Rating == 0 && len(m.Genres) == 0 && len(m.Directors) == 0 &&
		len(m.Actors) == 0 && m.Studio == "" && m.PosterPath == "" && m.BackdropPath == ""
}

// manualEmpty 判断手动编辑覆盖文件的 10 个核心文本字段是否全空；
// 图片路径与来源不参与（手动表单不含这些字段），全空时覆盖文件应被删除而非保留。
func (m *Metadata) manualEmpty() bool {
	return m.Title == "" && m.Original == "" && m.Overview == "" && m.Year == "" &&
		m.Runtime == 0 && m.Rating == 0 && len(m.Genres) == 0 && len(m.Directors) == 0 &&
		len(m.Actors) == 0 && m.Studio == ""
}

// loadMetadata 为单个视频发现 NFO 与本地海报/背景图。
// 任何一步失败都返回已收集到的部分结果，绝不因元信息问题影响视频本身可用；
// 两者都缺失时返回 nil，便于调用方区分「无元信息」与「有部分元信息」。
func loadMetadata(root string, v *Video) *Metadata {
	md := &Metadata{}
	dir := filepath.Dir(v.AbsPath())

	if nfoPath := findNFO(dir, v.Name); nfoPath != "" {
		if parsed, err := parseNFO(nfoPath); err == nil {
			md = parsed
			md.Source = "nfo"
		}
	}

	// 本地海报：NFO <art> 指向的路径优先，其次按命名约定在同目录发现
	if md.PosterPath == "" {
		if p := findArtwork(dir, v.Name, posterFileMarkers); p != "" {
			md.PosterPath = p
		}
	} else {
		md.PosterPath = resolveArtPath(dir, md.PosterPath)
	}
	if md.BackdropPath == "" {
		if p := findArtwork(dir, v.Name, backdropMarkers); p != "" {
			md.BackdropPath = p
		}
	} else {
		md.BackdropPath = resolveArtPath(dir, md.BackdropPath)
	}
	if md.empty() {
		return nil
	}
	return md
}

// findNFO 查找与视频关联的 NFO：优先同名 <片名>.nfo，其次目录下的 movie.nfo。
func findNFO(dir, videoName string) string {
	candidates := []string{
		filepath.Join(dir, videoName+".nfo"),
		filepath.Join(dir, "movie.nfo"),
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

// parseNFO 读取并解析 Kodi 风格 NFO 文件为 Metadata。
func parseNFO(path string) (*Metadata, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var n nfoMovie
	if err := xml.Unmarshal(raw, &n); err != nil {
		return nil, err
	}

	md := &Metadata{
		Title:        strings.TrimSpace(n.Title),
		Original:     strings.TrimSpace(n.Original),
		Overview:     firstNonEmpty(n.Plot, n.Outline),
		Year:         nfoYear(n),
		Genres:       cleanList(n.Genre),
		Directors:    cleanList(n.Director),
		Studio:       strings.TrimSpace(n.Studio),
		PosterPath:   strings.TrimSpace(n.Art.Poster),
		BackdropPath: strings.TrimSpace(n.Art.Fanart),
	}
	if rt, err := strconv.Atoi(strings.TrimSpace(n.Runtime)); err == nil {
		md.Runtime = rt
	}
	// 评分：优先新版 <ratings><rating><value>，回退旧版顶层 <rating>
	if r, err := strconv.ParseFloat(firstNonEmpty(n.Rating.Value, n.RatingTop.Value), 64); err == nil {
		md.Rating = r
	}
	for _, a := range n.Actor {
		if name := strings.TrimSpace(a.Name); name != "" {
			md.Actors = append(md.Actors, name)
		}
	}
	return md, nil
}

// nfoYear 从 <year> 或 <premiered>（YYYY-MM-DD）提取年份。
func nfoYear(n nfoMovie) string {
	if y := strings.TrimSpace(n.Year); y != "" {
		return y
	}
	if p := strings.TrimSpace(n.Premiered); len(p) >= 4 {
		return p[:4]
	}
	return ""
}

// findArtwork 在视频同目录按命名约定查找图片。
// 匹配方式（命中即返回，按 markers 优先级）：
//  1. 与视频同名且带标记：<片名>-poster.jpg、<片名>.fanart.jpg
//  2. 目录级通用图：poster.jpg、folder.jpg、fanart.png
func findArtwork(dir, videoName string, markers []string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	// 收集目录下全部图片，避免多次遍历
	type cand struct {
		path  string
		stem  string // 不含扩展名的小写文件名
		order int    // marker 优先级，越小越优先
	}
	var imgs []cand
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !posterExts[ext] {
			continue
		}
		stem := strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		imgs = append(imgs, cand{path: filepath.Join(dir, e.Name()), stem: stem})
	}

	// 第一轮：与视频同名的图片（<片名><分隔><标记> 或 <片名>.<标记>）
	for _, marker := range markers {
		for _, im := range imgs {
			if artStemMatch(im.stem, strings.ToLower(videoName), marker) {
				return im.path
			}
		}
	}
	// 第二轮：目录级通用图（folder.jpg / poster.jpg / fanart.png 等）
	for _, marker := range markers {
		for _, im := range imgs {
			if im.stem == marker {
				return im.path
			}
		}
	}
	return ""
}

// artStemMatch 判断图片 stem 是否为「视频名 + 分隔符 + 标记」形式。
// 分隔符支持 '-'、'_'、'.'、空格，例如 "movie-poster"、"movie.poster"、"movie poster"。
func artStemMatch(stem, videoStem, marker string) bool {
	if !strings.HasPrefix(stem, videoStem) {
		return false
	}
	rest := strings.TrimPrefix(stem, videoStem)
	if rest == "" {
		return false
	}
	// 去掉一个前导分隔符后应正好等于标记
	rest = strings.TrimLeft(rest, "-_. ")
	return rest == marker
}

// resolveArtPath 解析 NFO <art> 中的图片路径：绝对路径原样返回，相对路径基于 NFO 所在目录。
func resolveArtPath(dir, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}
	abs := filepath.Join(dir, filepath.FromSlash(p))
	if _, err := os.Stat(abs); err == nil {
		return abs
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		// NFO 偶见用 / 或 , 把多个值塞进一个 <genre>
		for _, part := range strings.FieldsFunc(s, func(r rune) bool {
			return r == '/' || r == ',' || r == '|'
		}) {
			if p := strings.TrimSpace(part); p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
