package server

import (
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	// multiUser 标记是否进入多用户 RBAC 模式（users 表至少一个账号）。
	// 单调置真：最后一个管理员受保护，账号不会归零，故无需从真回退到假。
	multiUser atomic.Bool

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
	// 启动时评估是否进入多用户模式（老库可能已存在 users 记录）
	srv.refreshMultiUser()
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

// Handler 构建路由。要求 Go 1.22+ 的增强 ServeMux（方法与路径变量）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 容器健康探针（认证放行，供 Docker HEALTHCHECK 匿名访问）
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /watch/{id}", s.handleWatch)
	// 影片详情页：聚合元信息、进度、收藏/标签与分级「更多类似」推荐（纯只读 SSR）
	mux.HandleFunc("GET /video/{id}", s.handleVideoDetail)
	mux.HandleFunc("GET /shows/{id}", s.handleShowPage)
	// 演员/导演聚合页：名字经路径变量传入，模板侧用 personURL 做路径转义
	mux.HandleFunc("GET /person/{name}", s.handlePerson)
	mux.HandleFunc("GET /stats", s.handleStats)
	// 片库存储分析：admin 专属（system.health），纯内存聚合不依赖 SQLite
	mux.HandleFunc("GET /library/storage", s.requirePerm(auth.PermSystemHealth, s.handleLibraryStorage))
	// 缓存健康检查与孤儿清理：admin 专属（system.health），纯文件扫描不依赖 SQLite
	mux.HandleFunc("GET /cache/health", s.requirePerm(auth.PermSystemHealth, s.handleCacheHealth))
	mux.HandleFunc("POST /api/v1/cache/thumb/orphans", s.requirePerm(auth.PermSystemHealth, s.handleCacheThumbCleanup))
	mux.HandleFunc("POST /api/v1/cache/tmdb/orphans", s.requirePerm(auth.PermSystemHealth, s.handleCacheTMDBCleanup))
	// 目录浏览：通配模式不匹配无尾斜杠的裸路径，根入口与子路径需分别注册
	mux.HandleFunc("GET /browse", s.handleBrowse)
	mux.HandleFunc("GET /browse/{rest...}", s.handleBrowse)
	mux.HandleFunc("GET /search", s.handleSearch)
	// 智能清单：无状态实时规则视图，中心页与规则详情分开注册
	mux.HandleFunc("GET /smart", s.handleSmartPlaylists)
	mux.HandleFunc("GET /smart/{rule}", s.handleSmartPlaylistDetail)
	mux.HandleFunc("GET /history", s.handleHistory)
	mux.HandleFunc("GET /watch-later", s.handleWatchLaterPage)
	// 续播提醒：聚合所有看到一半的视频，按最近观看排序，展示剩余时长并一键续播（依赖 SQLite）
	mux.HandleFunc("GET /continue", s.handleContinuePage)
	// 我的高分：个人 4 星及以上评分的智能视图
	mux.HandleFunc("GET /my-ratings", s.handleHighRatedPage)
	mux.HandleFunc("GET /new", s.handleNewArrivals)
	mux.HandleFunc("GET /random", s.handleRandom)
	mux.HandleFunc("GET /bookmarks", s.handleBookmarksPage)
	mux.HandleFunc("GET /review", s.handleReview)
	mux.HandleFunc("POST /history/clear", s.handleClearHistory)
	mux.HandleFunc("POST /history/event/delete", s.handleDeleteEvent)
	// 分享管理中心：全量列表、单条撤销、清理过期（share.manage，pm/admin；受鉴权链保护）
	mux.HandleFunc("GET /shares", s.requirePerm(auth.PermShareManage, s.handleShareCenter))
	mux.HandleFunc("POST /shares/delete", s.requirePerm(auth.PermShareManage, s.handleShareRevoke))
	mux.HandleFunc("POST /shares/clean-expired", s.requirePerm(auth.PermShareManage, s.handleShareCleanExpired))
	mux.HandleFunc("GET /api/v1/shows", s.handleAPIListShows)
	mux.HandleFunc("GET /api/v1/shows/{id}", s.handleAPIGetShow)
	mux.HandleFunc("GET /api/v1/videos", s.handleAPIList)
	mux.HandleFunc("GET /api/v1/videos/{id}", s.handleAPIGet)
	mux.HandleFunc("GET /api/v1/videos/{id}/metadata", s.handleAPIMetadata)
	// 手动编辑元信息：pm/admin（metadata.edit）；仅依赖可写缓存目录，未配置时 handler 返回 503
	mux.HandleFunc("PUT /api/v1/videos/{id}/metadata", s.requirePerm(auth.PermMetadataEdit, s.handleMetadataEdit))
	// 重新扫描媒体库：仅 admin（library.scan）
	mux.HandleFunc("POST /api/v1/rescan", s.requirePerm(auth.PermLibraryScan, s.handleRescan))

	// 本地海报/背景图（NFO/命名约定/TMDB 缓存发现，仅提供已登记的图片）
	mux.HandleFunc("GET /artwork/{id}/poster", s.handleArtwork("poster"))
	mux.HandleFunc("GET /artwork/{id}/backdrop", s.handleArtwork("backdrop"))

	// TMDB 在线刮削（未配置 TMDB_API_KEY 时返回 503）；刮削写元信息，需 metadata.edit（pm/admin）
	mux.HandleFunc("GET /api/v1/videos/{id}/scrape", s.requirePerm(auth.PermMetadataEdit, s.handleScrapeSearch))
	mux.HandleFunc("POST /api/v1/videos/{id}/scrape", s.requirePerm(auth.PermMetadataEdit, s.handleScrapeApply))
	// 全库批量刮削：POST 启动（202/409/503），GET 轮询进度，均需 metadata.edit
	mux.HandleFunc("POST /api/v1/scrape/bulk", s.requirePerm(auth.PermMetadataEdit, s.handleBulkScrapeStart))
	mux.HandleFunc("GET /api/v1/scrape/bulk", s.requirePerm(auth.PermMetadataEdit, s.handleBulkScrapeStatus))

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
	// 稍后再看收件箱（仅在启用持久化时可用，否则返回 503）
	mux.HandleFunc("GET /watch-later/list", s.handleWatchLaterList)
	mux.HandleFunc("POST /api/v1/videos/{id}/watch-later", s.handleWatchLaterAdd)
	mux.HandleFunc("DELETE /api/v1/videos/{id}/watch-later", s.handleWatchLaterRemove)
	// 个人 1-5 星评分：读/写/清除，未启用持久化时统一 503
	mux.HandleFunc("GET /api/v1/videos/{id}/rating", s.handleRatingGet)
	mux.HandleFunc("PUT /api/v1/videos/{id}/rating", s.handleRatingPut)
	mux.HandleFunc("DELETE /api/v1/videos/{id}/rating", s.handleRatingDelete)
	mux.HandleFunc("GET /api/v1/videos/{id}/watch-later", s.handleWatchLaterGet)

	// 视频标签（仅在启用持久化时可用，否则返回 503）
	mux.HandleFunc("GET /api/v1/tags", s.handleTagList)
	mux.HandleFunc("GET /api/v1/videos/{id}/tags", s.handleVideoTagList)
	mux.HandleFunc("PUT /api/v1/videos/{id}/tags", s.requirePerm(auth.PermContentOrg, s.handleVideoTagSet))
	mux.HandleFunc("DELETE /api/v1/videos/{id}/tags/{tagId}", s.requirePerm(auth.PermContentOrg, s.handleVideoTagRemove))
	// 标签全局管理与首页批量操作（仅在启用持久化时可用，否则返回 503）
	mux.HandleFunc("PATCH /api/v1/tags/{tagId}", s.requirePerm(auth.PermContentOrg, s.handleTagRename))
	mux.HandleFunc("DELETE /api/v1/tags/{tagId}", s.requirePerm(auth.PermContentOrg, s.handleTagDelete))
	mux.HandleFunc("POST /api/v1/tags/batch-set", s.requirePerm(auth.PermContentOrg, s.handleBatchTagSet))
	mux.HandleFunc("POST /api/v1/tags/batch-remove", s.requirePerm(auth.PermContentOrg, s.handleBatchTagRemove))

	// 播放列表/合集（仅在启用持久化时可用，否则返回 503）
	mux.HandleFunc("GET /api/v1/playlists", s.handlePlaylistList)
	mux.HandleFunc("POST /api/v1/playlists", s.requirePerm(auth.PermContentOrg, s.handlePlaylistCreate))
	mux.HandleFunc("GET /api/v1/playlists/{id}", s.handlePlaylistGet)
	mux.HandleFunc("PATCH /api/v1/playlists/{id}", s.requirePerm(auth.PermContentOrg, s.handlePlaylistRename))
	mux.HandleFunc("DELETE /api/v1/playlists/{id}", s.requirePerm(auth.PermContentOrg, s.handlePlaylistDelete))
	mux.HandleFunc("POST /api/v1/playlists/{id}/items", s.requirePerm(auth.PermContentOrg, s.handlePlaylistAddItem))
	mux.HandleFunc("DELETE /api/v1/playlists/{id}/items/{videoId}", s.requirePerm(auth.PermContentOrg, s.handlePlaylistRemoveItem))
	mux.HandleFunc("POST /api/v1/playlists/{id}/items/{videoId}/move", s.requirePerm(auth.PermContentOrg, s.handlePlaylistMoveItem))
	mux.HandleFunc("POST /api/v1/shows/{id}/playlist", s.requirePerm(auth.PermContentOrg, s.handleShowAddToPlaylist))
	mux.HandleFunc("GET /api/v1/videos/{id}/playlists", s.handleVideoPlaylists)
	// 合集（播放列表）独立 SSR 详情页，与 /api/v1 前缀的 JSON 接口区分
	mux.HandleFunc("GET /playlists/{id}", s.handlePlaylistPage)
	// 播放列表备份迁移：导出为 JSON 文件、从 JSON 新建导入（仅在启用持久化时可用，否则 503）
	mux.HandleFunc("GET /api/v1/export/playlists.json", s.handleExportPlaylists)
	mux.HandleFunc("POST /api/v1/import/playlists", s.requirePerm(auth.PermContentOrg, s.handleImportPlaylists))
	// 数据健康检查：SQLite 孤儿记录体检与显式按表/一键清理（仅在启用持久化时可用，否则 503）
	mux.HandleFunc("GET /data/health", s.requirePerm(auth.PermSystemHealth, s.handleDataHealthPage))
	mux.HandleFunc("POST /api/v1/data-health/clean", s.requirePerm(auth.PermSystemHealth, s.handleDataHealthClean))
	mux.HandleFunc("POST /api/v1/data-health/clean-all", s.requirePerm(auth.PermSystemHealth, s.handleDataHealthCleanAll))
	mux.HandleFunc("GET /stream/{id}", s.handleStream)
	mux.HandleFunc("GET /thumb/{id}", s.handleThumb)
	mux.HandleFunc("GET /thumb-placeholder.svg", s.handlePlaceholder)

	// 匿名文件分享（公开链路，isPublicPath 已放行 /share/ 前缀）：落地页、提取码校验、下载
	mux.HandleFunc("GET /share/{token}", s.handleSharePage)
	mux.HandleFunc("POST /share/{token}/verify", s.handleShareVerify)
	mux.HandleFunc("GET /share/{token}/download", s.handleShareDownload)

	// 分享管理（share.manage，pm/admin；匿名 /share 链路不受影响）：创建、列表、撤销
	mux.HandleFunc("POST /api/v1/videos/{id}/shares", s.requirePerm(auth.PermShareManage, s.handleShareCreate))
	mux.HandleFunc("GET /api/v1/videos/{id}/shares", s.requirePerm(auth.PermShareManage, s.handleShareList))
	mux.HandleFunc("DELETE /api/v1/shares/{token}", s.requirePerm(auth.PermShareManage, s.handleShareDelete))

	// HLS 在线转码（可选）：状态轮询（?q= 清晰度）、按档位的 m3u8/分片、视频级共享内嵌字幕。
	// ServeMux 通配段不允许与字面段（subs）同级共存，故用 {rest...} 收敛后在 handler 内分发。
	mux.HandleFunc("GET /api/v1/videos/{id}/transcode", s.handleTranscodeStatus)
	mux.HandleFunc("GET /hls/{id}/{rest...}", s.handleHLS)

	// 转码任务管理（仅 admin，transcode.admin）：管理页、会话快照轮询、终止并清理单个会话
	mux.HandleFunc("GET /transcode/admin", s.requirePerm(auth.PermTranscodeAdmin, s.handleTranscodeAdminPage))
	mux.HandleFunc("GET /api/v1/transcode/sessions", s.requirePerm(auth.PermTranscodeAdmin, s.handleTranscodeAdminList))
	mux.HandleFunc("POST /api/v1/transcode/sessions/{id}/stop", s.requirePerm(auth.PermTranscodeAdmin, s.handleTranscodeAdminStop))

	// 外挂字幕：列表与 WebVTT 轨道（原画/HLS 两种模式通用）；上传/删除属元信息维护，需 metadata.edit
	mux.HandleFunc("GET /api/v1/videos/{id}/subtitles", s.handleSubtitleList)
	mux.HandleFunc("POST /api/v1/videos/{id}/subtitles", s.requirePerm(auth.PermMetadataEdit, s.handleSubtitleUpload))
	mux.HandleFunc("DELETE /api/v1/videos/{id}/subtitles/{sid}", s.requirePerm(auth.PermMetadataEdit, s.handleSubtitleDelete))
	mux.HandleFunc("GET /subtitles/{id}/{sid}/track.vtt", s.handleSubtitleTrack)

	// 认证相关路由（未开启密码时 /login 直接回首页）
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)

	// 用户与角色管理（仅 admin，user.manage 权限；依赖持久化，无 DB 时 handler 503）
	mux.HandleFunc("GET /admin/users", s.requirePerm(auth.PermUserManage, s.handleUserAdminPage))
	mux.HandleFunc("POST /api/v1/admin/users", s.requirePerm(auth.PermUserManage, s.handleUserCreate))
	mux.HandleFunc("PATCH /api/v1/admin/users/{uid}/role", s.requirePerm(auth.PermUserManage, s.handleUserUpdateRole))
	mux.HandleFunc("POST /api/v1/admin/users/{uid}/password", s.requirePerm(auth.PermUserManage, s.handleUserResetPassword))
	mux.HandleFunc("DELETE /api/v1/admin/users/{uid}", s.requirePerm(auth.PermUserManage, s.handleUserDelete))

	// Service Worker 根路径挂载：突破 /static/ 默认作用域，管控整站
	mux.HandleFunc("GET /sw.js", s.handleServiceWorker)

	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		log.Fatalf("加载静态资源失败: %v", err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	return s.recoverPanic(s.logRequest(s.requireAuth(mux)))
}
