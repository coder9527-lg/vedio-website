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
		// 纵深防御：图片必须位于媒体根目录或 TMDB 缓存目录内，杜绝任何路径穿越可能
		abs, err := filepath.Abs(target)
		if err != nil || !s.allowedArtworkRoot(abs) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000")
		http.ServeFile(w, r, abs)
	}
}

// allowedArtworkRoot 判断绝对路径是否落在媒体根目录或刮削图片缓存目录之内。
func (s *Server) allowedArtworkRoot(abs string) bool {
	for _, root := range s.artworkRoots() {
		if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// artworkRoots 返回允许提供图片的根目录（均已转为绝对路径，跳过不可解析项）。
func (s *Server) artworkRoots() []string {
	candidates := []string{s.library.Root()}
	if s.scraper != nil {
		if dir := s.scraper.ImageDir(); dir != "" {
			candidates = append(candidates, dir)
		}
	}
	roots := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil {
			roots = append(roots, abs)
		}
	}
	return roots
}
