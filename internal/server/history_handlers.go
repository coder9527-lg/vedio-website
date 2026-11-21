package server

import (
	"net/http"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

const historyLimit = 100

// historyItem 是时间线上的一条会话，附带渲染卡片所需的视频/元信息/进度。
type historyItem struct {
	Video    *media.Video
	Metadata *media.Metadata
	Event    store.WatchEvent
	Progress store.Progress // 零值即无进度，模板用 hasProgress 判定
}

// historyGroup 是按本地日期归并后的一组会话。
type historyGroup struct {
	Label string
	Items []historyItem
}

// historyData 是观看历史页视图模型。
type historyData struct {
	basePage
	StoreOn bool
	Groups  []historyGroup
	Count   int
}

// handleHistory 渲染观看历史时间线：按今天/昨天/具体日期分组。
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	data := historyData{
		basePage: basePage{Title: "观看历史", AuthEnabled: s.authEnabled()},
		StoreOn:  s.storeEnabled(),
	}
	if !s.storeEnabled() {
		s.render(w, "history.html", data)
		return
	}

	events, err := s.store.ListWatchEvents(historyLimit)
	if err != nil {
		http.Error(w, "查询观看历史失败", http.StatusInternalServerError)
		return
	}
	progressMap, _ := s.store.ProgressMap()

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	yesterday := today.AddDate(0, 0, -1)

	for _, ev := range events {
		v, ok := s.library.Get(ev.VideoID)
		if !ok {
			continue // 视频已从媒体库移除，跳过悬空事件
		}
		item := historyItem{
			Video:    v,
			Metadata: s.library.Metadata(ev.VideoID), // 无元信息时为 nil，模板自行降级
			Event:    ev,
			Progress: progressMap[ev.VideoID],
		}

		localDay := ev.StartedAt.Local()
		localStart := time.Date(localDay.Year(), localDay.Month(), localDay.Day(), 0, 0, 0, 0, time.Local)
		var label string
		switch {
		case localStart.Equal(today):
			label = "今天"
		case localStart.Equal(yesterday):
			label = "昨天"
		default:
			label = localStart.Format("2006-01-02")
		}

		if len(data.Groups) == 0 || data.Groups[len(data.Groups)-1].Label != label {
			data.Groups = append(data.Groups, historyGroup{Label: label})
		}
		g := &data.Groups[len(data.Groups)-1]
		g.Items = append(g.Items, item)
		data.Count++
	}

	s.render(w, "history.html", data)
}

// handleClearHistory 清空全部观看历史，PRG 重定向回历史页避免重复提交。
func (s *Server) handleClearHistory(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	if err := s.store.ClearWatchEvents(); err != nil {
		http.Error(w, "清空观看历史失败", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/history", http.StatusSeeOther)
}
