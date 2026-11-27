package server

import (
	"log"
	"net/http"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// watchLaterCard 是稍后再看页面的单张卡片：视频数据 + 加入时间 + 进度（用于续播与已看判断）。
type watchLaterCard struct {
	Video    *media.Video
	Metadata *media.Metadata
	Progress store.Progress
	AddedAt  string // 本地化展示由模板 timefmt 处理，这里透传原始时间
}

// watchLaterPageData 是稍后再看独立页面的模板数据。
type watchLaterPageData struct {
	basePage
	StoreOn bool
	Cards   []watchLaterCard
}

// handleWatchLaterAdd 加入稍后再看（幂等，重复加入不报错）。
func (s *Server) handleWatchLaterAdd(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.store.AddWatchLater(id); err != nil {
		http.Error(w, "加入稍后再看失败", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"watchLater": true})
}

// handleWatchLaterRemove 移出稍后再看（幂等）。
func (s *Server) handleWatchLaterRemove(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.store.RemoveWatchLater(id); err != nil {
		http.Error(w, "移出稍后再看失败", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"watchLater": false})
}

// handleWatchLaterGet 查询单个视频的稍后再看状态，供播放页按钮初始化。
func (s *Server) handleWatchLaterGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	ok, err := s.store.IsWatchLater(id)
	if err != nil {
		http.Error(w, "读取稍后再看状态失败", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"watchLater": ok})
}

// handleWatchLaterList 返回全部稍后再看视频 ID（JSON），供前端批量标记入口状态。
func (s *Server) handleWatchLaterList(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	set, err := s.store.WatchLaterSet()
	if err != nil {
		http.Error(w, "读取稍后再看清单失败", http.StatusInternalServerError)
		return
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"videoIds": ids, "count": len(ids)})
}

// handleWatchLaterPage 渲染稍后再看独立页面：按加入时间倒序，跳过已删视频的悬空记录，
// 并对已看完（进度 ≥ 95%）的条目做自动归档（从清单移除、不展示）。
func (s *Server) handleWatchLaterPage(w http.ResponseWriter, r *http.Request) {
	if !s.storeEnabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	items, err := s.store.ListWatchLater()
	if err != nil {
		http.Error(w, "读取稍后再看清单失败", http.StatusInternalServerError)
		return
	}
	progressMap, _ := s.store.ProgressMap()

	cards := make([]watchLaterCard, 0, len(items))
	var archived []string
	for _, it := range items {
		v, ok := s.library.Get(it.VideoID)
		if !ok {
			// 视频已从媒体库删除：清单记录成为悬空数据，归档清理
			archived = append(archived, it.VideoID)
			continue
		}
		p := progressMap[it.VideoID]
		if p.Completed {
			// 已看完（进度写入时按 ≥95% 标记）自动归档，不再占用待办清单
			archived = append(archived, it.VideoID)
			continue
		}
		cards = append(cards, watchLaterCard{
			Video:    v,
			Metadata: s.library.Metadata(it.VideoID),
			Progress: p,
			AddedAt:  it.AddedAt.Format("2006-01-02 15:04"),
		})
	}
	if len(archived) > 0 {
		if err := s.store.RemoveWatchLaterByIDs(archived); err != nil {
			log.Printf("稍后再看自动归档失败: %v", err)
		}
	}
	s.render(w, "watch_later.html", watchLaterPageData{
		basePage: basePage{Title: "稍后再看", AuthEnabled: s.authEnabled()},
		StoreOn:  true,
		Cards:    cards,
	})
}
