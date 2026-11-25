package server

import (
	"net/http"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
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

	// 筛选状态（与首页六元组同口径，搜索页只取 sort/type/genre/tag 四元）
	Sort   string
	Type   string // all | movie | show（show 不过滤折叠，仅排除非剧集成员）
	Genre  string
	Tag    string
	Genres []string
	Tags   []store.Tag
	Params map[string]string // 当前四元组快照（含 q），供 searchURL 保持筛选正交
}

// handleSearch 渲染独立搜索页。无关键字时只显示引导空态，不查库、不展示筛选条。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	keyword := strings.TrimSpace(q.Get("q"))
	sortVal := q.Get("sort")
	if !allowedSorts[sortVal] {
		sortVal = ""
	}
	typeFilter := normalizeTypeFilter(q.Get("type"))
	genre := strings.TrimSpace(q.Get("genre"))
	tag := strings.TrimSpace(q.Get("tag"))

	data := searchData{
		basePage: basePage{Title: "搜索", AuthEnabled: s.authEnabled()},
		Query:    keyword,
		Metadata: map[string]*media.Metadata{},
		State:    s.loadVideoState(),
		StoreOn:  s.storeEnabled(),
		Sort:     sortVal,
		Type:     typeFilter,
		Genre:    genre,
		Tag:      tag,
		Params: map[string]string{
			"q":     keyword,
			"sort":  sortVal,
			"type":  typeFilter,
			"genre": genre,
			"tag":   tag,
		},
	}

	if keyword != "" {
		query := media.Query{Keyword: keyword, Sort: sortVal, Genre: genre}

		// 标签筛选：与首页同口径，TagOn 时标签名不存在也强制零结果；仅持久化开启时生效
		if s.storeEnabled() && tag != "" {
			query.TagOn = true
			query.TagIDs = map[string]bool{}
			if tg, err := s.store.TagByName(tag); err == nil && tg != nil {
				if set, err := s.store.TagVideoSet(tg.ID); err == nil {
					query.TagIDs = set
				}
			}
		}

		videos := s.library.Query(query)
		// 类型过滤在查询结果上做内存收敛：电影排除剧集成员，剧集只保留剧集成员（搜索页不折叠剧集）
		if typeFilter != "all" {
			filtered := videos[:0:0]
			for _, v := range videos {
				_, isEp := s.library.ShowIDOf(v.ID)
				if typeFilter == "movie" && !isEp {
					filtered = append(filtered, v)
				} else if typeFilter == "show" && isEp {
					filtered = append(filtered, v)
				}
			}
			videos = filtered
		}

		data.Videos = videos
		data.Count = len(videos)
		for _, v := range videos {
			if md := s.library.Metadata(v.ID); md != nil {
				data.Metadata[v.ID] = md
			}
		}
		data.Genres = s.library.AllGenres()
		if s.storeEnabled() {
			data.Tags, _ = s.store.ListTags()
		}
	}

	s.render(w, "search.html", data)
}
