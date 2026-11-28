package server

import "net/http"

// transcodeAdminPage 是转码任务管理页的模板数据。
// 转码未启用时 Enabled=false，页面只渲染提示，不发起轮询。
type transcodeAdminPage struct {
	basePage
	Enabled       bool
	MaxConcurrent int
	HWAccel       string
}

// handleTranscodeAdminPage 渲染转码任务管理页（会话列表由前端轮询 JSON 填充）。
func (s *Server) handleTranscodeAdminPage(w http.ResponseWriter, r *http.Request) {
	data := transcodeAdminPage{
		basePage: basePage{Title: "转码任务", AuthEnabled: s.authEnabled()},
		Enabled:  s.transcodeEnabled(),
	}
	if s.transcoder != nil {
		data.MaxConcurrent = s.transcoder.MaxConcurrent()
		data.HWAccel = s.transcoder.HWAccelMode()
	}
	s.render(w, "transcode_admin.html", data)
}

// handleTranscodeAdminList 返回全部转码会话与磁盘占用快照，供管理页轮询。
func (s *Server) handleTranscodeAdminList(w http.ResponseWriter, r *http.Request) {
	if !s.transcodeEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "转码不可用：未配置 ffmpeg 或 TRANSCODE_ENABLED=false",
		})
		return
	}
	writeJSON(w, http.StatusOK, s.transcoder.ManagementSnapshot())
}

// handleTranscodeAdminStop 终止并清理指定视频+档位的转码会话。
// 视频与档位取自查询参数 id 与 q；会话不存在返回 404。
func (s *Server) handleTranscodeAdminStop(w http.ResponseWriter, r *http.Request) {
	if !s.transcodeEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "转码不可用：未配置 ffmpeg 或 TRANSCODE_ENABLED=false",
		})
		return
	}
	videoID := r.PathValue("id")
	quality := r.URL.Query().Get("q")
	if _, ok := s.library.Get(videoID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	if !s.transcoder.StopSession(videoID, quality) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "转码会话不存在或已结束"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}
