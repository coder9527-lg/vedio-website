package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// ---------- 页面 ----------

// basePage 存放所有页面共用的模板字段。
type basePage struct {
	Title       string
	AuthEnabled bool
}

// 允许的排序白名单，防止把任意字符串透传到查询层。
var allowedSorts = map[string]bool{
	"": true, "name": true, "newest": true, "oldest": true, "size": true,
}

type indexData struct {
	basePage
	Videos    []*media.Video
	Cards     []homeCard                 // 首页卡片：默认视图下剧集成员折叠为剧集卡片
	Metadata  map[string]*media.Metadata // 视频 ID → 元信息（无元信息的视频不在 map 中）
	Count     int                        // 当前视图下的视频数
	Total     int                        // 媒体库视频总数
	State     videoState
	Query     string                  // 搜索关键字
	Sort      string                  // 当前排序
	View      string                  // all | favorites | continue | playlists
	Type      string                  // all | movie | show
	Genre     string                  // 当前类型筛选（空=全部）
	Genres    []string                // 全库可用类型标签（chips 数据源）
	Params    map[string]string       // 当前五元组快照，供 indexURL 保持筛选正交
	StoreOn   bool                    // 是否启用持久化
	ScrapeOn  bool                    // 是否启用 TMDB 在线刮削
	Playlists []store.PlaylistSummary // view=playlists 时的列表摘要

	// 首页横向行（仅默认视图、无搜索/筛选时填充）：继续观看与最近加入
	ContinueCards []shelfCard
	RecentCards   []shelfCard
}

// shelfCard 是首页横向行（继续观看/最近加入）的单张卡片数据。
type shelfCard struct {
	Video    *media.Video
	Progress store.Progress // 继续观看行使用；最近加入行为零值
	Metadata *media.Metadata
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	keyword := q.Get("q")
	sortVal := q.Get("sort")
	if !allowedSorts[sortVal] {
		sortVal = ""
	}
	view := q.Get("view")
	if view != "favorites" && view != "continue" && view != "playlists" {
		view = "all"
	}
	typeFilter := normalizeTypeFilter(q.Get("type"))
	genre := strings.TrimSpace(q.Get("genre"))

	query := media.Query{Keyword: keyword, Sort: sortVal, Genre: genre}
	state := s.loadVideoState()
	switch view {
	case "playlists":
		// 播放列表视图不按视频卡片过滤，忽略 type/q/sort/genre，模板渲染专属面板
		query.IDs = nil
		query.Genre = ""
		genre = ""
	case "favorites":
		if len(state.Favs) == 0 {
			query.IDs = map[string]bool{} // 强制空结果
		} else {
			query.IDs = state.Favs
		}
	case "continue":
		ids := make(map[string]bool, len(state.Progress))
		for id := range state.Progress {
			ids[id] = true
		}
		query.IDs = ids
	}

	videos := s.library.Query(query)
	metaMap := make(map[string]*media.Metadata, len(videos))
	for _, v := range videos {
		if md := s.library.Metadata(v.ID); md != nil {
			metaMap[v.ID] = md
		}
	}
	// 仅播放列表视图需要摘要数据，出错时留空由模板显示空态
	var playlists []store.PlaylistSummary
	if view == "playlists" {
		playlists, _ = s.store.ListPlaylists()
	}

	data := indexData{
		basePage: basePage{Title: "我的视频库", AuthEnabled: s.authEnabled()},
		Videos:   videos,
		Cards:    s.buildHomeCards(videos, typeFilter),
		Metadata: metaMap,
		Count:    len(videos),
		Total:    s.library.Len(),
		State:    state,
		Query:    keyword,
		Sort:     sortVal,
		View:     view,
		Type:     typeFilter,
		Genre:    genre,
		Genres:   s.library.AllGenres(),
		Params: map[string]string{
			"type":  typeFilter,
			"view":  view,
			"sort":  sortVal,
			"q":     keyword,
			"genre": genre,
		},
		StoreOn:   s.storeEnabled(),
		ScrapeOn:  s.scraperEnabled(),
		Playlists: playlists,
	}
	// 横向行只在干净的默认视图展示：全部类型、全部视图、无搜索、无排序，避免与筛选结果语义冲突
	if s.storeEnabled() && view == "all" && typeFilter == "all" && keyword == "" && sortVal == "" {
		data.ContinueCards = s.buildContinueShelf()
		data.RecentCards = s.buildRecentShelf()
	}
	s.render(w, "index.html", data)
}

// shelfSize 限制每个横向行的卡片数量。
const shelfSize = 12

// buildContinueShelf 组装「继续观看」行：复用 progress 表最近更新记录，
// 过滤已看完（>=95%）与片头抖动（<5s），悬空视频自动跳过，保持 RecentProgress 的有序结果。
func (s *Server) buildContinueShelf() []shelfCard {
	recent, err := s.store.RecentProgress(shelfSize)
	if err != nil {
		log.Printf("读取继续观看失败: %v", err)
		return nil
	}
	cards := make([]shelfCard, 0, len(recent))
	for _, p := range recent {
		if !hasProgressValue(p.Position, p.Duration) {
			continue
		}
		v, ok := s.library.Get(p.VideoID)
		if !ok {
			continue // 视频文件已删除，悬空进度跳过
		}
		cards = append(cards, shelfCard{
			Video:    v,
			Progress: p,
			Metadata: s.library.Metadata(v.ID),
		})
	}
	return cards
}

