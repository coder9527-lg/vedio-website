package server

import (
	"net/http"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// searchData 是全局搜索页的视图模型：平铺展示命中的视频卡片，不做剧集折叠。
type searchData struct {
	basePage
	Query    string
	Videos   []*media.Video
	Metadata map[string]*media.Metadata
	State    videoState
	StoreOn  bool
	Count    int
}

// handleSearch 渲染独立搜索页。无关键字时只显示引导空态，不查库。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	data := searchData{
		basePage: basePage{Title: "搜索", AuthEnabled: s.authEnabled()},
		Query:    q,
		Metadata: map[string]*media.Metadata{},
		State:    s.loadVideoState(),
		StoreOn:  s.storeEnabled(),
	}

	if q != "" {
		videos := s.library.Query(media.Query{Keyword: q})
		data.Videos = videos
		data.Count = len(videos)
		for _, v := range videos {
			if md := s.library.Metadata(v.ID); md != nil {
				data.Metadata[v.ID] = md
			}
		}
	}

	s.render(w, "search.html", data)
}
