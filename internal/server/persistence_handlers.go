package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/store"
)

// requireStore 在未启用持久化时统一返回 503，处理器只需在开头调用。
func (s *Server) requireStore(w http.ResponseWriter) bool {
	if !s.storeEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "persistence disabled: 未配置 DB_PATH，播放进度与收藏不可用",
		})
		return false
	}
	return true
}

// progressRequest 是前端上报播放进度的请求体。
// WatchDelta/SessionStart 为可选字段，旧客户端省略时为零值，向后兼容。
type progressRequest struct {
	Position     float64 `json:"position"`
	Duration     float64 `json:"duration"`
	WatchDelta   float64 `json:"watchDelta"`   // 本次上报新增的观看秒数
	SessionStart bool    `json:"sessionStart"` // 是否为本次播放会话首次上报
}

// handleProgressPut 上报/更新某个视频的播放进度。
func (s *Server) handleProgressPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}

	// 限制请求体大小，避免异常大 body 占用内存
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取请求体失败"})
		return
	}
	var req progressRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
			return
		}
	}
	if req.Position < 0 || req.Duration < 0 || req.WatchDelta < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "position/duration/watchDelta 不能为负数"})
		return
	}
	// 看完判定与前端、hasProgress 同口径：duration 已知且播放位置 >=95%；duration=0 永不自动完成
	completed := req.Duration > 0 && req.Position/req.Duration >= 0.95
	if err := s.store.SaveProgress(id, store.ProgressUpdate{
		Position:     req.Position,
		Duration:     req.Duration,
		WatchDelta:   req.WatchDelta,
		SessionStart: req.SessionStart,
		Completed:    completed,
	}); err != nil {
		log.Printf("保存播放进度失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存进度失败"})
		return
	}
	s.recordWatchEvent(id, req)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// recordWatchEvent 把本次上报累加到观看历史。进度是核心数据，历史是附属，
// 因此任何失败只记日志、不影响上报响应。
func (s *Server) recordWatchEvent(videoID string, req progressRequest) {
	now := time.Now().UTC()
	s.watchSessionMu.Lock()
	eventID, ok := s.watchSession[videoID]
	s.watchSessionMu.Unlock()

	// 会话首次上报，或服务重启后内存索引丢失：补建一行事件
	if req.SessionStart || !ok {
		id, err := s.store.StartWatchEvent(videoID, now)
		if err != nil {
			log.Printf("创建观看历史事件失败: %v", err)
			return
		}
		s.watchSessionMu.Lock()
		s.watchSession[videoID] = id
		s.watchSessionMu.Unlock()
		eventID = id
	}

	if err := s.store.UpdateWatchEvent(eventID, req.WatchDelta, req.Position, req.Duration, now); err != nil {
		log.Printf("更新观看历史事件失败: %v", err)
	}
}

// handleProgressGet 查询单个视频的播放进度。
func (s *Server) handleProgressGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	p, err := s.store.GetProgress(id)
	if err != nil {
		log.Printf("查询播放进度失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询进度失败"})
		return
	}
	if p == nil {
		writeJSON(w, http.StatusOK, map[string]any{"progress": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"progress": p})
}

// handleProgressDelete 清除单个视频的播放进度。
func (s *Server) handleProgressDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	if err := s.store.DeleteProgress(r.PathValue("id")); err != nil {
		log.Printf("删除播放进度失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "删除进度失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleProgressList 返回全部播放进度（列表页一次取全，前端按视频 ID 关联展示）。
func (s *Server) handleProgressList(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	m, err := s.store.ProgressMap()
	if err != nil {
		log.Printf("查询进度列表失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询进度失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"progress": m})
}

// favoriteRequest 收藏切换请求体；省略 favorite 字段时按切换(toggle)处理。
type favoriteRequest struct {
	Favorite *bool `json:"favorite"`
}

// handleFavoriteSet 收藏/取消收藏/切换收藏。
func (s *Server) handleFavoriteSet(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取请求体失败"})
		return
	}
	fav := true
	if len(body) > 0 {
		var req favoriteRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
			return
		}
		if req.Favorite != nil {
			fav = *req.Favorite
		} else {
			// 无 favorite 字段：按当前状态切换
			current, err := s.store.IsFavorite(id)
			if err != nil {
				log.Printf("查询收藏状态失败: %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "操作收藏失败"})
				return
			}
			fav = !current
		}
	}

	if err := s.store.SetFavorite(id, fav); err != nil {
		log.Printf("写入收藏失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "操作收藏失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"favorite": fav})
}

// handleFavoriteGet 查询单个视频收藏状态。
func (s *Server) handleFavoriteGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	fav, err := s.store.IsFavorite(r.PathValue("id"))
	if err != nil {
		log.Printf("查询收藏状态失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询收藏失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"favorite": fav})
}

// handleFavoriteList 返回全部已收藏视频 ID。
func (s *Server) handleFavoriteList(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	favs, err := s.store.ListFavorites()
	if err != nil {
		log.Printf("查询收藏列表失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询收藏失败"})
		return
	}
	ids := make([]string, 0, len(favs))
	for _, f := range favs {
		ids = append(ids, f.VideoID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"favorites": ids})
}

// videoState 聚合列表页需要的持久化状态（供模板渲染）。
type videoState struct {
	Progress map[string]store.Progress
	Favs     map[string]bool
	Members  map[string][]string    // 视频 ID → 所属播放列表 ID 列表
	Tags     map[string][]store.Tag // 视频 ID → 标签列表
}

// loadVideoState 一次性读取进度、收藏、播放列表成员关系与标签；未启用持久化时返回零值。
func (s *Server) loadVideoState() videoState {
	st := videoState{
		Progress: map[string]store.Progress{},
		Favs:     map[string]bool{},
		Members:  map[string][]string{},
		Tags:     map[string][]store.Tag{},
	}
	if !s.storeEnabled() {
		return st
	}
	if m, err := s.store.ProgressMap(); err == nil {
		st.Progress = m
	} else {
		log.Printf("读取播放进度失败: %v", err)
	}
	if m, err := s.store.FavoriteSet(); err == nil {
		st.Favs = m
	} else {
		log.Printf("读取收藏失败: %v", err)
	}
	if m, err := s.store.VideoPlaylistMemberships(); err == nil {
		st.Members = m
	} else {
		log.Printf("读取播放列表成员关系失败: %v", err)
	}
	if m, err := s.store.VideoTagsMap(); err == nil {
		st.Tags = m
	} else {
		log.Printf("读取视频标签失败: %v", err)
	}
	return st
}