// buildRecentShelf 组装「最近加入」行：复用文件 ModTime 语义，按修改时间倒序取前 N 个。
func (s *Server) buildRecentShelf() []shelfCard {
	videos := s.library.Query(media.Query{Sort: "newest", Limit: shelfSize})
	cards := make([]shelfCard, 0, len(videos))
	for _, v := range videos {
		cards = append(cards, shelfCard{Video: v, Metadata: s.library.Metadata(v.ID)})
	}
	return cards
}

// hasProgressValue 与模板 hasProgress 同口径：位置 >5s 且未看到 95% 以上。
func hasProgressValue(position, duration float64) bool {
	if position < 5 {
		return false
	}
	if duration > 0 && position/duration >= 0.95 {
		return false
	}
	return true
}

func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	state := s.loadVideoState()
	// 片头片尾跳过设置（仅 watch 页单视频点查；读取失败静默降级，不阻断播放页）
	var skip *store.SkipSetting
	// 视频书签（同上，仅 watch 页点查；失败降级为空列表，不阻断播放页）
	var bookmarks []store.Bookmark
	var bookmarksJSON template.JS
	if s.storeEnabled() {
		got, err := s.store.GetSkipSetting(v.ID)
		if err != nil {
			log.Printf("读取跳过设置失败: %v", err)
			skip = nil
		} else {
			skip = got
		}
		bookmarks, err = s.store.ListBookmarks(v.ID)
		if err != nil {
			log.Printf("读取书签失败: %v", err)
			bookmarks = nil
		}
		if raw, mErr := json.Marshal(bookmarks); mErr == nil {
			bookmarksJSON = template.JS(raw)
		}
	}
	subtitles := s.library.Subtitles(v.ID)
	if subtitles == nil {
		subtitles = []*media.Subtitle{}
	}
	md := s.library.Metadata(v.ID)
	if md == nil {
		md = &media.Metadata{}
	}
	// 剧集成员：带上所属剧集 ID 与下一集（下一集作为无播放列表上下文时的连播回填）
	showID, _ := s.library.ShowIDOf(v.ID)
	var nextVideo *media.Video
	if showID != "" {
		nextVideo, _ = s.library.NextEpisode(v.ID)
	}

	// 连播决策由 handler 统一输出 AutoNext 三字段，模板无需分支：
	// 1. 合法 playlist 且当前视频在列表中：列表连播绝对优先，末项停止不回退
	// 2. 无/非法/已删 playlist（静默忽略）：剧集成员回填 NextEpisode，电影无卡片
	var (
		playlistID   string
		autoNextURL  string
		autoNextName string
	)
	if pid := r.URL.Query().Get("playlist"); pid != "" && s.storeEnabled() {
		if pl, err := s.store.GetPlaylist(pid); err == nil && pl != nil {
			found := false
			for _, it := range pl.Items {
				if !found {
					if it.VideoID == v.ID {
						found = true
					}
					continue
				}
				// 按 sort_order 找当前条目之后的第一个有效视频，连续跳过悬空 ID
				if nv, ok := s.library.Get(it.VideoID); ok {
					playlistID = pid
					autoNextURL = "/watch/" + nv.ID + "?playlist=" + pid
					autoNextName = nv.Name
					break
				}
			}
		}
	}
	if autoNextURL == "" && nextVideo != nil {
		autoNextURL = "/watch/" + nextVideo.ID
		autoNextName = nextVideo.Name
	}
	s.render(w, "watch.html", struct {
		basePage
		Video          *media.Video
		Metadata       *media.Metadata
		State          videoState
		StoreOn        bool
		TranscodeOn    bool
		ScrapeOn       bool
		MetadataEditOn bool // 是否启用元信息手动编辑（仅依赖可写缓存目录，与刮削开关解耦）
		NativePlayable bool
		Subtitles      []*media.Subtitle
		ShowID         string
		PlaylistID     string
		AutoNextURL    string
		AutoNextName   string
		Skip           *store.SkipSetting
		Bookmarks      []store.Bookmark
		BookmarksJSON  template.JS
	}{
		basePage:       basePage{Title: v.Name, AuthEnabled: s.authEnabled()},
		Video:          v,
		Metadata:       md,
		State:          state,
		StoreOn:        s.storeEnabled(),
		TranscodeOn:    s.transcodeEnabled(),
		ScrapeOn:       s.scraperEnabled(),
		MetadataEditOn: s.metadataEditEnabled(),
		NativePlayable: isNativePlayable(v.Ext),
		Subtitles:      subtitles,
		ShowID:         showID,
		PlaylistID:     playlistID,
		AutoNextURL:    autoNextURL,
		AutoNextName:   autoNextName,
		Skip:           skip,
		Bookmarks:      bookmarks,
		BookmarksJSON:  bookmarksJSON,
	})
}

