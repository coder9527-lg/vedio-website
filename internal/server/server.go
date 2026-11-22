package server

import (
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/auth"
	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

//go:embed templates static
var assets embed.FS

// Server 持有路由与全部依赖。
type Server struct {
	library       *media.Library
	thumbs        *media.Thumbnailer
	transcoder    *media.Transcoder  // nil 表示未启用 HLS 在线转码
	store         *store.Store       // nil 表示未启用持久化（播放进度/收藏不可用）
	scraper       *media.Scraper     // nil 表示未启用 TMDB 在线刮削
	bulkScraper   *media.BulkScraper // 全库批量刮削编排器，与 scraper 同时启用/关闭
	tmpl          *template.Template
	authenticator *auth.Authenticator // nil 表示未开启访问密码
	shareLimiter  *auth.FailLimiter   // 匿名分享提取码的按 IP+token 失败限流
	cookieSecure  bool

	// 播放会话 → watch_events 行 ID 的内存索引。按 videoID 维度：同一视频的并发
	// 标签页会复用一行（可接受的折衷）；服务重启后 map 清空，下次上报自动补建新行。
	watchSessionMu sync.Mutex
	watchSession   map[string]int64
}

func New(library *media.Library, thumbs *media.Thumbnailer, transcoder *media.Transcoder, st *store.Store, authenticator *auth.Authenticator, scraper *media.Scraper, cookieSecure bool) (*Server, error) {
	tmpl, err := template.New("").Funcs(tmplFuncs).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	srv := &Server{
		library:       library,
		thumbs:        thumbs,
		transcoder:    transcoder,
		store:         st,
		scraper:       scraper,
		tmpl:          tmpl,
		authenticator: authenticator,
		// 与登录限流同量级：单 IP 对同一分享 10 分钟内错 10 次即锁到窗口结束，
		// 防止提取码被脚本化在线爆破（下载工具的 query 传参同样受此约束）。
		shareLimiter: auth.NewFailLimiter(10, 10*time.Minute),
		cookieSecure: cookieSecure,
		watchSession: map[string]int64{},
	}
	// 批量刮削与单视频刮削同生命周期：scraper 为 nil（未配 TMDB_API_KEY）时保持 nil
	if scraper != nil {
		srv.bulkScraper = media.NewBulkScraper(scraper, library)
	}
	return srv, nil
}

// storeEnabled 判断是否启用了 SQLite 持久化。
func (s *Server) storeEnabled() bool { return s.store != nil }

// authEnabled 供模板与处理器判断是否展示登录相关元素。
func (s *Server) authEnabled() bool { return s.authenticator != nil }

// isPublicPath 开启认证后仍允许匿名访问的路径前缀/精确路径。
func isPublicPath(path string) bool {
	if path == "/login" || path == "/healthz" {
		return true
	}
	return strings.HasPrefix(path, "/static/") ||
		strings.HasPrefix(path, "/share/") ||
		path == "/thumb-placeholder.svg" ||
		path == "/favicon.ico"
}

// requireAuth 在开启访问密码时拦截未登录请求：
// API 返回 401 JSON，页面请求 302 跳转到登录页并携带回跳地址。
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled() || isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if err := s.authenticator.CheckRequest(r); err == nil {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		loc := "/login"
		if r.URL.Path != "/" && r.URL.Path != "" {
			loc = "/login?next=" + urlEncode(r.URL.RequestURI())
		}
		http.Redirect(w, r, loc, http.StatusSeeOther)
	})
}

