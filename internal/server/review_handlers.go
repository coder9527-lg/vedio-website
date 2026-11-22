package server

import (
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/store"
)

// reviewTopN 限制年度回顾 Top 视频榜的条数。
const reviewTopN = 10

// reviewMinYear 是年份参数的下限，早于该值视为非法输入回退当前年。
const reviewMinYear = 1900

// reviewCheckinSeconds 是单日打卡阈值：当天累计观看满 10 分钟（600 秒）算打卡成功。
const reviewCheckinSeconds = 600

// heatCell 是年度热力图中的一个日格（含行列补齐用的占位格）。
type heatCell struct {
	Date     string  // "2006-01-02"，占位格为空，供 title 与测试断言
	Day      int     // 月内日号（1-31），占位格为 0
	Seconds  float64 // 当天累计观看秒数
	Level    int     // 颜色档位 0-4；不足打卡阈值与无活动均为 0
	InMonth  bool    // false 表示年初/年末对齐周列的占位格
	IsFuture bool    // 当前年今天之后的日期置灰，往年恒为 false
}

// heatMonthLabel 是热力图顶部的月份标签，Col 为该月 1 号所在周列（0 基）。
type heatMonthLabel struct {
	Label string
	Col   int
}

// monthStat 是年度回顾中单个自然月的聚合桶。
type monthStat struct {
	Month    int     // 1-12
	Label    string  // "1月"…"12月"
	Seconds  float64 // 当月累计观看秒数
	Sessions int     // 当月播放会话数
	Percent  int     // 相对最活跃月份的占比（0-100）
}

// reviewTopVideo 是年度观看时长榜中的一行：卡片数据加该视频本年度累计秒数。
type reviewTopVideo struct {
	Card    shelfCard
	Seconds float64
}

// reviewData 是年度回顾页的模板数据。聚合全部在 Go 内存完成，按本地时区归并。
type reviewData struct {
	basePage
	StoreOn         bool
	Year            int
	Years           []int // 有观看记录的年份，降序，供下拉切换
	HasData         bool
	WatchedVideos   int
	Sessions        int
	CompletedVideos int
	ActiveMonth     int // 最活跃月份（1-12），无数据为 0
	TotalSeconds    float64
	MonthlyAvgSec   float64
	DailyAvgSec     float64
	Months          []monthStat // 固定 12 项
	Genres          []distItem
	TopVideos       []reviewTopVideo
	// 观影打卡与年度热力图
	CheckinDays   int              // 本年度打卡总天数
	CurrentStreak int              // 当前连续打卡天数，仅当前年计算
	LongestStreak int              // 本年度最长连续打卡段
	HeatCells     []heatCell       // 列优先的日格序列（含占位），长度为 7 的倍数
	HeatMonths    []heatMonthLabel // 月份标签（12 项）
	HeatWeeks     int              // 周列数 = len(HeatCells)/7
}

// handleReview 渲染年度观影回顾。未启用持久化时与 /stats 一致渲染 200 空态页。
func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	data := reviewData{
		basePage: basePage{Title: "年度观影回顾", AuthEnabled: s.authEnabled()},
		StoreOn:  s.storeEnabled(),
		Year:     parseReviewYear(r.URL.Query().Get("year"), now),
	}
	if !s.storeEnabled() {
		s.render(w, "review.html", data)
		return
	}

	events, err := s.store.AllWatchEvents()
	if err != nil {
		// 查询失败记录日志并按零值渲染空态，避免整页 500（与 handleStats 同策略）
		log.Printf("读取观看事件失败: %v", err)
		s.render(w, "review.html", data)
		return
	}

	// 年份下拉：全量事件按本地时区取 distinct 年份后降序；当前查看年份即使无数据也补入，
	// 保证空态页下拉仍停留在所选年份
	yearSet := map[int]bool{}
	for _, ev := range events {
		yearSet[ev.StartedAt.Local().Year()] = true
	}
	yearSet[data.Year] = true
	data.Years = make([]int, 0, len(yearSet))
	for y := range yearSet {
		data.Years = append(data.Years, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(data.Years)))

	// 进度记录：用于完播数口径与 Top 榜卡片的续播信息
	progressList, err := s.store.AllProgress()
	if err != nil {
		log.Printf("读取播放进度失败: %v", err)
	}
	progressMap := make(map[string]store.Progress, len(progressList))
	for _, p := range progressList {
		progressMap[p.VideoID] = p
	}

	s.aggregateYearReview(&data, events, progressMap, progressList, now)
	s.render(w, "review.html", data)
}

