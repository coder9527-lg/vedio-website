package server

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"

	"github.com/coder9527-lg/vedio-website/internal/store"
)

// bookmarkCreateRequest 是创建书签的请求体：位置为秒，备注可省略。
type bookmarkCreateRequest struct {
	Position float64 `json:"position"`
	Note     string  `json:"note"`
}

// bookmarkUpdateRequest 是修改书签备注的请求体。
type bookmarkUpdateRequest struct {
	Note string `json:"note"`
}

// parseBookmarkID 解析路径中的书签主键，非法时写 400 并返回 ok=false。
func parseBookmarkID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("bid"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法的书签 ID"})
		return 0, false
	}
	return id, true
}

// handleBookmarkList 返回一个视频的全部书签；空列表回空数组，保证前端 forEach 安全。
func (s *Server) handleBookmarkList(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	list, err := s.store.ListBookmarks(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if list == nil {
		list = []store.Bookmark{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookmarks": list})
}

// handleBookmarkCreate 在当前播放位置打点创建书签，备注可留空。
func (s *Server) handleBookmarkCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	var req bookmarkCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	// NaN/Inf 正常会在 JSON 解码阶段报错，这里再兜一层防御
	if math.IsNaN(req.Position) || math.IsInf(req.Position, 0) || req.Position < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "书签位置不能为负数"})
		return
	}
	bm, err := s.store.CreateBookmark(id, req.Position, req.Note)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"bookmark": bm})
}

// handleBookmarkUpdate 修改书签备注，带视频归属校验：不命中返回 404。
func (s *Server) handleBookmarkUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	bid, ok := parseBookmarkID(w, r)
	if !ok {
		return
	}
	var req bookmarkUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	videoID := r.PathValue("id")
	updated, err := s.store.UpdateBookmarkNote(bid, videoID, req.Note)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !updated {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bookmark not found"})
		return
	}
	bm, err := s.store.GetBookmark(bid, videoID)
	if err != nil || bm == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "回读书签失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookmark": bm})
}

// handleBookmarkDelete 删除书签，幂等：记录不存在同样返回 200。
func (s *Server) handleBookmarkDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	bid, ok := parseBookmarkID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteBookmark(bid, r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
