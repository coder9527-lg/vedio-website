package server

import (
	"encoding/json"
	"net/http"
)

// handleScrapeSearch 用清洗后的文件名搜索 TMDB 候选影片。
func (s *Server) handleScrapeSearch(w http.ResponseWriter, r *http.Request) {
	if !s.scraperEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "在线刮削未启用（未配置 TMDB_API_KEY）",
		})
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	hits, err := s.scraper.Search(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"count": len(hits),
		"hits":  hits,
	})
}

// scrapeApplyRequest 是选定候选时的请求体。
type scrapeApplyRequest struct {
	TMDBID int64 `json:"tmdbId"`
}

// handleScrapeApply 选定 TMDB 影片后拉详情、下载图片并合并元信息。
func (s *Server) handleScrapeApply(w http.ResponseWriter, r *http.Request) {
	if !s.scraperEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "在线刮削未启用（未配置 TMDB_API_KEY）",
		})
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	var req scrapeApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	if req.TMDBID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tmdbId 必须为正整数"})
		return
	}
	md, err := s.scraper.Apply(r.Context(), id, req.TMDBID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, md)
}

// scraperEnabled 供 handler 与模板判断在线刮削是否可用。
func (s *Server) scraperEnabled() bool { return s.scraper != nil && s.scraper.Available() }
