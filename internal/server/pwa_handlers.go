package server

import (
	"mime"
	"net/http"
)

func init() {
	// 部分系统 MIME 表未登记 .webmanifest，Go FileServer 会退化为 text/plain，
	// 导致浏览器拒绝 manifest；显式登记保证返回 application/manifest+json。
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// handleServiceWorker 在根路径提供 Service Worker 脚本。
// sw.js 物理位于 /static/ 下，而 Service Worker 的默认作用域受脚本路径限制
// （/static/sw.js 只能管控 /static/），因此在根路径挂载并显式返回
// Service-Worker-Allowed: / 头，使其能管控整站。
func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	body, err := assets.ReadFile("static/sw.js")
	if err != nil {
		http.Error(w, "service worker 资源缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Service-Worker-Allowed", "/")
	// SW 文件本身不缓存：保证版本更新（vw-cache-vN）能被浏览器及时取到
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}
