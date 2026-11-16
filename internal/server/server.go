package server

import (
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

//go:embed templates static
var assets embed.FS

// Server 持有路由与全部依赖。
type Server struct {
	library *media.Library
	thumbs  *media.Thumbnailer
	tmpl    *template.Template
}

func New(library *media.Library, thumbs *media.Thumbnailer) (*Server, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{library: library, thumbs: thumbs, tmpl: tmpl}, nil
}

// Handler 构建路由。要求 Go 1.22+ 的增强 ServeMux（方法与路径变量）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /watch/{id}", s.handleWatch)
	mux.HandleFunc("GET /api/v1/videos", s.handleAPIList)
	mux.HandleFunc("GET /api/v1/videos/{id}", s.handleAPIGet)
	mux.HandleFunc("POST /api/v1/rescan", s.handleRescan)
	mux.HandleFunc("GET /stream/{id}", s.handleStream)
	mux.HandleFunc("GET /thumb/{id}", s.handleThumb)
	mux.HandleFunc("GET /thumb-placeholder.svg", s.handlePlaceholder)

	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		log.Fatalf("加载静态资源失败: %v", err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	return s.recoverPanic(s.logRequest(mux))
}
