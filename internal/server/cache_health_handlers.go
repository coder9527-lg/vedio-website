package server

import (
	"errors"
	"log"
	"net/http"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// cacheEntryView 是缓存条目的展示模型，时间与字节在 handler 预格式化，模板不做换算。
type cacheEntryView struct {
	Name      string
	SizeText  string
	ModTime   string
	Orphan    bool
	Protected bool
	IsDir     bool
	Missing   bool
}

// cacheReportView 是一类缓存体检报告的展示模型。
type cacheReportView struct {
	Kind          string
	Title         string
	Configured    bool
	Exists        bool
	TotalCount    int
	TotalSizeText string
	Orphans       []cacheEntryView
	Unidentified  []cacheEntryView
	Missing       []cacheEntryView
	MissingCount  int
	ReclaimText   string // 可回收（非保护期孤儿）字节的人类可读文本
}

// cacheHealthData 是缓存健康检查页的模板数据，纯文件扫描，不依赖 SQLite。
type cacheHealthData struct {
	basePage
	Reports []cacheReportView // 固定顺序：缩略图缓存、TMDB 刮削缓存
}

// 时间展示格式：tmplFuncs 无时间格式化函数，统一在 handler 预格式化。
const cacheTimeLayout = "2006-01-02 15:04"

// handleCacheHealth 渲染缓存健康检查页：缩略图与 TMDB 刮削缓存的孤儿、缺失、未识别清单。
func (s *Server) handleCacheHealth(w http.ResponseWriter, r *http.Request) {
	health := media.InspectCache(s.library, s.thumbs.Dir(), s.library.ScraperImageDir())
	data := cacheHealthData{
		basePage: basePage{Title: "缓存健康检查", AuthEnabled: s.authEnabled()},
		Reports: []cacheReportView{
			toCacheReportView("缩略图缓存", health.Thumb),
			toCacheReportView("TMDB 刮削缓存", health.TMDB),
		},
	}
	s.render(w, "cache.html", data)
}

// handleCacheThumbCleanup 清理缩略图孤儿文件；空库 409，成功返回删除数与回收字节。
func (s *Server) handleCacheThumbCleanup(w http.ResponseWriter, r *http.Request) {
	removed, reclaimed, err := media.DeleteThumbOrphans(s.library, s.thumbs.Dir())
	if err != nil {
		writeCacheCleanupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{
		"removed":   int64(removed),
		"reclaimed": reclaimed,
	})
}

// handleCacheTMDBCleanup 清理 TMDB 孤儿子目录；空库 409，成功返回删除数与回收字节。
func (s *Server) handleCacheTMDBCleanup(w http.ResponseWriter, r *http.Request) {
	removed, reclaimed, err := media.DeleteScraperOrphans(s.library, s.library.ScraperImageDir())
	if err != nil {
		writeCacheCleanupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{
		"removed":   int64(removed),
		"reclaimed": reclaimed,
	})
}

// writeCacheCleanupError 把清理错误映射为 HTTP 状态：空库护栏 409，其余 500。
func writeCacheCleanupError(w http.ResponseWriter, err error) {
	if errors.Is(err, media.ErrLibraryEmpty) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "媒体库为空，已阻止清理以防止误判",
		})
		return
	}
	log.Printf("缓存清理失败: %v", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{
		"error": "缓存清理失败，请查看服务日志",
	})
}

// toCacheReportView 把 media 层报告转换为预格式化的展示模型。
func toCacheReportView(title string, rep media.CacheReport) cacheReportView {
	view := cacheReportView{
		Kind:          rep.Kind,
		Title:         title,
		Configured:    rep.Configured,
		Exists:        rep.Exists,
		TotalCount:    rep.TotalCount,
		TotalSizeText: humanSize(rep.TotalSize),
		Orphans:       make([]cacheEntryView, 0, len(rep.Orphans)),
		Unidentified:  make([]cacheEntryView, 0, len(rep.Unidentified)),
		Missing:       make([]cacheEntryView, 0, len(rep.Missing)),
		MissingCount:  rep.MissingCount,
	}
	var reclaim int64
	for _, e := range rep.Orphans {
		if !e.Protected {
			reclaim += e.Size
		}
		view.Orphans = append(view.Orphans, toCacheEntryView(e))
	}
	for _, e := range rep.Unidentified {
		view.Unidentified = append(view.Unidentified, toCacheEntryView(e))
	}
	for _, e := range rep.Missing {
		view.Missing = append(view.Missing, toCacheEntryView(e))
	}
	view.ReclaimText = humanSize(reclaim)
	return view
}

// toCacheEntryView 单条条目转换；缺失条目无 mtime，时间文本留空。
func toCacheEntryView(e media.CacheEntry) cacheEntryView {
	v := cacheEntryView{
		Name:      e.Name,
		SizeText:  humanSize(e.Size),
		Orphan:    e.Orphan,
		Protected: e.Protected,
		IsDir:     e.IsDir,
		Missing:   e.Missing,
	}
	if !e.ModTime.IsZero() {
		v.ModTime = e.ModTime.Format(cacheTimeLayout)
	}
	return v
}
