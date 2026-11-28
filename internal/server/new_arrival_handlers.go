package server

import (
	"net/http"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// newArrivalWindow 是「新片」角标的判定窗口：入库时间在该窗口内即视为新片。
const newArrivalWindow = 30 * 24 * time.Hour

// timelineGroup 是时间线上的一个分组（如「最近 7 天」），组内按入库时间倒序。
type timelineGroup struct {
	Label string // 分组标题
	Cards []detailCard
}

// newArrivalPageData 是新片时间线页模板数据，纯媒体库数据、零额外存储。
type newArrivalPageData struct {
	basePage
	Groups  []timelineGroup
	Total   int
	NewDays int // 角标窗口天数，供页面文案展示
}

// isNewArrival 判断视频是否在新片窗口内入库。
func isNewArrival(v *media.Video, now time.Time) bool {
	if v == nil || v.ModTime.IsZero() {
		return false
	}
	return now.Sub(v.ModTime) <= newArrivalWindow
}

// handleNewArrivals 渲染新片时间线 /new：按入库时间（文件 ModTime）倒序，
// 并归入「最近 7 天 / 最近 30 天 / 更早」三个分组，不依赖任何持久化存储。
func (s *Server) handleNewArrivals(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	videos := s.library.Query(media.Query{Sort: "newest"})

	// 三个固定分组，空组不渲染，保证标题与时间口径稳定
	groupDefs := []struct {
		label string
		match func(*media.Video) bool
	}{
		{"最近 7 天", func(v *media.Video) bool { return now.Sub(v.ModTime) <= 7*24*time.Hour }},
		{"最近 30 天", func(v *media.Video) bool {
			d := now.Sub(v.ModTime)
			return d > 7*24*time.Hour && d <= newArrivalWindow
		}},
		{"更早", func(v *media.Video) bool { return now.Sub(v.ModTime) > newArrivalWindow }},
	}
	groups := make([]timelineGroup, 0, len(groupDefs))
	for _, def := range groupDefs {
		g := timelineGroup{Label: def.label}
		for _, v := range videos {
			if def.match(v) {
				g.Cards = append(g.Cards, detailCard{
					Video: v, Metadata: s.library.Metadata(v.ID),
				})
			}
		}
		if len(g.Cards) > 0 {
			groups = append(groups, g)
		}
	}

	s.render(w, "new_arrivals.html", newArrivalPageData{
		basePage: basePage{Title: "新片时间线", AuthEnabled: s.authEnabled()},
		Groups:   groups,
		Total:    len(videos),
		NewDays:  int(newArrivalWindow.Hours() / 24),
	})
}
