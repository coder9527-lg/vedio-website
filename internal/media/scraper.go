package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Scraper 负责在线刮削的编排：文件名清洗 → TMDB 搜索 → 拉详情 →
// 下载海报/背景图到缓存目录 → 与本地 NFO 元信息合并后写回 Library。
//
// 与 NFO 的优先级：本地 NFO 已有的字段不覆盖，TMDB 只补缺；
// 图片同理，NFO/命名约定已发现的海报不重复下载。
type Scraper struct {
	client   *TMDBClient
	library  *Library
	imageDir string

	// locks 保证同一视频不会被并发生成两份刮削结果
	locksMu sync.Mutex
	locks   map[string]chan struct{}
}

// NewScraper 创建刮削器；client 为 nil（未配置 API Key）时返回 nil。
func NewScraper(client *TMDBClient, library *Library, imageDir string) (*Scraper, error) {
	if client == nil {
		return nil, nil
	}
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return nil, err
	}
	s := &Scraper{
		client:   client,
		library:  library,
		imageDir: imageDir,
		locks:    map[string]chan struct{}{},
	}
	library.SetScraperImageDir(imageDir)
	return s, nil
}

// metadataJSONPath 返回某视频刮削结果的持久化文件路径。
func (s *Scraper) metadataJSONPath(videoID string) string {
	return filepath.Join(s.imageDir, videoID, "metadata.json")
}

// persist 把合并后的元信息写入缓存目录，使重新扫描/重启后仍可恢复。
func (s *Scraper) persist(videoID string, md *Metadata) error {
	return writeJSONAtomic(s.metadataJSONPath(videoID), md)
}

// Available 判断在线刮削是否可用（配置了 TMDB API Key）。
func (s *Scraper) Available() bool { return s != nil && s.client.Available() }

// ImageDir 返回刮削图片的缓存根目录，供 HTTP 层做路径白名单校验。
func (s *Scraper) ImageDir() string { return s.imageDir }

// lockFor 返回某视频的刮削完成通道与释放函数：
// 已有刮削进行中时返回可等待的通道；首次调用返回 nil 通道与释放函数。
func (s *Scraper) lockFor(videoID string) (wait <-chan struct{}, acquire func()) {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	if ch, ok := s.locks[videoID]; ok {
		return ch, nil
	}
	ch := make(chan struct{})
	s.locks[videoID] = ch
	return nil, func() {
		s.locksMu.Lock()
		close(ch)
		delete(s.locks, videoID)
		s.locksMu.Unlock()
	}
}

