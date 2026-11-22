package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/media"
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

// metadataEditRequest 是手动编辑元信息的请求体，仅含 10 个核心文本字段。
// 切片字段为 nil 时 media 层保留原值，为空切片时表示用户主动清空。
type metadataEditRequest struct {
	Title     string   `json:"title"`
	Original  string   `json:"original"`
	Overview  string   `json:"overview"`
	Year      string   `json:"year"`
	Runtime   int      `json:"runtime"`
	Rating    float64  `json:"rating"`
	Genres    []string `json:"genres"`
	Directors []string `json:"directors"`
	Actors    []string `json:"actors"`
	Studio    string   `json:"studio"`
}

// metadataEditEnabled 判断手动编辑是否可用：只要登记了可写缓存目录即可，与在线刮削开关无关。
func (s *Server) metadataEditEnabled() bool { return s.library.ScraperImageDir() != "" }

// handleMetadataEdit 保存网页端手动编辑的元信息（PUT）。
// 视频不存在 → 404；缓存目录未配置 → 503；非法 JSON 或字段越界 → 400；成功返回合并后的完整元信息。
func (s *Server) handleMetadataEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	if !s.metadataEditEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "元信息编辑不可用：未配置可写缓存目录（TMDB_IMAGE_DIR）",
		})
		return
	}

	// 简介可能较长，限制请求体 1MB，防止异常大 body 打满内存
	var req metadataEditRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	if req.Runtime < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "片长不能为负数"})
		return
	}
	if req.Rating < 0 || req.Rating > 10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "评分必须在 0 到 10 之间"})
		return
	}

	md := &media.Metadata{
		Title:     strings.TrimSpace(req.Title),
		Original:  strings.TrimSpace(req.Original),
		Overview:  strings.TrimSpace(req.Overview),
		Year:      strings.TrimSpace(req.Year),
		Runtime:   req.Runtime,
		Rating:    req.Rating,
		Genres:    cleanManualList(req.Genres),
		Directors: cleanManualList(req.Directors),
		Actors:    cleanManualList(req.Actors),
		Studio:    strings.TrimSpace(req.Studio),
	}

	if err := s.library.SaveManualMetadata(id, md); err != nil {
		if errors.Is(err, media.ErrMetadataDirNotConfigured) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "元信息编辑不可用：未配置可写缓存目录（TMDB_IMAGE_DIR）",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// 返回当前生效的完整合并元信息（含图片路径与来源），与刮削 apply 的响应形状保持一致
	merged := s.library.Metadata(id)
	if merged == nil {
		// 编辑前无任何元信息时，合并结果即手动值本身
		merged = md
	}
	writeJSON(w, http.StatusOK, merged)
}

// cleanManualList 清洗表单提交的逗号分隔切片：逐项去空白、丢弃空串并去重；
// 入参为 nil（字段缺失）时返回 nil，使 media 层保留原值；入参为空切片时返回空切片，表示主动清空。
func cleanManualList(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, item := range in {
		part := strings.TrimSpace(item)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
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
