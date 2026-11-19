package media

import (
	"regexp"
	"strings"
)

// EpisodeInfo 是从视频文件名解析出的季集信息。
type EpisodeInfo struct {
	Season     int    // 季号，缺省（仅集号）时兜底为 1
	Episode    int    // 首集集号
	EpisodeEnd int    // 多集文件的尾集集号（如 S01E02E03），单集时为 0
	Title      string // 规范化后的剧名
}

// 季集标记正则，按优先级依次匹配；均大小写不敏感、全文搜索。
var (
	// S01E02 / S1E2 / S01E02E03 / S01E02-03 由后续两个分别覆盖
	reSE = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,2})(?:E?(\d{1,2}))?`)
	// 1x02
	reX = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,2})\b`)
	// 第1季第2集 / 第一季第二集
	reCNSeasonEpisode = regexp.MustCompile(`第\s*([0-9零一二三四五六七八九十百]+)\s*季\s*第\s*([0-9零一二三四五六七八九十百]+)\s*集`)
	// 仅集号：第2集 / EP02 / E02，季号兜底为 1
	reCNEpisode = regexp.MustCompile(`第\s*([0-9零一二三四五六七八九十百]+)\s*集`)
	reEPEpisode = regexp.MustCompile(`(?i)\bEP?(\d{1,2})\b`)
)

// 标题清理用正则：季集标记、方括号组名、年份、分辨率、来源/编码噪声词。
var (
	reSEMarker         = regexp.MustCompile(`(?i)S\d{1,2}E\d{1,2}(?:E?\d{1,2})?`)
	reXMarker          = regexp.MustCompile(`(?i)\b\d{1,2}x\d{1,2}\b`)
	reCNSeasonEpMarker = regexp.MustCompile(`第\s*[0-9零一二三四五六七八九十百]+\s*季\s*第\s*[0-9零一二三四五六七八九十百]+\s*集`)
	reCNEpMarker       = regexp.MustCompile(`第\s*[0-9零一二三四五六七八九十百]+\s*集`)
	reEPMarker         = regexp.MustCompile(`(?i)\bEP?\d{1,2}\b`)
	reBrackets         = regexp.MustCompile(`[\[\(（【][^\]\)）】]*[\]\)）】]`)
	reYear             = regexp.MustCompile(`(?:19|20)\d{2}`)
	reResolution       = regexp.MustCompile(`(?i)(?:\b\d{3,4}p\b|[48]k)`)
	reNoiseWords       = regexp.MustCompile(`(?i)(?:\b(?:bluray|blu[-.\s]?ray|web[-.\s]?dl|webrip|hdtv|dvdrip|bdrip|x264|x265|h[.\s]?264|h[.\s]?265|hevc|avc|aac|ac3|dts|ddp?5[.\s]?1|10bit|8bit|hdr|remux|proper|repack)\b)`)
	reSeparatorRuns    = regexp.MustCompile(`[._\-]+`)
	reSpaceRuns        = regexp.MustCompile(`\s+`)
)

// ParseEpisode 从去扩展名的文件名（Video.Name）解析季集信息。
// 能解析出季/集标记时 ok=true，否则视为电影，ok=false。
func ParseEpisode(stem string) (EpisodeInfo, bool) {
	title := normalizeTitle(stem)
	info := EpisodeInfo{Title: title}

	if m := reSE.FindStringSubmatch(stem); m != nil {
		info.Season = atoiSafe(m[1])
		info.Episode = atoiSafe(m[2])
		if m[3] != "" {
			info.EpisodeEnd = atoiSafe(m[3])
		}
		return info, true
	}
	if m := reX.FindStringSubmatch(stem); m != nil {
		info.Season = atoiSafe(m[1])
		info.Episode = atoiSafe(m[2])
		return info, true
	}
	if m := reCNSeasonEpisode.FindStringSubmatch(stem); m != nil {
		info.Season = cnNum(m[1])
		info.Episode = cnNum(m[2])
		return info, true
	}
	if m := reCNEpisode.FindStringSubmatch(stem); m != nil {
		info.Season = 1
		info.Episode = cnNum(m[1])
		return info, true
	}
	if m := reEPEpisode.FindStringSubmatch(stem); m != nil {
		info.Season = 1
		info.Episode = atoiSafe(m[1])
		return info, true
	}
	return info, false
}

// normalizeTitle 去掉文件名中的季集标记、年份、分辨率、来源/编码词与组名，
// 并把 . _ - 等分隔符统一为空格，得到可用于聚合/展示的剧名。
func normalizeTitle(stem string) string {
	// 先把 . _ - 等分隔符统一为空格，确保后续 \b 边界在点分/下划线命名下也成立
	s := reSeparatorRuns.ReplaceAllString(stem, " ")
	s = reSEMarker.ReplaceAllString(s, " ")
	s = reXMarker.ReplaceAllString(s, " ")
	s = reCNSeasonEpMarker.ReplaceAllString(s, " ")
	s = reCNEpMarker.ReplaceAllString(s, " ")
	s = reEPMarker.ReplaceAllString(s, " ")
	s = reBrackets.ReplaceAllString(s, " ")
	s = reYear.ReplaceAllString(s, " ")
	s = reResolution.ReplaceAllString(s, " ")
	s = reNoiseWords.ReplaceAllString(s, " ")
	s = reSpaceRuns.ReplaceAllString(strings.TrimSpace(s), " ")
	return s
}

// atoiSafe 解析 1-2 位阿拉伯数字，失败返回 0。
func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// cnNum 解析阿拉伯或中文数字（支持到百级，如 一百零一、二十），失败返回 0。
func cnNum(s string) int {
	if n := atoiSafe(s); n > 0 {
		return n
	}
	n, section := 0, 0
	for _, r := range s {
		switch r {
		case '零':
			continue
		case '一':
			section += 1
		case '二':
			section += 2
		case '三':
			section += 3
		case '四':
			section += 4
		case '五':
			section += 5
		case '六':
			section += 6
		case '七':
			section += 7
		case '八':
			section += 8
		case '九':
			section += 9
		case '十':
			if section == 0 {
				section = 1
			}
			n += section * 10
			section = 0
		case '百':
			if section == 0 {
				section = 1
			}
			n += section * 100
			section = 0
		}
	}
	return n + section
}
