package server

import (
	"net/http"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// continueFetchLimit 是续播提醒页一次拉取的进度上限：progress 表按视频去重，
// 取足够大的值即可覆盖实际媒体库规模，再在内存里按有效进度过滤。
const continueFetchLimit = 1000

// continueCard 是续播提醒页的单张卡片：在视频/进度基础上预算好剩余时长、
// 进度百分比与相对时间，避免模板做算术（模板 FuncMap 无加法）。
type continueCard struct {
	Video    *media.Video
	Metadata *media.Metadata
	Progress store.Progress
	Remain   float64 // 剩余秒数（时长未知时为 0，模板据此降级文案）
	Percent  int     // 已看百分比（时长未知时为 0）
	LastSeen string  // 最近观看的相对时间文案，如「3 分钟前」
}

// continuePageData 是续播提醒独立页面的模板数据。
type continuePageData struct {
	basePage
	StoreOn bool
	Cards   []continueCard
}

// handleContinuePage 渲染续播提醒页：聚合所有「看到一半」（位置 >5s 且未到 95%）
// 的视频，按最近观看倒序，展示已看/总长/剩余时长，点卡片即进播放页自动续播。
// 未启用持久化时渲染友好提示页（不跳转、不报错），与项目零数据库默认约定一致。
func (s *Server) handleContinuePage(w http.ResponseWriter, r *http.Request) {
	data := continuePageData{
		basePage: basePage{Title: "续播提醒", AuthEnabled: s.authEnabled()},
		StoreOn:  s.storeEnabled(),
	}
	if !s.storeEnabled() {
		s.render(w, "continue.html", data)
		return
	}

	recent, err := s.store.RecentProgress(continueFetchLimit)
	if err != nil {
		http.Error(w, "读取续播清单失败", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	cards := make([]continueCard, 0, len(recent))
	for _, p := range recent {
		// 与首页「继续观看」、智能清单 continue 规则严格同口径
		if !hasProgressValue(p.Position, p.Duration) {
			continue
		}
		v, ok := s.library.Get(p.VideoID)
		if !ok {
			continue // 视频文件已删除，悬空进度跳过
		}
		card := continueCard{
			Video:    v,
			Metadata: s.library.Metadata(p.VideoID),
			Progress: p,
			Percent:  progressPercentOf(p.Position, p.Duration),
			LastSeen: humanizeAgo(now, p.UpdatedAt),
		}
		if p.Duration > 0 && p.Duration > p.Position {
			card.Remain = p.Duration - p.Position
		}
		cards = append(cards, card)
	}
	data.Cards = cards
	s.render(w, "continue.html", data)
}

// progressPercentOf 计算已看百分比，时长未知或位置非法时返回 0，封顶 100。
func progressPercentOf(position, duration float64) int {
	if duration <= 0 || position <= 0 {
		return 0
	}
	pct := int(position / duration * 100)
	if pct > 100 {
		pct = 100
	}
	return pct
}

// humanizeAgo 把时间点格式化为中文相对时间（刚刚 / N 分钟前 / N 小时前 / N 天前 / 具体日期）。
func humanizeAgo(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return itoa(int(d / time.Minute)) + " 分钟前"
	case d < 24*time.Hour:
		return itoa(int(d / time.Hour)) + " 小时前"
	case d < 30*24*time.Hour:
		return itoa(int(d / (24 * time.Hour))) + " 天前"
	default:
		return t.Format("2006-01-02")
	}
}