package server

import (
	"encoding/json"
	"html/template"
	"math"
	"net/http"
	"sort"
	"strconv"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// 全局书签页是只读汇总视图：复用 bookmarks 表，不新增表；
// 页内的删除/改备注直接调既有书签 API，本 handler 只负责首屏渲染与数据岛装配。

// bookmarkViewItem 是全局页上的一条书签，预生成跳转链接与展示文案。
type bookmarkViewItem struct {
	Bookmark store.Bookmark
	TimeText string // 秒 → h:m:s 展示，与播放页书签面板同口径
	SeekURL  string // /watch/{id}?t=整数秒，复用播放页既有 ?t= 跳转约定
}

// bookmarkGroup 是一个视频下的全部书签（组内按位置升序）。
type bookmarkGroup struct {
	Video     *media.Video
	Metadata  *media.Metadata // 无元信息时为 nil，模板自行降级
	VideoName string          // 预解析展示名（元信息标题优先），供数据岛复用
	Items     []bookmarkViewItem
	LatestAt  int64 // 组内最新书签的创建时间（Unix 秒），决定组间排序
}

// bookmarkRecord 是注入数据岛的单条书签扁平结构，
// 前端搜索/平铺视图/导出全部以此为唯一数据源，避免模板与 JS 各维护一份字段。
type bookmarkRecord struct {
	ID        int64   `json:"id"`
	VideoID   string  `json:"videoId"`
	VideoName string  `json:"videoName"`
	Position  float64 `json:"position"`
	TimeText  string  `json:"timeText"`
	Note      string  `json:"note"`
	SeekURL   string  `json:"seekUrl"`
	CreatedAt int64   `json:"createdAt"`
}

// bookmarksPageData 是全局书签页视图模型。
type bookmarksPageData struct {
	basePage
	StoreOn     bool
	Groups      []bookmarkGroup
	TotalMarks  int         // 页面上实际展示的书签总数（已剔除悬空视频）
	OrphanMarks int         // 视频已不在媒体库、被跳过的悬空书签数
	RecordsJSON template.JS // 数据岛：全部书签的扁平 JSON，仅持久化且有数据时注入
}

// handleBookmarksPage 渲染全局书签页：按视频分组，组间按最新书签倒序。
// 无持久化时与观看历史页同策略——渲染页面但展示未启用提示，而非 404。
func (s *Server) handleBookmarksPage(w http.ResponseWriter, r *http.Request) {
	data := bookmarksPageData{
		basePage: basePage{Title: "全部书签", AuthEnabled: s.authEnabled()},
		StoreOn:  s.storeEnabled(),
	}
	if !s.storeEnabled() {
		s.render(w, "bookmarks.html", data)
		return
	}

	all, err := s.store.ListAllBookmarks()
	if err != nil {
		http.Error(w, "查询书签失败", http.StatusInternalServerError)
		return
	}

	// ListAllBookmarks 已按 created_at 倒序，map 只做视频 ID → 分组下标的寻址，
	// 分组首次出现的顺序即最新书签顺序，不依赖 map 迭代。
	groupIndex := map[string]int{}
	records := make([]bookmarkRecord, 0, len(all))
	for _, bm := range all {
		v, ok := s.library.Get(bm.VideoID)
		if !ok {
			data.OrphanMarks++ // 视频已从媒体库移除，跳过悬空书签
			continue
		}
		md := s.library.Metadata(bm.VideoID)
		gIdx, exists := groupIndex[bm.VideoID]
		if !exists {
			gIdx = len(data.Groups)
			groupIndex[bm.VideoID] = gIdx
			data.Groups = append(data.Groups, bookmarkGroup{
				Video:     v,
				Metadata:  md,
				VideoName: displayVideoName(v, md),
			})
		}
		g := &data.Groups[gIdx]
		timeText := formatBookmarkClock(bm.Position)
		seekURL := "/watch/" + bm.VideoID + "?t=" + strconv.FormatInt(int64(math.Round(bm.Position)), 10)
		g.Items = append(g.Items, bookmarkViewItem{Bookmark: bm, TimeText: timeText, SeekURL: seekURL})
		if ts := bm.CreatedAt.Unix(); ts > g.LatestAt {
			g.LatestAt = ts
		}
		data.TotalMarks++
		records = append(records, bookmarkRecord{
			ID:        bm.ID,
			VideoID:   bm.VideoID,
			VideoName: g.VideoName,
			Position:  bm.Position,
			TimeText:  timeText,
			Note:      bm.Note,
			SeekURL:   seekURL,
			CreatedAt: bm.CreatedAt.Unix(),
		})
	}

	// 组内按位置升序、同位置按 id 升序，与 ListBookmarks 单视频口径一致
	for i := range data.Groups {
		items := data.Groups[i].Items
		sort.Slice(items, func(a, b int) bool {
			if items[a].Bookmark.Position != items[b].Bookmark.Position {
				return items[a].Bookmark.Position < items[b].Bookmark.Position
			}
			return items[a].Bookmark.ID < items[b].Bookmark.ID
		})
	}
	// 组间按最新书签倒序：最近打点的视频排在最前
	sort.SliceStable(data.Groups, func(i, j int) bool {
		return data.Groups[i].LatestAt > data.Groups[j].LatestAt
	})

	if len(records) > 0 {
		raw, err := json.Marshal(records)
		if err != nil {
			http.Error(w, "书签数据序列化失败", http.StatusInternalServerError)
			return
		}
		data.RecordsJSON = template.JS(raw) // 内容来自本机库数据，经 encoding/json 转义后注入
	}

	s.render(w, "bookmarks.html", data)
}

// displayVideoName 与搜索/智能清单页同口径：有元信息标题用标题，否则用文件名。
func displayVideoName(v *media.Video, md *media.Metadata) string {
	if md != nil && md.Title != "" {
		return md.Title
	}
	return v.Name
}

// formatBookmarkClock 把秒数格式化为 mm:ss / h:mm:ss，与播放页 formatClock 同口径。
func formatBookmarkClock(sec float64) string {
	total := int(math.Max(0, math.Floor(sec)))
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return strconv.Itoa(h) + ":" + pad2(m) + ":" + pad2(s)
	}
	return pad2(m) + ":" + pad2(s)
}
