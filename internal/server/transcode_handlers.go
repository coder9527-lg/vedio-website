package server

import (
	"errors"
	"net/http"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// transcodeEnabled 供模板与处理器判断是否展示转码播放入口。
func (s *Server) transcodeEnabled() bool { return s.transcoder != nil && s.transcoder.Available() }

// handleTranscodeStatus 启动（或复用）转码会话并返回状态，前端轮询直到产出 m3u8。
func (s *Server) handleTranscodeStatus(w http.ResponseWriter, r *http.Request) {
	if !s.transcodeEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "转码不可用：未配置 ffmpeg 或 TRANSCODE_ENABLED=false",
		})
		return
	}
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}

	sess, err := s.transcoder.Ensure(v)
	if err != nil {
		switch {
		case errors.Is(err, media.ErrTranscoderBusy):
			// 槽位满：让前端继续轮询而不是报错
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"state": "busy",
				"error": err.Error(),
			})
		case errors.Is(err, media.ErrSessionFailed):
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"state": "failed",
				"error": err.Error(),
			})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	defer sess.Touch()
	st := sess.Status()
	writeJSON(w, http.StatusOK, st)
}

// handleHLSPlaylist 返回动态 m3u8 播放列表。
// 列表会随 ffmpeg 转码进度不断追加，因此禁用缓存，由播放器定时重新拉取。
func (s *Server) handleHLSPlaylist(w http.ResponseWriter, r *http.Request) {
	sess := s.transcoder.Get(r.PathValue("id"))
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	sess.Touch()
	path, err := sess.PlaylistPath()
	if err != nil {
		// 首个分片尚未产出：404，播放器按重试间隔再来
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, path)
}

// handleHLSSegment 返回单个 mpegts 分片；仍在写入或不存在时返回 404 由播放器重试。
func (s *Server) handleHLSSegment(w http.ResponseWriter, r *http.Request) {
	sess := s.transcoder.Get(r.PathValue("id"))
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	sess.Touch()
	path, err := sess.SegmentPath(r.PathValue("seg"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	// 分片一旦写完内容不变，可长期缓存
	w.Header().Set("Cache-Control", "private, max-age=31536000")
	http.ServeFile(w, r, path)
}
