package server

import (
	"log"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/store"
)

// statsShelfSize 限制统计页每个横向行的卡片数量，与首页 shelfSize 同值。
const statsShelfSize = 12

// 分布区展示条数上限。
const (
	maxGenreDist = 8
	maxYearDist  = 10
)

// distItem 是类型/年份分布中的一行：名称、视频数、相对最大计数的占比（0-100）。
type distItem struct {
	Name    string
	Count   int
	Percent int
}

// statsData 是观影统计页的模板数据。
type statsData struct {
	basePage
	StoreOn        bool
	Stats          store.WatchStats
	Genres         []distItem
	Years          []distItem
	RecentCards    []shelfCard
	CompletedCards []shelfCard
}

// handleStats 渲染观影统计仪表盘。未启用持久化时渲染空态而非 503（页面而非 API）。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	data := statsData{
		basePage: basePage{Title: "观影统计", AuthEnabled: s.authEnabled()},
		StoreOn:  s.storeEnabled(),
	}
	if !s.storeEnabled() {
		s.render(w, "stats.html", data)
		return
	}

	// 顶部聚合数字：查询失败时记录日志并按零值继续，避免整页 500
	stats, err := s.store.WatchStats()
	if err != nil {
		log.Printf("读取观影统计失败: %v", err)
	}
	data.Stats = stats

	data.RecentCards = s.buildStatsShelf(s.recentProgressIDs())
	data.CompletedCards = s.buildStatsShelf(s.completedProgressIDs())
	data.Genres, data.Years = s.buildDistribution()

	s.render(w, "stats.html", data)
}

// recentProgressIDs 取最近观看的进度记录，悬空视频由 buildStatsShelf 跳过。
func (s *Server) recentProgressIDs() []store.Progress {
	list, err := s.store.RecentProgress(statsShelfSize)
	if err != nil {
		log.Printf("读取最近观看失败: %v", err)
		return nil
	}
	return list
}

// completedProgressIDs 取已看完的进度记录。
func (s *Server) completedProgressIDs() []store.Progress {
	list, err := s.store.CompletedProgress(statsShelfSize)
	if err != nil {
		log.Printf("读取已看完失败: %v", err)
		return nil
	}
	return list
}

// buildStatsShelf 把一组进度记录组装成横向行卡片，跳过已从媒体库删除的悬空 ID。
func (s *Server) buildStatsShelf(list []store.Progress) []shelfCard {
	cards := make([]shelfCard, 0, len(list))
	for _, p := range list {
		v, ok := s.library.Get(p.VideoID)
		if !ok {
			continue
		}
		cards = append(cards, shelfCard{
			Video:    v,
			Progress: p,
			Metadata: s.library.Metadata(v.ID),
		})
	}
	return cards
}

// buildDistribution 基于全部进度记录与内存库元信息，统计最常看类型与年份分布。
// 无元信息的视频不参与（与 library.AllGenres 口径一致）。
func (s *Server) buildDistribution() (genres []distItem, years []distItem) {
	progressMap, err := s.store.ProgressMap()
	if err != nil {
		log.Printf("读取进度映射失败: %v", err)
		return nil, nil
	}
	genreCount := map[string]int{}
	yearCount := map[string]int{}
	for videoID := range progressMap {
		md := s.library.Metadata(videoID)
		if md == nil {
			continue
		}
		for _, g := range md.Genres {
			g = strings.TrimSpace(g)
			if g != "" {
				genreCount[g]++
			}
		}
		if y := strings.TrimSpace(md.Year); y != "" {
			yearCount[y]++
		}
	}
	genres = buildDistItems(genreCount, maxGenreDist, func(keys []string) {
		sort.Slice(keys, func(i, j int) bool {
			if genreCount[keys[i]] != genreCount[keys[j]] {
				return genreCount[keys[i]] > genreCount[keys[j]] // 计数降序
			}
			return keys[i] < keys[j] // 同数次按名称升序，保证确定性
		})
	})
	years = buildDistItems(yearCount, maxYearDist, func(keys []string) {
		sort.Sort(sort.Reverse(sort.StringSlice(keys))) // 年份字符串降序：近 → 远
	})
	return genres, years
}

// buildDistItems 把计数 map 按给定键排序后截取前 limit 条，并换算相对最大值的占比。
func buildDistItems(count map[string]int, limit int, sortKeys func([]string)) []distItem {
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sortKeys(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	max := 0
	for _, k := range keys {
		if count[k] > max {
			max = count[k]
		}
	}
	items := make([]distItem, 0, len(keys))
	for _, k := range keys {
		pct := 0
		if max > 0 {
			pct = int(math.Round(float64(count[k]) * 100 / float64(max)))
		}
		items = append(items, distItem{Name: k, Count: count[k], Percent: pct})
	}
	return items
}
