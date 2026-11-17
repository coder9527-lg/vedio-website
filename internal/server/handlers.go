package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// ---------- 页面 ----------

// basePage 存放所有页面共用的模板字段。
type basePage struct {
	Title       string
	AuthEnabled bool
}

type indexData struct {
	basePage
	Videos []*media.Video
	Count  int
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	videos := s.library.List()
	s.render(w, "index.html", indexData{
		basePage: basePage{Title: "我的视频库", AuthEnabled: s.authEnabled()},
		Videos:   videos,
		Count:    len(videos),
	})
}

func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "watch.html", struct {
		basePage
		Video *media.Video
	}{
		basePage: basePage{Title: v.Name, AuthEnabled: s.authEnabled()},
		Video:    v,
	})
}

// ---------- JSON API ----------

func (s *Server) handleAPIList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"count":  len(s.library.List()),
		"videos": s.library.List(),
	})
}

func (s *Server) handleAPIGet(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleRescan(w http.ResponseWriter, r *http.Request) {
	n, err := s.library.Scan()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("重新扫描完成，索引视频 %d 个", n)
	writeJSON(w, http.StatusOK, map[string]any{"indexed": n})
}

// ---------- 视频流（支持 Range / 拖动进度条）----------

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	file, err := os.Open(v.AbsPath())
	if err != nil {
		http.Error(w, "无法打开视频文件", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		http.Error(w, "无法读取文件信息", http.StatusInternalServerError)
		return
	}

	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename*=UTF-8''%s", urlEncode(v.Name+v.Ext)))
	}
	w.Header().Set("Content-Type", mimeByExt(v.Ext))
	w.Header().Set("Accept-Ranges", "bytes")

	// http.ServeContent 原生处理 Range 请求、If-Modified-Since 与 HEAD
	http.ServeContent(w, r, v.Name+v.Ext, stat.ModTime(), file)
}

// ---------- 缩略图 ----------

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	v, ok := s.library.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	path, err := s.thumbs.Ensure(v)
	if err != nil {
		if !s.thumbs.Available() {
			log.Printf("ffmpeg 不可用，返回占位图: %v", err)
		} else {
			log.Printf("生成缩略图失败 %s: %v", v.RelPath, err)
		}
		s.handlePlaceholder(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	http.ServeFile(w, r, path)
}

func (s *Server) handlePlaceholder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(placeholderSVG))
}

// ---------- 中间件与工具 ----------

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.RequestURI(), rec.status, time.Since(start).Round(time.Microsecond))
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v", rec)
				http.Error(w, "服务器内部错误", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("渲染模板 %s 失败: %v", name, err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		log.Printf("写 JSON 响应失败: %v", err)
	}
}

func mimeByExt(ext string) string {
	switch ext {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".ts":
		return "video/mp2t"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".wmv":
		return "video/x-ms-wmv"
	case ".flv":
		return "video/x-flv"
	case ".mpeg", ".mpg":
		return "video/mpeg"
	case ".3gp":
		return "video/3gpp"
	default:
		return "application/octet-stream"
	}
}

func urlEncode(s string) string {
	// RFC 5987 filename* 编码：保留 ASCII 安全字符，其余按 UTF-8 百分号编码
	var b []byte
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b = append(b, byte(r))
		case r == '-' || r == '_' || r == '.' || r == '~':
			b = append(b, byte(r))
		default:
			for _, c := range []byte(string(r)) {
				b = append(b, '%', hexUpper[c>>4], hexUpper[c&0x0f])
			}
		}
	}
	return string(b)
}

const hexUpper = "0123456789ABCDEF"

const placeholderSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="480" height="270" viewBox="0 0 480 270">
  <rect width="480" height="270" fill="#1f2937"/>
  <polygon points="205,100 205,170 275,135" fill="#6b7280"/>
  <text x="240" y="210" font-family="sans-serif" font-size="18" fill="#9ca3af" text-anchor="middle">暂无缩略图</text>
</svg>`