// ---------- JSON API ----------

func (s *Server) handleAPIList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sortVal := q.Get("sort")
	if !allowedSorts[sortVal] {
		sortVal = ""
	}
	view := q.Get("view")
	state := s.loadVideoState()
	query := media.Query{Keyword: q.Get("q"), Sort: sortVal}
	switch view {
	case "favorites":
		query.IDs = state.Favs
	case "continue":
		ids := make(map[string]bool, len(state.Progress))
		for id := range state.Progress {
			ids[id] = true
		}
		query.IDs = ids
	}
	videos := s.library.Query(query)
	writeJSON(w, http.StatusOK, map[string]any{
		"count":  len(videos),
		"total":  s.library.Len(),
		"videos": videos,
	})
}

func (s *Server) handleAPIGet(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleRescan(w http.ResponseWriter, r *http.Request) {
	n, err := s.library.Scan()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// 清理指向已删除视频的孤儿进度/收藏记录
	if s.storeEnabled() {
		existing := make(map[string]bool, n)
		for _, v := range s.library.List() {
			existing[v.ID] = true
		}
		if err := s.store.Prune(existing); err != nil {
			log.Printf("清理孤儿持久化记录失败: %v", err)
		}
	}
	log.Printf("重新扫描完成，索引视频 %d 个", n)
	writeJSON(w, http.StatusOK, map[string]any{"indexed": n})
}

// ---------- 视频流（支持 Range / 拖动进度条）----------

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// 在线播放不强制下载头；?download=1 时由 serveVideoFile 附加 attachment
	serveVideoFile(w, r, v, r.URL.Query().Has("download"))
}

// serveVideoFile 打开视频并通过 http.ServeContent 输出，原生支持 Range/条件请求。
// forceAttachment=true 时强制下载文件名（匿名分享下载复用此入口）。
func serveVideoFile(w http.ResponseWriter, r *http.Request, v *media.Video, forceAttachment bool) {
	file, err := os.Open(v.AbsPath())
	if err != nil {
		http.Error(w, "无法打开视频文件", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		http.Error(w, "无法读取文件信息", http.StatusInternalServerError)
		return
	}

	if forceAttachment {
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename*=UTF-8''%s", urlEncode(v.Name+v.Ext)))
	}
	w.Header().Set("Content-Type", mimeByExt(v.Ext))
	w.Header().Set("Accept-Ranges", "bytes")

	// http.ServeContent 原生处理 Range 请求、If-Modified-Since 与 HEAD
	http.ServeContent(w, r, v.Name+v.Ext, stat.ModTime(), file)
}

// ---------- 缩略图 ----------

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	path, err := s.thumbs.Ensure(v)
	if err != nil {
		if !s.thumbs.Available() {
			log.Printf("ffmpeg 不可用，返回占位图: %v", err)
		} else {
			log.Printf("生成缩略图失败 %s: %v", v.RelPath, err)
		}
		s.handlePlaceholder(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	http.ServeFile(w, r, path)
}

func (s *Server) handlePlaceholder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(placeholderSVG))
}

// ---------- 中间件与工具 ----------

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.RequestURI(), rec.status, time.Since(start).Round(time.Microsecond))
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v", rec)
				http.Error(w, "服务器内部错误", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("渲染模板 %s 失败: %v", name, err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		log.Printf("写 JSON 响应失败: %v", err)
	}
}

// isNativePlayable 判断该容器在主流浏览器中通常可直接用 <video> 播放
// （注意 MKV 即使含 H.264 也常因容器支持问题失败，因此归入转码候选）。
func isNativePlayable(ext string) bool {
	switch ext {
	case ".mp4", ".m4v", ".webm", ".mov":
		return true
	default:
		return false
	}
}

func mimeByExt(ext string) string {
	switch ext {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".ts":
		return "video/mp2t"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".wmv":
		return "video/x-ms-wmv"
	case ".flv":
		return "video/x-flv"
	case ".mpeg", ".mpg":
		return "video/mpeg"
	case ".3gp":
		return "video/3gpp"
	default:
		return "application/octet-stream"
	}
}

func urlEncode(s string) string {
	// RFC 5987 filename* 编码：保留 ASCII 安全字符，其余按 UTF-8 百分号编码
	var b []byte
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b = append(b, byte(r))
		case r == '-' || r == '_' || r == '.' || r == '~':
			b = append(b, byte(r))
		default:
			for _, c := range []byte(string(r)) {
				b = append(b, '%', hexUpper[c>>4], hexUpper[c&0x0f])
			}
		}
	}
	return string(b)
}

const hexUpper = "0123456789ABCDEF"

const placeholderSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="480" height="270" viewBox="0 0 480 270">
  <rect width="480" height="270" fill="#1f2937"/>
  <polygon points="205,100 205,170 275,135" fill="#6b7280"/>
  <text x="240" y="210" font-family="sans-serif" font-size="18" fill="#9ca3af" text-anchor="middle">暂无缩略图</text>
</svg>`
