package server

import (
	"log"
	"net/http"
)

// handleSubtitleList 返回指定视频的外挂字幕列表。
func (s *Server) handleSubtitleList(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subtitles": s.library.FindSubtitles(v),
	})
}

// handleSubtitleTrack 输出浏览器可用的 WebVTT 字幕：
// VTT 原文件直接服务，SRT/ASS/SSA 经 ffmpeg 按需转换后缓存。
func (s *Server) handleSubtitleTrack(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	sub, ok := s.library.GetSubtitle(v, r.PathValue("subID"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	path, err := s.subs.Ensure(sub)
	if err != nil {
		if !s.subs.Available() {
			log.Printf("ffmpeg 不可用，无法转换字幕 %s: %v", sub.RelPath, err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "字幕转换不可用：服务器未安装 ffmpeg，仅支持 VTT 字幕",
			})
			return
		}
		log.Printf("转换字幕失败 %s: %v", sub.RelPath, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "字幕转换失败"})
		return
	}

	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	http.ServeFile(w, r, path)
}
