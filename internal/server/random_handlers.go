package server

import (
	"math/rand"
	"net/http"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// handleRandom 「随便看看」：从未看完的视频里随机挑一个并重定向到播放页。
//
// 选片优先级：
//  1. 启用持久化时，先在「未看完（无 progress 记录或 Completed=false）」集合中随机；
//  2. 若全部都已看完（未看完集合为空），回退到全库随机，避免无片可播；
//  3. 未启用持久化时没有进度概念，直接全库随机。
//
// 媒体库为空时返回 404，由前端/用户感知无内容。
func (s *Server) handleRandom(w http.ResponseWriter, r *http.Request) {
	all := s.library.List()
	if len(all) == 0 {
		http.NotFound(w, r)
		return
	}

	pool := s.unwatchedPool(all)
	if len(pool) == 0 {
		// 全部已看完：回退全库，保证「随便看看」始终有结果
		pool = all
	}

	pick := pool[rand.Intn(len(pool))]
	http.Redirect(w, r, "/watch/"+pick.ID, http.StatusSeeOther)
}

// unwatchedPool 过滤出未看完的视频；未启用持久化或读取失败时返回 nil（调用方回退全库）。
func (s *Server) unwatchedPool(all []*media.Video) []*media.Video {
	if !s.storeEnabled() {
		return nil
	}
	progress, err := s.store.ProgressMap()
	if err != nil {
		// 进度读取失败不应阻断随机播放，回退全库
		return nil
	}
	pool := make([]*media.Video, 0, len(all))
	for _, v := range all {
		if p, ok := progress[v.ID]; !ok || !p.Completed {
			pool = append(pool, v)
		}
	}
	return pool
}
