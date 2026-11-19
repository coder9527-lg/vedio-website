package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// ---------- 请求体 ----------

type createPlaylistRequest struct {
	Name string `json:"name"`
}

type renamePlaylistRequest struct {
	Name string `json:"name"`
}

type addPlaylistItemRequest struct {
	VideoID string `json:"videoId"`
}

type moveItemRequest struct {
	Direction string `json:"direction"` // up | down
}

type showAddPlaylistRequest struct {
	PlaylistID string `json:"playlistId"`
}

// decodePlaylistBody 解析受限大小的 JSON 请求体；空 body 或坏 JSON 返回错误。
func decodePlaylistBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

// playlistNotFound 把 store.ErrNotFound 统一映射为 404 JSON。
func playlistNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "playlist not found"})
}

// ---------- 列表头 ----------

// handlePlaylistList 返回全部播放列表摘要。
func (s *Server) handlePlaylistList(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	list, err := s.store.ListPlaylists()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if list == nil {
		list = []store.PlaylistSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"playlists": list})
}

// handlePlaylistCreate 新建播放列表。
func (s *Server) handlePlaylistCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	var req createPlaylistRequest
	if err := decodePlaylistBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法 JSON 请求体"})
		return
	}
	pl, err := s.store.CreatePlaylist(req.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"playlist": pl})
}

// handlePlaylistGet 返回列表详情，条目经 library 解析并跳过悬空 video_id。
func (s *Server) handlePlaylistGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	pl, err := s.store.GetPlaylist(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if pl == nil {
		playlistNotFound(w)
		return
	}
	type detailItem struct {
		VideoID   string       `json:"videoId"`
		SortOrder int          `json:"sortOrder"`
		AddedAt   time.Time    `json:"addedAt"`
		Video     *media.Video `json:"video"`
	}
	items := make([]detailItem, 0, len(pl.Items))
	for _, it := range pl.Items {
		v, ok := s.library.Get(it.VideoID)
		if !ok {
			continue // 容忍 rescan 前的悬空条目
		}
		items = append(items, detailItem{
			VideoID: it.VideoID, SortOrder: it.SortOrder, AddedAt: it.AddedAt, Video: v,
		})
	}
	// 悬空条目可能造成 sort_order 不连续，响应中重新编号展示位置
	for i := range items {
		items[i].SortOrder = i
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"playlist": map[string]any{
			"id":        pl.ID,
			"name":      pl.Name,
			"createdAt": pl.CreatedAt,
			"updatedAt": pl.UpdatedAt,
		},
		"items": items,
	})
}

// handlePlaylistRename 重命名播放列表。
func (s *Server) handlePlaylistRename(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	var req renamePlaylistRequest
	if err := decodePlaylistBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法 JSON 请求体"})
		return
	}
	err := s.store.RenamePlaylist(r.PathValue("id"), req.Name)
	if errors.Is(err, store.ErrNotFound) {
		playlistNotFound(w)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePlaylistDelete 删除播放列表及其条目。
func (s *Server) handlePlaylistDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	err := s.store.DeletePlaylist(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		playlistNotFound(w)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------- 条目 ----------

// handlePlaylistAddItem 向列表追加单个视频（重复加入幂等）。
func (s *Server) handlePlaylistAddItem(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	var req addPlaylistItemRequest
	if err := decodePlaylistBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法 JSON 请求体"})
		return
	}
	if _, ok := s.library.Get(req.VideoID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	added, err := s.store.AddPlaylistItem(r.PathValue("id"), req.VideoID)
	if errors.Is(err, store.ErrNotFound) {
		playlistNotFound(w)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// 新增返回 201，重复加入幂等返回 200
	status := http.StatusOK
	if added {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]bool{"added": added})
}

// handlePlaylistRemoveItem 从列表移除条目（幂等）。
func (s *Server) handlePlaylistRemoveItem(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	err := s.store.RemovePlaylistItem(r.PathValue("id"), r.PathValue("videoId"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePlaylistMoveItem 上移/下移条目，返回移动后的完整顺序。
func (s *Server) handlePlaylistMoveItem(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	var req moveItemRequest
	if err := decodePlaylistBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法 JSON 请求体"})
		return
	}
	if req.Direction != "up" && req.Direction != "down" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `direction 仅接受 "up" 或 "down"`})
		return
	}
	playlistID := r.PathValue("id")
	videoID := r.PathValue("videoId")

	pl, err := s.store.GetPlaylist(playlistID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if pl == nil {
		playlistNotFound(w)
		return
	}
	from := -1
	for i, it := range pl.Items {
		if it.VideoID == videoID {
			from = i
			break
		}
	}
	if from < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "item not found"})
		return
	}
	to := from
	if req.Direction == "up" && from > 0 {
		to = from - 1
	} else if req.Direction == "down" && from < len(pl.Items)-1 {
		to = from + 1
	}
	// 边界移动（首项上移/末项下移）为 no-op，不报错
	if to != from {
		if err := s.store.MovePlaylistItem(playlistID, videoID, to); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	// 重新读取并返回移动后的全序（悬空项同样过滤）
	updated, err := s.store.GetPlaylist(playlistID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	type orderItem struct {
		VideoID   string `json:"videoId"`
		SortOrder int    `json:"sortOrder"`
	}
	out := make([]orderItem, 0, len(updated.Items))
	order := 0
	for _, it := range updated.Items {
		if _, ok := s.library.Get(it.VideoID); !ok {
			continue
		}
		out = append(out, orderItem{VideoID: it.VideoID, SortOrder: order})
		order++
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// ---------- 整剧加入 / 成员查询 ----------

// handleShowAddToPlaylist 把整部剧按季集顺序一键加入指定列表。
func (s *Server) handleShowAddToPlaylist(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	var req showAddPlaylistRequest
	if err := decodePlaylistBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法 JSON 请求体"})
		return
	}
	show, ok := s.library.GetShow(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "show not found"})
		return
	}
	// 校验列表存在（不存在时 AddPlaylistItems 也会报 ErrNotFound，这里提前给出一致语义）
	pl, err := s.store.GetPlaylist(req.PlaylistID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if pl == nil {
		playlistNotFound(w)
		return
	}
	// 按 Seasons → Episodes 的既有顺序收集视频 ID（buildShowLocked 已保证季/集升序）
	var videoIDs []string
	for _, season := range show.Seasons {
		for _, ep := range season.Episodes {
			if ep.Video != nil {
				videoIDs = append(videoIDs, ep.Video.ID)
			}
		}
	}
	added, err := s.store.AddPlaylistItems(req.PlaylistID, videoIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// 至少新增一条返回 201；全部已存在的幂等请求返回 200
	status := http.StatusOK
	if added > 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{
		"added":   added,
		"skipped": len(videoIDs) - added,
		"total":   len(videoIDs),
	})
}

// handleVideoPlaylists 返回该视频所在的播放列表（选择弹窗勾选态）。
func (s *Server) handleVideoPlaylists(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	videoID := r.PathValue("id")
	if _, ok := s.library.Get(videoID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	memberships, err := s.store.VideoPlaylistMemberships()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	type ref struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := []ref{}
	for _, pid := range memberships[videoID] {
		pl, err := s.store.GetPlaylist(pid)
		if err != nil || pl == nil {
			continue
		}
		out = append(out, ref{ID: pid, Name: pl.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"playlists": out})
}
