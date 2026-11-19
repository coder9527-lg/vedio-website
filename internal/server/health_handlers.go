package server

import "net/http"

// handleHealthz 容器存活探针。
// 本服务的外部依赖（SQLite/ffmpeg/TMDB）均为可选且静默降级，
// 进程能响应 HTTP 即视为存活，故不把可选组件状态计入状态码，
// 避免依赖抖动触发容器编排系统的重启循环。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
