package server

import (
	"net/http"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// homeCard 是首页统一卡片：剧集卡片聚合多集，电影卡片直接承载单个视频。
type homeCard struct {
	IsShow bool
	Show   *media.Show
	Video  *media.Video
}

// handleAPIListShows 返回全部剧集。
func (s *Server) handleAPIListShows(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"count": len(s.library.ListShows()),
		"shows": s.library.ListShows(),
	})
}

// handleAPIGetShow 返回单部剧集详情；不存在返回 404。
func (s *Server) handleAPIGetShow(w http.ResponseWriter, r *http.Request) {
	show, ok := s.library.GetShow(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "show not found"})
		return
	}
	writeJSON(w, http.StatusOK, show)
}

// handleShowPage 渲染剧集详情页（按季列集）。
func (s *Server) handleShowPage(w http.ResponseWriter, r *http.Request) {
	show, ok := s.library.GetShow(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// 剧集各成员的元信息，供海报兜底与集标题展示
	metaMap := map[string]*media.Metadata{}
	for vid := range s.library.ShowVideos(show.ID) {
		if md := s.library.Metadata(vid); md != nil {
			metaMap[vid] = md
		}
	}
	s.render(w, "show.html", struct {
		basePage
		Show     *media.Show
		Metadata map[string]*media.Metadata
		State    videoState
		StoreOn  bool
	}{
		basePage: basePage{Title: show.Title, AuthEnabled: s.authEnabled()},
		Show:     show,
		Metadata: metaMap,
		State:    s.loadVideoState(),
		StoreOn:  s.storeEnabled(),
	})
}

// buildHomeCards 按首页查询结果组装卡片：
// 仅 type=all 时把剧集成员折叠为剧集卡片；movie/show 显式筛选时返回对应平铺结果。
func (s *Server) buildHomeCards(videos []*media.Video, typeFilter string) []homeCard {
	cards := make([]homeCard, 0, len(videos))
	emittedShows := map[string]bool{}

	for _, v := range videos {
		showID, isEp := s.library.ShowIDOf(v.ID)
		switch typeFilter {
		case "movie":
			// 电影视图：跳过剧集成员
			if isEp {
				continue
			}
			cards = append(cards, homeCard{IsShow: false, Video: v})
		case "show":
			// 剧集视图：每个剧只出一张卡片
			if !isEp || emittedShows[showID] {
				continue
			}
			show, ok := s.library.GetShow(showID)
			if !ok {
				continue
			}
			emittedShows[showID] = true
			cards = append(cards, homeCard{IsShow: true, Show: show})
		default:
			// 默认全部：剧集折叠，电影平铺
			if isEp {
				if emittedShows[showID] {
					continue
				}
				show, ok := s.library.GetShow(showID)
				if !ok {
					continue
				}
				emittedShows[showID] = true
				cards = append(cards, homeCard{IsShow: true, Show: show})
			} else {
				cards = append(cards, homeCard{IsShow: false, Video: v})
			}
		}
	}
	return cards
}

// normalizeTypeFilter 把 type 参数收敛到白名单 all|movie|show。
func normalizeTypeFilter(v string) string {
	switch strings.TrimSpace(v) {
	case "movie", "show":
		return v
	default:
		return "all"
	}
}
