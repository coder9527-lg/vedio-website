package server

import (
	"net/http"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// collectionItem 是合集详情页的单个成员行：聚合视频、元信息与播放进度。
type collectionItem struct {
	Index    int // 0 起的展示位置（悬空条目已剔除后重新编号）
	Last     bool
	Video    *media.Video
	Metadata *media.Metadata
	Progress store.Progress
	HasProg  bool
	Poster   string // 海报地址（无海报为空串，模板回退占位图）
}

// collectionPageData 是合集详情页（/playlists/{id}）的渲染数据。
// 合集复用播放列表存储，仅新增独立 SSR 详情页与片库入口，不引入第二套实体。
type collectionPageData struct {
	basePage
	StoreOn bool
	PL      store.Playlist
	Items   []collectionItem
}

// handlePlaylistPage 渲染合集（播放列表）独立详情页。
// 未启用持久化给出提示；列表不存在返回 404；悬空成员在重扫前以缺失标记跳过。
func (s *Server) handlePlaylistPage(w http.ResponseWriter, r *http.Request) {
	if !s.storeEnabled() {
		s.render(w, "collection.html", collectionPageData{
			basePage: basePage{Title: "合集详情", AuthEnabled: s.authEnabled()},
			StoreOn:  false,
		})
		return
	}

	pl, err := s.store.GetPlaylist(r.PathValue("id"))
	if err != nil {
		http.Error(w, "读取合集失败", http.StatusInternalServerError)
		return
	}
	if pl == nil {
		http.NotFound(w, r)
		return
	}

	progress, _ := s.store.ProgressMap()
	items := make([]collectionItem, 0, len(pl.Items))
	for _, it := range pl.Items {
		v, ok := s.library.Get(it.VideoID)
		if !ok {
			// 视频文件已移除但重扫尚未清理：跳过，避免死链
			continue
		}
		md := s.library.Metadata(v.ID)
		poster := ""
		if md != nil && md.PosterPath != "" {
			// 与首页卡片同口径：有刮削海报走 artwork 路由，否则模板回退 /thumb 缩略图
			poster = "/artwork/" + v.ID + "/poster"
		}
		ci := collectionItem{
			Index:    len(items),
			Video:    v,
			Metadata: md, // 无元信息时为 nil，模板自行降级
			Progress: progress[it.VideoID],
			Poster:   poster,
		}
		ci.HasProg = hasProgressValue(ci.Progress.Position, ci.Progress.Duration)
		items = append(items, ci)
	}
	if len(items) > 0 {
		items[len(items)-1].Last = true
	}

	s.render(w, "collection.html", collectionPageData{
		basePage: basePage{Title: pl.Name + " · 合集", AuthEnabled: s.authEnabled()},
		StoreOn:  true,
		PL:       *pl,
		Items:    items,
	})
}