// Search 用清洗后的文件名搜索 TMDB 候选；若 NFO 已带年份则作为过滤条件。
func (s *Scraper) Search(ctx context.Context, videoID string) ([]TMDBHit, error) {
	v, ok := s.library.Get(videoID)
	if !ok {
		return nil, fmt.Errorf("视频不存在")
	}
	keyword, year := scrapeKeyword(v, s.library.Metadata(videoID))
	hits, err := s.client.SearchMovie(ctx, keyword, year)
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// Apply 选定某个 TMDB 影片后拉详情、下载图片并合并元信息。
// 整个过程对同一视频串行化，避免并发请求重复下载与互相覆盖。
func (s *Scraper) Apply(ctx context.Context, videoID string, tmdbID int64) (*Metadata, error) {
	if wait, acquire := s.lockFor(videoID); wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else {
		defer acquire()
	}

	if _, ok := s.library.Get(videoID); !ok {
		return nil, fmt.Errorf("视频不存在")
	}

	tmdbMeta, posterRel, backdropRel, err := s.client.MovieDetail(ctx, tmdbID)
	if err != nil {
		return nil, err
	}

	// 下载图片到缓存目录：data/tmdb/<videoID>/poster.<ext>
	destDir := filepath.Join(s.imageDir, videoID)
	if p := posterRel; p != "" {
		dest := filepath.Join(destDir, "poster"+imageExt(p))
		if dlErr := s.client.downloadImage(ctx, p, "w500", dest); dlErr != nil {
			// 图片下载失败不阻断文字元信息，留空海报字段后继续
		} else if _, statErr := os.Stat(dest); statErr == nil {
			tmdbMeta.PosterPath = dest
		}
	}
	if b := backdropRel; b != "" {
		dest := filepath.Join(destDir, "backdrop"+imageExt(b))
		if dlErr := s.client.downloadImage(ctx, b, "w1280", dest); dlErr == nil {
			if _, statErr := os.Stat(dest); statErr == nil {
				tmdbMeta.BackdropPath = dest
			}
		}
	}

	// 与本地元信息合并：NFO 优先，TMDB 补缺
	merged := mergeMetadata(s.library.Metadata(videoID), tmdbMeta)
	merged.Source = mergedSource(s.library.Metadata(videoID))

	if err := s.persist(videoID, merged); err != nil {
		// 持久化失败不阻断本次刮削，仅记录；重扫后该结果无法恢复
		fmt.Fprintf(os.Stderr, "警告: 持久化刮削结果失败 %s: %v\n", videoID, err)
	}
	// 手动编辑覆盖优先：metadata.json 仍存纯刮削合并结果便于回溯，内存生效值再叠加 manual.json
	final := mergeManual(merged, s.library.loadManualMetadata(videoID))
	s.library.SetMetadata(videoID, final)
	return final, nil
}

// mergedSource 判断合并后的来源标记：本地 NFO 存在则标 nfo+tmdb，否则 tmdb。
func mergedSource(local *Metadata) string {
	if local != nil && local.Source == "nfo" {
		return "nfo+tmdb"
	}
	return "tmdb"
}

// mergeMetadata 以 local 为基底，用 tmdb 填充所有空字段；返回新对象不改入参。
// 两者都为 nil 时返回 nil。来源标记不参与字段补缺，由调用方单独决定。
func mergeMetadata(local, tmdb *Metadata) *Metadata {
	if local == nil && tmdb == nil {
		return nil
	}
	out := &Metadata{}
	if local != nil {
		cp := *local
		out = &cp
		// 切片复制，避免修改共享底层数组
		out.Genres = append([]string(nil), local.Genres...)
		out.Directors = append([]string(nil), local.Directors...)
		out.Actors = append([]string(nil), local.Actors...)
	}
	if tmdb == nil {
		return out
	}
	if out.Source == "" {
		out.Source = tmdb.Source
	}
	if out.Title == "" {
		out.Title = tmdb.Title
	}
	if out.Original == "" {
		out.Original = tmdb.Original
	}
	if out.Overview == "" {
		out.Overview = tmdb.Overview
	}
	if out.Year == "" {
		out.Year = tmdb.Year
	}
	if out.Runtime == 0 {
		out.Runtime = tmdb.Runtime
	}
	if out.Rating == 0 {
		out.Rating = tmdb.Rating
	}
	if out.Studio == "" {
		out.Studio = tmdb.Studio
	}
	if len(out.Genres) == 0 {
		out.Genres = append([]string(nil), tmdb.Genres...)
	}
	if len(out.Directors) == 0 {
		out.Directors = append([]string(nil), tmdb.Directors...)
	}
	if len(out.Actors) == 0 {
		out.Actors = append([]string(nil), tmdb.Actors...)
	}
	if out.PosterPath == "" {
		out.PosterPath = tmdb.PosterPath
	}
	if out.BackdropPath == "" {
		out.BackdropPath = tmdb.BackdropPath
	}
	return out
}

// imageExt 从 TMDB 图片路径提取小写扩展名，默认 .jpg。
func imageExt(relPath string) string {
	ext := strings.ToLower(filepath.Ext(relPath))
	if ext == "" || len(ext) > 5 {
		return ".jpg"
	}
	return ext
}

// scrapeKeyword 从视频名提取搜索关键字与可选年份：
// 优先用 NFO 正式片名，否则清洗文件名（去分辨率、编码组、来源标签等噪声）。
func scrapeKeyword(v *Video, md *Metadata) (string, string) {
	year := ""
	if md != nil && md.Year != "" {
		year = md.Year
	}
	if md != nil && md.Title != "" {
		return md.Title, year
	}
	return cleanVideoName(v.Name), year
}

var (
	// bracketRe 匹配整对方括号/圆括号/中文括号注释，如 [1080p]、(2020)、【TAG】
	bracketRe = regexp.MustCompile(`[\[\(（【][^\[\]（）()【】]*[\]\)）】]`)
	// noiseTokenRe 匹配常见发布噪声：分辨率、来源、编码、音频、位深等
	noiseTokenRe = regexp.MustCompile(`(?i)(\d{3,4}p|4k|uhd|hdr(\d{2,4})?|10bit|8bit|` +
		`dolby|atmos|blu-?ray|bluray|web-?dl|webrip|web-?rip|web|hdrip|dvdrip|bdrip|` +
		`remux|hdtv|x26[45]|h\.?26[45]|hevc|avc|xvid|divx|` +
		`dts(-hd)?|ac3|aac|flac|truehd)`)
	yearRe = regexp.MustCompile(`(19|20)\d{2}`)
	sepRe  = regexp.MustCompile(`[._]+`)
	// tailRe 匹配结尾的 -YIFY / -CHD 等发布组标记
	tailRe = regexp.MustCompile(`-\s*[A-Za-z0-9]+$`)
)

// cleanVideoName 把 "The.Movie.2019.1080p.BluRay.x264-YIFY" 清洗为 "The Movie 2019"。
// 括号中的年份（如 (2020)）会先取出再删括号，最后统一补回，避免随注释一起丢失。
func cleanVideoName(name string) string {
	year := yearRe.FindString(name)
	s := bracketRe.ReplaceAllString(name, " ")
	s = noiseTokenRe.ReplaceAllString(s, " ")
	s = sepRe.ReplaceAllString(s, " ")
	s = tailRe.ReplaceAllString(s, "")
	fields := strings.Fields(s)
	if year != "" && !containsString(fields, year) {
		fields = append(fields, year)
	}
	return strings.Join(fields, " ")
}

func containsString(items []string, target string) bool {
	for _, it := range items {
		if it == target {
			return true
		}
	}
	return false
}

// extractYear 暴露给测试与搜索兜底：从清洗后的名称中找年份。
func extractYear(name string) string {
	return yearRe.FindString(name)
}
