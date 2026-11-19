package server

import (
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"

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
	transcoder    *media.Transcoder // nil 表示未启用 HLS 在线转码
	store         *store.Store      // nil 表示未启用持久化（播放进度/收藏不可用）
	scraper       *media.Scraper    // nil 表示未启用 TMDB 在线刮削
	tmpl          *template.Template
	authenticator *auth.Authenticator // nil 表示未开启访问密码
	cookieSecure  bool
}

func New(library *media.Library, thumbs *media.Thumbnailer, transcoder *media.Transcoder, st *store.Store, authenticator *auth.Authenticator, scraper *media.Scraper, cookieSecure bool) (*Server, error) {
	tmpl, err := template.New("").Funcs(tmplFuncs).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{
		library:       library,
		thumbs:        thumbs,
		transcoder:    transcoder,
		store:         st,
		scraper:       scraper,
		tmpl:          tmpl,
		authenticator: authenticator,
		cookieSecure:  cookieSecure,
	}, nil
}

// storeEnabled 判断是否启用了 SQLite 持久化。
func (s *Server) storeEnabled() bool { return s.store != nil }

// authEnabled 供模板与处理器判断是否展示登录相关元素。
func (s *Server) authEnabled() bool { return s.authenticator != nil }

// isPublicPath 开启认证后仍允许匿名访问的路径前缀/精确路径。
func isPublicPath(path string) bool {
	if path == "/login" {
		return true
	}
	return strings.HasPrefix(path, "/static/") ||
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

	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /watch/{id}", s.handleWatch)
	mux.HandleFunc("GET /shows/{id}", s.handleShowPage)
	mux.HandleFunc("GET /api/v1/shows", s.handleAPIListShows)
	mux.HandleFunc("GET /api/v1/shows/{id}", s.handleAPIGetShow)
	mux.HandleFunc("GET /api/v1/videos", s.handleAPIList)
	mux.HandleFunc("GET /api/v1/videos/{id}", s.handleAPIGet)
	mux.HandleFunc("GET /api/v1/videos/{id}/metadata", s.handleAPIMetadata)
	mux.HandleFunc("POST /api/v1/rescan", s.handleRescan)

	// 本地海报/背景图（NFO/命名约定/TMDB 缓存发现，仅提供已登记的图片）
	mux.HandleFunc("GET /artwork/{id}/poster", s.handleArtwork("poster"))
	mux.HandleFunc("GET /artwork/{id}/backdrop", s.handleArtwork("backdrop"))

	// TMDB 在线刮削（未配置 TMDB_API_KEY 时返回 503）
	mux.HandleFunc("GET /api/v1/videos/{id}/scrape", s.handleScrapeSearch)
	mux.HandleFunc("POST /api/v1/videos/{id}/scrape", s.handleScrapeApply)

	// 播放进度与收藏（仅在启用持久化时可用，否则返回 503）
	mux.HandleFunc("GET /api/v1/progress", s.handleProgressList)
	mux.HandleFunc("PUT /api/v1/videos/{id}/progress", s.handleProgressPut)
	mux.HandleFunc("GET /api/v1/videos/{id}/progress", s.handleProgressGet)
	mux.HandleFunc("DELETE /api/v1/videos/{id}/progress", s.handleProgressDelete)
	mux.HandleFunc("GET /api/v1/favorites", s.handleFavoriteList)
	mux.HandleFunc("POST /api/v1/videos/{id}/favorite", s.handleFavoriteSet)
	mux.HandleFunc("GET /api/v1/videos/{id}/favorite", s.handleFavoriteGet)
	mux.HandleFunc("GET /stream/{id}", s.handleStream)
	mux.HandleFunc("GET /thumb/{id}", s.handleThumb)
	mux.HandleFunc("GET /thumb-placeholder.svg", s.handlePlaceholder)

	// HLS 在线转码（可选）：状态轮询、m3u8 播放列表（单音轨 index / 多音轨 master）、分片与变体
	mux.HandleFunc("GET /api/v1/videos/{id}/transcode", s.handleTranscodeStatus)
	mux.HandleFunc("GET /hls/{id}/index.m3u8", s.handleHLSPlaylist)
	mux.HandleFunc("GET /hls/{id}/{asset...}", s.handleHLSAsset)

	// 外挂字幕：列表与 WebVTT 轨道（原画/HLS 两种模式通用）
	mux.HandleFunc("GET /api/v1/videos/{id}/subtitles", s.handleSubtitleList)
	mux.HandleFunc("GET /subtitles/{id}/{sid}/track.vtt", s.handleSubtitleTrack)

	// 认证相关路由（未开启密码时 /login 直接回首页）
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)

	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		log.Fatalf("加载静态资源失败: %v", err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	return s.recoverPanic(s.logRequest(s.requireAuth(mux)))
}
