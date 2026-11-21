package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// transcodeEnabled 供模板与处理器判断是否展示转码播放入口。
func (s *Server) transcodeEnabled() bool { return s.transcoder != nil && s.transcoder.Available() }

// handleTranscodeStatus 启动（或复用）指定清晰度档位的转码会话并返回状态，前端轮询直到产出 m3u8。
// 档位由查询参数 q 指定（1080p/720p/480p），非法值由 media 层归一化为默认 720p。
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

	// auto（自适应 ABR）独立识别；其余档位非法值由 media 层归一化为 720p。
	quality := r.URL.Query().Get("q")
	if quality != media.QualityAuto {
		quality = media.NormalizeQuality(quality)
	}
	sess, err := s.transcoder.Ensure(v, quality)
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

// handleHLS 是 /hls/{id}/{rest...} 的统一入口，按路径形态分发：
//   - {quality}/index.m3u8          → 档位入口播放列表（简单 index / 多音轨 master 内容）
//   - {quality}/{asset}             → 档位产物（变体 m3u8 或 mpegts 分片，白名单校验）
//   - subs/{name}                   → 视频级共享内嵌字幕（sub-N.vtt，跨档位只提取一次）
//
// 路径形态非法（深度不对、档位未知、文件名越界）一律 404。
func (s *Server) handleHLS(w http.ResponseWriter, r *http.Request) {
	if s.transcoder == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	rest := strings.Trim(r.PathValue("rest"), "/")
	parts := strings.Split(rest, "/")

	switch {
	case len(parts) == 2 && parts[0] == "subs":
		// 共享内嵌字幕：与档位平级，文件名经 SubAssetPath 白名单校验
		path, err := s.transcoder.SubAssetPath(id, parts[1])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.Header().Set("Cache-Control", "private, max-age=31536000") // 提取产物内容不变
		http.ServeFile(w, r, path)

	case len(parts) == 2 && parts[1] == "index.m3u8":
		s.serveHLSPlaylist(w, r, id, parts[0])

	case len(parts) == 2 && parts[1] == "master.m3u8":
		// ABR 自适应会话（及多音轨变体布局）的 master 入口，同样走 PlaylistPath 解析。
		s.serveHLSPlaylist(w, r, id, parts[0])

	case len(parts) == 2:
		s.serveHLSAsset(w, r, id, parts[0], parts[1])

	default:
		http.NotFound(w, r)
	}
}

// serveHLSPlaylist 返回档位入口播放列表：简单布局为 index.m3u8，多音轨为 master.m3u8。
// 列表会随 ffmpeg 转码进度不断追加，因此禁用缓存，由播放器定时重新拉取。
func (s *Server) serveHLSPlaylist(w http.ResponseWriter, r *http.Request, id, quality string) {
	sess := s.transcoder.Get(id, quality)
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	sess.Touch()
	content, err := sess.PlaylistContent()
	if err != nil {
		// 首个分片尚未产出：404，播放器按重试间隔再来
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(content)
}

// serveHLSAsset 返回 HLS 产物中的媒体播放列表或 mpegts 分片（多音轨变体布局）。
// 文件名经会话白名单严格校验防路径穿越；仍在写入或不存在时返回 404 由播放器重试。
func (s *Server) serveHLSAsset(w http.ResponseWriter, r *http.Request, id, quality, name string) {
	sess := s.transcoder.Get(id, quality)
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	sess.Touch()
	path, err := sess.AssetPath(name)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if strings.HasSuffix(name, ".m3u8") {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store") // 媒体列表同样随转码进度追加
	} else {
		w.Header().Set("Content-Type", "video/mp2t")
		// 分片一旦写完内容不变，可长期缓存
		w.Header().Set("Cache-Control", "private, max-age=31536000")
	}
	http.ServeFile(w, r, path)
}