// parseReviewYear 解析 ?year= 参数：空值取当前年；非法、过小或超过下一年的输入回退当前年。
func parseReviewYear(raw string, now time.Time) int {
	if raw == "" {
		return now.Year()
	}
	y, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || y < reviewMinYear || y > now.Year()+1 {
		return now.Year()
	}
	return y
}

// aggregateYearReview 把全量会话流水在内存中按本地时区聚合到指定年份。
// 库内时间统一 UTC 存储，必须先 Local() 再按年月归并，禁止用 SQLite 字符串截取（会错分跨年）。
func (s *Server) aggregateYearReview(data *reviewData, events []store.WatchEvent,
	progressMap map[string]store.Progress, progressList []store.Progress, now time.Time) {
	year := data.Year

	months := make([]monthStat, 12)
	for i := range months {
		months[i] = monthStat{Month: i + 1, Label: strconv.Itoa(i+1) + "月"}
	}

	distinct := map[string]bool{}
	videoSeconds := map[string]float64{}
	// daySeconds 按本地自然日累计观看秒数，供打卡判定与热力图使用
	daySeconds := map[string]float64{}
	var totalSeconds float64
	sessions := 0

	for _, ev := range events {
		v, ok := s.library.Get(ev.VideoID)
		if !ok {
			continue // 视频已从媒体库移除，悬空事件不参与年度聚合
		}
		local := ev.StartedAt.Local()
		if local.Year() != year {
			continue
		}
		sessions++
		totalSeconds += ev.WatchSeconds
		distinct[v.ID] = true
		videoSeconds[v.ID] += ev.WatchSeconds
		daySeconds[local.Format("2006-01-02")] += ev.WatchSeconds

		bucket := &months[int(local.Month())-1]
		bucket.Seconds += ev.WatchSeconds
		bucket.Sessions++
	}

	// 月度占比：以最活跃月份为 100%，平局时最活跃月取较小月
	maxMonthSeconds := 0.0
	activeMonth := 0
	for i := range months {
		if months[i].Seconds > maxMonthSeconds {
			maxMonthSeconds = months[i].Seconds
			activeMonth = months[i].Month
		}
	}
	for i := range months {
		if maxMonthSeconds > 0 {
			months[i].Percent = int(math.Round(months[i].Seconds * 100 / maxMonthSeconds))
		}
	}

	// 类型偏好：本年度有观看记录的去重视频，按元信息类型各计一次
	genreCount := map[string]int{}
	for videoID := range distinct {
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
	}
	genres := buildDistItems(genreCount, maxGenreDist, func(keys []string) {
		sort.Slice(keys, func(i, j int) bool {
			if genreCount[keys[i]] != genreCount[keys[j]] {
				return genreCount[keys[i]] > genreCount[keys[j]] // 计数降序
			}
			return keys[i] < keys[j] // 同数次按名称升序，保证确定性
		})
	})

	// Top 视频榜：按本年度累计秒数降序，平局按视频 ID 升序
	ids := make([]string, 0, len(videoSeconds))
	for id := range videoSeconds {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if videoSeconds[ids[i]] != videoSeconds[ids[j]] {
			return videoSeconds[ids[i]] > videoSeconds[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > reviewTopN {
		ids = ids[:reviewTopN]
	}
	top := make([]reviewTopVideo, 0, len(ids))
	for _, id := range ids {
		v, ok := s.library.Get(id)
		if !ok {
			continue
		}
		top = append(top, reviewTopVideo{
			Card: shelfCard{
				Video:    v,
				Progress: progressMap[id], // 零值即无进度，模板自行降级
				Metadata: s.library.Metadata(id),
			},
			Seconds: videoSeconds[id],
		})
	}

	// 完播数：watch_events 不记录完播标记，改用 progress 的黏滞 completed=1，
	// 且其 updated_at 落在本年度、视频仍在库中。属近似口径（往年看完今年又打开的会计入本年）。
	completed := 0
	for _, p := range progressList {
		if !p.Completed || p.UpdatedAt.Local().Year() != year {
			continue
		}
		if _, ok := s.library.Get(p.VideoID); ok {
			completed++
		}
	}

	// 均值分母：当年用已过去的月数/天数，往年用全年；兜底 1 防除零
	monthDenom := 12
	dayDenom := yearDays(year)
	if year == now.Year() {
		monthDenom = int(now.Month())
		dayDenom = now.YearDay()
	} else if year > now.Year() {
		monthDenom = 1
		dayDenom = 1
	}
	if monthDenom < 1 {
		monthDenom = 1
	}
	if dayDenom < 1 {
		dayDenom = 1
	}

	data.HasData = sessions > 0
	data.WatchedVideos = len(distinct)
	data.Sessions = sessions
	data.CompletedVideos = completed
	data.ActiveMonth = activeMonth
	data.TotalSeconds = totalSeconds
	data.MonthlyAvgSec = totalSeconds / float64(monthDenom)
	data.DailyAvgSec = totalSeconds / float64(dayDenom)
	data.Months = months
	data.Genres = genres
	data.TopVideos = top

	s.aggregateCheckin(data, daySeconds, now)
}

// checkinLevel 按当天累计秒数返回热力图色档：不足打卡阈值与无活动均为 0。
func checkinLevel(seconds float64) int {
	switch {
	case seconds < reviewCheckinSeconds:
		return 0
	case seconds < 1800: // [10 分钟, 30 分钟)
		return 1
	case seconds < 3600: // [30 分钟, 1 小时)
		return 2
	case seconds < 7200: // [1 小时, 2 小时)
		return 3
	default: // 2 小时及以上
		return 4
	}
}

// aggregateCheckin 基于每日累计秒数计算打卡统计并生成列优先的年度热力图格子序列。
// 库内时间已在调用方按本地时区归并，这里直接按自然日处理。
func (s *Server) aggregateCheckin(data *reviewData, daySeconds map[string]float64, now time.Time) {
	year := data.Year
	jan1 := time.Date(year, 1, 1, 0, 0, 0, 0, time.Local)
	// Go 的 Weekday 周日=0，转换为周一起点的偏移量
	lead := (int(jan1.Weekday()) + 6) % 7

	totalDays := yearDays(year)
	capacity := lead + totalDays
	if rem := capacity % 7; rem != 0 {
		capacity += 7 - rem
	}
	cells := make([]heatCell, 0, capacity)

	// 年初占位：对齐到周一所在列
	for i := 0; i < lead; i++ {
		cells = append(cells, heatCell{})
	}

	// 打卡集合（仅满阈值的自然日入集合），供连续天数扫描
	checkinSet := map[string]bool{}
	todayStr := now.Format("2006-01-02")
	for d := 0; d < totalDays; d++ {
		date := jan1.AddDate(0, 0, d)
		dateStr := date.Format("2006-01-02")
		seconds := daySeconds[dateStr]
		level := checkinLevel(seconds)
		if level > 0 {
			checkinSet[dateStr] = true
		}
		cells = append(cells, heatCell{
			Date:     dateStr,
			Day:      date.Day(),
			Seconds:  seconds,
			Level:    level,
			InMonth:  true,
			IsFuture: year == now.Year() && dateStr > todayStr,
		})
	}

	// 年末占位补齐整周
	for len(cells)%7 != 0 {
		cells = append(cells, heatCell{})
	}

	// 月份标签：每月 1 号所在周列 = (年初偏移 + 年序日-1) / 7
	monthLabels := make([]heatMonthLabel, 12)
	for m := 1; m <= 12; m++ {
		first := time.Date(year, time.Month(m), 1, 0, 0, 0, 0, time.Local)
		col := (lead + first.YearDay() - 1) / 7
		monthLabels[m-1] = heatMonthLabel{Label: strconv.Itoa(m) + "月", Col: col}
	}

	// 最长连续打卡：从 1 月 1 日按自然日扫描打卡集合
	longest, run := 0, 0
	for d := 0; d < totalDays; d++ {
		dateStr := jan1.AddDate(0, 0, d).Format("2006-01-02")
		if checkinSet[dateStr] {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}

	// 当前连续：仅当前年计算；今天尚未结束，未打卡不中断，从昨天起向前数
	current := 0
	if year == now.Year() {
		cursor := now.AddDate(0, 0, -1)
		if todayCheckin := checkinSet[todayStr]; todayCheckin {
			current = 1
			cursor = now // 今天已打卡则从今天起向前
		}
		for {
			key := cursor.Format("2006-01-02")
			if !checkinSet[key] {
				break
			}
			if key == todayStr {
				cursor = cursor.AddDate(0, 0, -1)
				continue
			}
			current++
			cursor = cursor.AddDate(0, 0, -1)
			if cursor.Year() < year {
				break
			}
		}
	}

	data.CheckinDays = len(checkinSet)
	data.CurrentStreak = current
	data.LongestStreak = longest
	data.HeatCells = cells
	data.HeatMonths = monthLabels
	data.HeatWeeks = len(cells) / 7
}

// yearDays 返回某年的全年天数，闰年 366 天。
func yearDays(year int) int {
	if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
		return 366
	}
	return 365
}
