package media

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TMDB 默认接入点与图片源。baseURL 可在配置中覆写，便于内网代理或测试模拟。
const (
	tmdbDefaultBaseURL = "https://api.themoviedb.org/3"
	tmdbDefaultImages  = "https://image.tmdb.org/t/p"
	tmdbHTTPTimeout    = 15 * time.Second
)

// TMDBHit 是搜索返回的候选影片，只保留前端选择所需的字段。
type TMDBHit struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	Original     string  `json:"originalTitle"`
	Overview     string  `json:"overview"`
	Year         string  `json:"year"`
	Rating       float64 `json:"rating"`
	PosterPath   string  `json:"posterPath"` // 相对图片路径，如 /abc.jpg，空表示无海报
	PosterURL    string  `json:"posterURL"`  // 可直接展示的完整海报 URL（遵循图片源配置）
	BackdropPath string  `json:"backdropPath"`
}

// tmdbSearchResult 对应 /search/movie 响应中我们关心的部分。
type tmdbSearchResult struct {
	Results []struct {
		ID           int64   `json:"id"`
		Title        string  `json:"title"`
		OriginalName string  `json:"original_title"`
		Overview     string  `json:"overview"`
		ReleaseDate  string  `json:"release_date"`
		VoteAverage  float64 `json:"vote_average"`
		PosterPath   string  `json:"poster_path"`
		BackdropPath string  `json:"backdrop_path"`
	} `json:"results"`
}