// Handler 构建路由。要求 Go 1.22+ 的增强 ServeMux（方法与路径变量）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 容器健康探针（认证放行，供 Docker HEALTHCHECK 匿名访问）
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /watch/{id}", s.handleWatch)
	mux.HandleFunc("GET /shows/{id}", s.handleShowPage)
	mux.HandleFunc("GET /stats", s.handleStats)
	// 片库存储分析：纯内存聚合，不依赖 SQLite，未启用持久化也可访问
	mux.HandleFunc("GET /library/storage", s.handleLibraryStorage)
	// 目录浏览：通配模式不匹配无尾斜杠的裸路径，根入口与子路径需分别注册
	mux.HandleFunc("GET /browse", s.handleBrowse)
	mux.HandleFunc("GET /browse/{rest...}", s.handleBrowse)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /history", s.handleHistory)
	mux.HandleFunc("POST /history/clear", s.handleClearHistory)
	mux.HandleFunc("POST /history/event/delete", s.handleDeleteEvent)
	// 分享管理中心：全量列表、单条撤销、清理过期（受鉴权链保护）
	mux.HandleFunc("GET /shares", s.handleShareCenter)
	mux.HandleFunc("POST /shares/delete", s.handleShareRevoke)
	mux.HandleFunc("POST /shares/clean-expired", s.handleShareCleanExpired)
	mux.HandleFunc("GET /api/v1/shows", s.handleAPIListShows)
	mux.HandleFunc("GET /api/v1/shows/{id}", s.handleAPIGetShow)
	mux.HandleFunc("GET /api/v1/videos", s.handleAPIList)
	mux.HandleFunc("GET /api/v1/videos/{id}", s.handleAPIGet)
	mux.HandleFunc("GET /api/v1/videos/{id}/metadata", s.handleAPIMetadata)
	// 手动编辑元信息（仅依赖可写缓存目录，未配置时 handler 返回 503）
	mux.HandleFunc("PUT /api/v1/videos/{id}/metadata", s.handleMetadataEdit)
	mux.HandleFunc("POST /api/v1/rescan", s.handleRescan)

	// 本地海报/背景图（NFO/命名约定/TMDB 缓存发现，仅提供已登记的图片）
	mux.HandleFunc("GET /artwork/{id}/poster", s.handleArtwork("poster"))
	mux.HandleFunc("GET /artwork/{id}/backdrop", s.handleArtwork("backdrop"))

	// TMDB 在线刮削（未配置 TMDB_API_KEY 时返回 503）
	mux.HandleFunc("GET /api/v1/videos/{id}/scrape", s.handleScrapeSearch)
	mux.HandleFunc("POST /api/v1/videos/{id}/scrape", s.handleScrapeApply)
	// 全库批量刮削：POST 启动（202/409/503），GET 轮询进度
	mux.HandleFunc("POST /api/v1/scrape/bulk", s.handleBulkScrapeStart)
	mux.HandleFunc("GET /api/v1/scrape/bulk", s.handleBulkScrapeStatus)

	// 播放进度与收藏（仅在启用持久化时可用，否则返回 503）
	mux.HandleFunc("GET /api/v1/progress", s.handleProgressList)
	// 观影数据 CSV 全量导出（仅启用持久化时可用，否则 503）
	mux.HandleFunc("GET /api/v1/export/progress.csv", s.handleExportProgress)
	mux.HandleFunc("PUT /api/v1/videos/{id}/progress", s.handleProgressPut)
	mux.HandleFunc("GET /api/v1/videos/{id}/progress", s.handleProgressGet)
	mux.HandleFunc("DELETE /api/v1/videos/{id}/progress", s.handleProgressDelete)
	// 片头片尾自动跳过设置（仅在启用持久化时可用，否则返回 503）
	mux.HandleFunc("GET /api/v1/videos/{id}/skip", s.handleSkipGet)
	mux.HandleFunc("PUT /api/v1/videos/{id}/skip", s.handleSkipPut)
	mux.HandleFunc("DELETE /api/v1/videos/{id}/skip", s.handleSkipDelete)
	// 视频书签/时间戳笔记（仅在启用持久化时可用，否则返回 503）
	mux.HandleFunc("GET /api/v1/videos/{id}/bookmarks", s.handleBookmarkList)
	mux.HandleFunc("POST /api/v1/videos/{id}/bookmarks", s.handleBookmarkCreate)
	mux.HandleFunc("PATCH /api/v1/videos/{id}/bookmarks/{bid}", s.handleBookmarkUpdate)
	mux.HandleFunc("DELETE /api/v1/videos/{id}/bookmarks/{bid}", s.handleBookmarkDelete)
	mux.HandleFunc("GET /api/v1/favorites", s.handleFavoriteList)
	mux.HandleFunc("POST /api/v1/videos/{id}/favorite", s.handleFavoriteSet)
	mux.HandleFunc("GET /api/v1/videos/{id}/favorite", s.handleFavoriteGet)

	// 播放列表/合集（仅在启用持久化时可用，否则返回 503）
	mux.HandleFunc("GET /api/v1/playlists", s.handlePlaylistList)
	mux.HandleFunc("POST /api/v1/playlists", s.handlePlaylistCreate)
	mux.HandleFunc("GET /api/v1/playlists/{id}", s.handlePlaylistGet)
	mux.HandleFunc("PATCH /api/v1/playlists/{id}", s.handlePlaylistRename)
	mux.HandleFunc("DELETE /api/v1/playlists/{id}", s.handlePlaylistDelete)
	mux.HandleFunc("POST /api/v1/playlists/{id}/items", s.handlePlaylistAddItem)
	mux.HandleFunc("DELETE /api/v1/playlists/{id}/items/{videoId}", s.handlePlaylistRemoveItem)
	mux.HandleFunc("POST /api/v1/playlists/{id}/items/{videoId}/move", s.handlePlaylistMoveItem)
	mux.HandleFunc("POST /api/v1/shows/{id}/playlist", s.handleShowAddToPlaylist)
	mux.HandleFunc("GET /api/v1/videos/{id}/playlists", s.handleVideoPlaylists)
	mux.HandleFunc("GET /stream/{id}", s.handleStream)
	mux.HandleFunc("GET /thumb/{id}", s.handleThumb)
	mux.HandleFunc("GET /thumb-placeholder.svg", s.handlePlaceholder)

	// 匿名文件分享（公开链路，isPublicPath 已放行 /share/ 前缀）：落地页、提取码校验、下载
	mux.HandleFunc("GET /share/{token}", s.handleSharePage)
	mux.HandleFunc("POST /share/{token}/verify", s.handleShareVerify)
	mux.HandleFunc("GET /share/{token}/download", s.handleShareDownload)

	// 分享管理（受访问密码与 requireStore 保护）：创建、列表、撤销
	mux.HandleFunc("POST /api/v1/videos/{id}/shares", s.handleShareCreate)
	mux.HandleFunc("GET /api/v1/videos/{id}/shares", s.handleShareList)
	mux.HandleFunc("DELETE /api/v1/shares/{token}", s.handleShareDelete)

	// HLS 在线转码（可选）：状态轮询（?q= 清晰度）、按档位的 m3u8/分片、视频级共享内嵌字幕。
	// ServeMux 通配段不允许与字面段（subs）同级共存，故用 {rest...} 收敛后在 handler 内分发。
	mux.HandleFunc("GET /api/v1/videos/{id}/transcode", s.handleTranscodeStatus)
	mux.HandleFunc("GET /hls/{id}/{rest...}", s.handleHLS)

	// 外挂字幕：列表与 WebVTT 轨道（原画/HLS 两种模式通用）
	mux.HandleFunc("GET /api/v1/videos/{id}/subtitles", s.handleSubtitleList)
	mux.HandleFunc("POST /api/v1/videos/{id}/subtitles", s.handleSubtitleUpload)
	mux.HandleFunc("DELETE /api/v1/videos/{id}/subtitles/{sid}", s.handleSubtitleDelete)
	mux.HandleFunc("GET /subtitles/{id}/{sid}/track.vtt", s.handleSubtitleTrack)

	// 认证相关路由（未开启密码时 /login 直接回首页）
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)

	// Service Worker 根路径挂载：突破 /static/ 默认作用域，管控整站
	mux.HandleFunc("GET /sw.js", s.handleServiceWorker)

	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		log.Fatalf("加载静态资源失败: %v", err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	return s.recoverPanic(s.logRequest(s.requireAuth(mux)))
}
