package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/coder9527-lg/vedio-website/internal/store"
)

// skipRequest 是保存片头片尾跳过设置的请求体。
// Enabled 用指针接收：省略该字段时按启用处理，避免与显式 false 的零值歧义。
type skipRequest struct {
	IntroSeconds float64 `json:"introSeconds"` // 片头长度（从 0 起算的秒数）
	OutroSeconds float64 `json:"outroSeconds"` // 片尾长度（距视频结尾的秒数）
	Enabled      *bool   `json:"enabled"`
	Duration     float64 `json:"duration"` // 可选：播放器已知的视频总时长，仅用于服务端几何校验
}

// handleSkipPut 保存（UPSERT）一个视频的片头片尾跳过设置。
func (s *Server) handleSkipPut(w http.ResponseWriter, r *http.Request) {
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
	var req skipRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
			return
		}
	}
	if req.IntroSeconds < 0 || req.OutroSeconds < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "片头/片尾秒数不能为负数"})
		return
	}
	// 仅在播放器已知总时长时做几何校验；duration=0（元数据未加载）一律放行，由前端运行时防御
	if req.Duration > 0 {
		if req.IntroSeconds >= req.Duration {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "片头时长不能大于等于视频总时长"})
			return
		}
		if req.IntroSeconds+req.OutroSeconds > req.Duration+0.001 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "片头与片尾区间重叠或超过总时长"})
			return
		}
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := s.store.UpsertSkipSetting(id, store.SkipSetting{
		IntroSeconds: req.IntroSeconds,
		OutroSeconds: req.OutroSeconds,
		Enabled:      enabled,
	}); err != nil {
		log.Printf("保存跳过设置失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存跳过设置失败"})
		return
	}
	got, err := s.store.GetSkipSetting(id)
	if err != nil {
		log.Printf("回读跳过设置失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "读取跳过设置失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skip": got})
}

// handleSkipGet 查询单个视频的跳过设置，无记录时返回 {"skip": null}。
func (s *Server) handleSkipGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	got, err := s.store.GetSkipSetting(r.PathValue("id"))
	if err != nil {
		log.Printf("查询跳过设置失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询跳过设置失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skip": got})
}

// handleSkipDelete 清除单个视频的跳过设置，幂等。
func (s *Server) handleSkipDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	if err := s.store.DeleteSkipSetting(r.PathValue("id")); err != nil {
		log.Printf("删除跳过设置失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "删除跳过设置失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
