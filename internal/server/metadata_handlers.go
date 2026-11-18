package server

import (
	"net/http"
	"path/filepath"
	"strings"
)

// artworkExts 允许通过 HTTP 提供的海报/背景图扩展名（与 media 层发现规则保持一致）。
var artworkExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
}

// handleAPIMetadata 返回单个视频的刮削元信息；无任何元信息时返回 404。
func (s *Server) handleAPIMetadata(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	md := s.library.Metadata(v.ID)
	if md == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "metadata not found"})
		return
	}
	writeJSON(w, http.StatusOK, md)
}

// handleArtwork 安全地提供视频同目录下的本地海报/背景图。
// kind 取值 poster / backdrop，由路由限定；只返回元信息中已登记的图片。
func (s *Server) handleArtwork(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok := s.library.Get(r.PathValue("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		md := s.library.Metadata(v.ID)
		if md == nil {
			http.NotFound(w, r)
			return
		}
		var target string
		switch kind {
		case "poster":
			target = md.PosterPath
		case "backdrop":
			target = md.BackdropPath
		default:
			http.NotFound(w, r)
			return
		}
		if target == "" || !artworkExts[strings.ToLower(filepath.Ext(target))] {
			http.NotFound(w, r)
			return
		}
		// 纵深防御：确认图片位于媒体根目录内，杜绝任何路径穿越可能
		root, err := filepath.Abs(s.library.Root())
		if err != nil {
			http.Error(w, "服务器内部错误", http.StatusInternalServerError)
			return
		}
		abs, err := filepath.Abs(target)
		if err != nil || abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000")
		http.ServeFile(w, r, abs)
	}
}