// tmdbDetail 对应 /movie/{id} 响应中我们关心的部分。
type tmdbDetail struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	OriginalName string  `json:"original_title"`
	Overview     string  `json:"overview"`
	ReleaseDate  string  `json:"release_date"`
	Runtime      int     `json:"runtime"`
	VoteAverage  float64 `json:"vote_average"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	Genres       []struct {
		Name string `json:"name"`
	} `json:"genres"`
	ProductionCompanies []struct {
		Name string `json:"name"`
	} `json:"production_companies"`
	Credits struct {
		Crew []struct {
			Job  string `json:"job"`
			Name string `json:"name"`
		} `json:"crew"`
		Cast []struct {
			Name string `json:"name"`
		} `json:"cast"`
	} `json:"credits"`
}

// TMDBClient 是 TMDB v3 API 的极简客户端，只覆盖影片搜索、详情与图片下载。
type TMDBClient struct {
	apiKey   string
	lang     string
	baseURL  string
	imageURL string
	http     *http.Client
}

// NewTMDBClient 创建客户端；apiKey 为空时返回 nil（功能禁用，不影响其他模块）。
func NewTMDBClient(apiKey, lang, baseURL, imageURL string) *TMDBClient {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	if lang == "" {
		lang = "zh-CN"
	}
	if baseURL == "" {
		baseURL = tmdbDefaultBaseURL
	}
	if imageURL == "" {
		imageURL = tmdbDefaultImages
	}
	return &TMDBClient{
		apiKey:   apiKey,
		lang:     lang,
		baseURL:  strings.TrimRight(baseURL, "/"),
		imageURL: strings.TrimRight(imageURL, "/"),
		http:     &http.Client{Timeout: tmdbHTTPTimeout},
	}
}

// Available 判断客户端是否配置了 API Key。
func (c *TMDBClient) Available() bool { return c != nil && c.apiKey != "" }

// get 发起带 API Key 与语言参数的 GET 请求并解析 JSON。
func (c *TMDBClient) get(ctx context.Context, path string, extra url.Values, out any) error {
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("api_key", c.apiKey)
	if c.lang != "" {
		q.Set("language", c.lang)
	}
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 TMDB 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("TMDB 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("解析 TMDB 响应失败: %w", err)
	}
	return nil
}

// SearchMovie 按关键字（通常是视频文件名）搜索影片，返回候选列表。
func (c *TMDBClient) SearchMovie(ctx context.Context, keyword string, year string) ([]TMDBHit, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, fmt.Errorf("搜索关键字为空")
	}
	q := url.Values{}
	q.Set("query", keyword)
	if year != "" {
		q.Set("year", year)
	}
	var raw tmdbSearchResult
	if err := c.get(ctx, "/search/movie", q, &raw); err != nil {
		return nil, err
	}
	hits := make([]TMDBHit, 0, len(raw.Results))
	for _, r := range raw.Results {
		hits = append(hits, TMDBHit{
			ID:           r.ID,
			Title:        r.Title,
			Original:     r.OriginalName,
			Overview:     r.Overview,
			Year:         tmdbYear(r.ReleaseDate),
			Rating:       r.VoteAverage,
			PosterPath:   r.PosterPath,
			PosterURL:    c.PosterURL(r.PosterPath),
			BackdropPath: r.BackdropPath,
		})
	}
	return hits, nil
}

// MovieDetail 拉取影片详情（含演职人员 credits）并转为内部 Metadata。
// 图片仅填入 TMDB 相对路径，由 Scraper 决定是否下载及落到哪里。
func (c *TMDBClient) MovieDetail(ctx context.Context, id int64) (*Metadata, string, string, error) {
	var raw tmdbDetail
	q := url.Values{}
	q.Set("append_to_response", "credits")
	if err := c.get(ctx, "/movie/"+strconv.FormatInt(id, 10), q, &raw); err != nil {
		return nil, "", "", err
	}
	md := &Metadata{
		Title:    strings.TrimSpace(raw.Title),
		Original: strings.TrimSpace(raw.OriginalName),
		Overview: strings.TrimSpace(raw.Overview),
		Year:     tmdbYear(raw.ReleaseDate),
		Runtime:  raw.Runtime,
		Rating:   raw.VoteAverage,
		Studio:   firstStudio(raw.ProductionCompanies),
		Source:   "tmdb",
	}
	for _, g := range raw.Genres {
		if g.Name != "" {
			md.Genres = append(md.Genres, g.Name)
		}
	}
	directorSeen := map[string]bool{}
	for _, crew := range raw.Credits.Crew {
		if crew.Job == "Director" && crew.Name != "" && !directorSeen[crew.Name] {
			directorSeen[crew.Name] = true
			md.Directors = append(md.Directors, crew.Name)
		}
	}
	actorSeen := map[string]bool{}
	for _, cast := range raw.Credits.Cast {
		// 主演只取前 10 位，避免资料区过长
		if len(md.Actors) >= 10 {
			break
		}
		if cast.Name != "" && !actorSeen[cast.Name] {
			actorSeen[cast.Name] = true
			md.Actors = append(md.Actors, cast.Name)
		}
	}
	return md, raw.PosterPath, raw.BackdropPath, nil
}

// buildImageURL 把 TMDB 相对图片路径拼成完整 URL，空路径返回空串。
func (c *TMDBClient) buildImageURL(relPath, size string) string {
	if relPath == "" {
		return ""
	}
	return c.imageURL + "/" + size + relPath
}

// PosterURL 返回候选卡片用的小尺寸海报完整 URL。
func (c *TMDBClient) PosterURL(relPath string) string {
	return c.buildImageURL(relPath, "w342")
}

// downloadImage 下载 TMDB 图片到指定绝对路径（父目录需已存在）。
func (c *TMDBClient) downloadImage(ctx context.Context, relPath, size, dest string) error {
	imgURL := c.buildImageURL(relPath, size)
	if imgURL == "" {
		return fmt.Errorf("图片路径为空")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("下载 TMDB 图片失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("TMDB 图片返回 %d", resp.StatusCode)
	}
	return writeAtomic(dest, resp.Body)
}

// tmdbYear 从 YYYY-MM-DD 发行日期中取年份。
func tmdbYear(releaseDate string) string {
	if len(releaseDate) >= 4 {
		return releaseDate[:4]
	}
	return ""
}

func firstStudio(companies []struct {
	Name string `json:"name"`
}) string {
	for _, pc := range companies {
		if pc.Name != "" {
			return pc.Name
		}
	}
	return ""
}
