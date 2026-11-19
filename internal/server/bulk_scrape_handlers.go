package server

import (
	"errors"
	"net/http"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// handleBulkScrapeStart 启动全库批量刮削（仅处理缺失元信息的视频）。
// 任务在后台异步执行，立即返回 202 与当前状态快照：
//   - 刮削未启用（未配置 TMDB_API_KEY）→ 503
//   - 已有批量任务运行中 → 409
func (s *Server) handleBulkScrapeStart(w http.ResponseWriter, r *http.Request) {
	if !s.scraperEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "在线刮削未启用（未配置 TMDB_API_KEY）",
		})
		return
	}
	st, err := s.bulkScraper.StartBulk()
	if err != nil {
		if errors.Is(err, media.ErrBulkRunning) {
			// 运行中再次启动：回传当前进度，前端可直接切到进度视图
			writeJSON(w, http.StatusConflict, st)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

// handleBulkScrapeStatus 返回最近一次/当前批量任务的进度快照。
// 从未启动过时返回 running=false 的零值状态。刮削未启用 → 503。
func (s *Server) handleBulkScrapeStatus(w http.ResponseWriter, r *http.Request) {
	if !s.scraperEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "在线刮削未启用（未配置 TMDB_API_KEY）",
		})
		return
	}
	writeJSON(w, http.StatusOK, s.bulkScraper.Status())
}
