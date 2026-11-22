package server

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// addReviewEvent 直接在 store 中造一条指定开始时间与观看秒数的会话流水。
func addReviewEvent(t *testing.T, srv *Server, videoID string, at time.Time, seconds float64) {
	t.Helper()
	id, err := srv.store.StartWatchEvent(videoID, at)
	if err != nil {
		t.Fatalf("StartWatchEvent 失败: %v", err)
	}
	if err := srv.store.UpdateWatchEvent(id, seconds, seconds, 0, at.Add(time.Duration(seconds)*time.Second)); err != nil {
		t.Fatalf("UpdateWatchEvent 失败: %v", err)
	}
}

// TestReviewPageRenders 固定 2026 年的跨月会话：概览数字、月度分布、Top 榜与目标面板均渲染。
func TestReviewPageRenders(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)
	base := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	addReviewEvent(t, srv, id, base, 600)
	addReviewEvent(t, srv, id, base.AddDate(0, 1, 0), 1200)

	code, body := getHTML(t, ts, "/review?year=2026")
	if code != http.StatusOK {
		t.Fatalf("/review 应 200，实际 %d", code)
	}
	containsAll(t, body,
		"2026 年度观影回顾",
		"年度概览",
		"月度观看分布",
		"月度观看目标",
		`name="year"`,
		"3月",
		"4月",
		"年度观看榜",
		`id="goal-input"`,
		"/static/js/review.js",
	)
	// 同一视频两条会话：去重 1 部、会话 2 次、总时长 30 分钟
	if strings.Count(body, "stat-card") < 6 {
		t.Fatalf("应渲染 6 张概览卡，实际出现 %d 次 stat-card", strings.Count(body, "stat-card"))
	}
}

// TestReviewUTCYearBoundary 库内 UTC 时间必须先转本地再归年：
// UTC 2025-12-31 17:30 在 Asia/Shanghai 已是 2026-01-01，应计入 2026 年。
func TestReviewUTCYearBoundary(t *testing.T) {
	origLocal := time.Local
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	time.Local = loc
	t.Cleanup(func() { time.Local = origLocal })

	srv, ts, id := newPersistTestServer(t)
	// 本地 2026-01-01 01:30 = UTC 2025-12-31 17:30
	utcEvent := time.Date(2025, 12, 31, 17, 30, 0, 0, time.UTC)
	addReviewEvent(t, srv, id, utcEvent, 900)

	_, body2026 := getHTML(t, ts, "/review?year=2026")
	if !strings.Contains(body2026, "1月") {
		t.Fatal("2026 页面缺少 1 月行")
	}
	// 900 秒 = 15 分钟，归入本地 2026 年 1 月后应在月度行中渲染
	if !strings.Contains(body2026, "15 分钟") {
		t.Fatal("2026 页面未渲染跨年事件的 15 分钟观看时长")
	}
	// 2026 应有数据（非空态），2025 应是空态
	if strings.Contains(body2026, "2026 年还没有观看记录") {
		t.Fatal("UTC 跨年事件应归入本地 2026 年，却渲染了空态")
	}
	_, body2025 := getHTML(t, ts, "/review?year=2025")
	if !strings.Contains(body2025, "2025 年还没有观看记录") {
		t.Fatal("本地归年后 2025 应无数据并渲染空态")
	}
}

// TestReviewEmptyStore 持久化开启但无任何会话时渲染无数据空态，且不出现概览卡。
func TestReviewEmptyStore(t *testing.T) {
	_, ts, _ := newPersistTestServer(t)

	code, body := getHTML(t, ts, "/review")
	if code != http.StatusOK {
		t.Fatalf("空库 /review 应 200，实际 %d", code)
	}
	if !strings.Contains(body, "还没有观看记录") {
		t.Fatal("空库应渲染无数据空态")
	}
	if strings.Contains(body, "stat-card") {
		t.Fatal("空库不应渲染概览卡片")
	}
	if strings.Contains(body, "goal-panel") {
		t.Fatal("空库不应渲染月度目标面板")
	}
}

// TestReviewYearParamFallback 非法与越界年份回退当前年；合法但无数据的年份保留并渲染空态。
func TestReviewYearParamFallback(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)
	currentYear := time.Now().Year()
	addReviewEvent(t, srv, id, time.Date(currentYear, 1, 5, 8, 0, 0, 0, time.UTC), 300)

	// 合法但无数据的年份：不回退，渲染该年空态，下拉保留该年
	_, bodyOld := getHTML(t, ts, "/review?year=2020")
	if !strings.Contains(bodyOld, "2020 年还没有观看记录") {
		t.Fatal("?year=2020 应保留年份并渲染空态")
	}
	if !strings.Contains(bodyOld, `value="2020"`) {
		t.Fatal("无数据年份也应出现在下拉中")
	}

	// 非法字符串回退当前年：当前年有数据，不应出现空态文案
	_, bodyBad := getHTML(t, ts, "/review?year=abc")
	if strings.Contains(bodyBad, "年还没有观看记录") {
		t.Fatal("非法 year 应回退当前年（有数据），不应渲染空态")
	}
	// 越界年份（超过下一年）同样回退
	_, bodyFuture := getHTML(t, ts, "/review?year=1800")
	if !strings.Contains(bodyFuture, `value="`+itoa(currentYear)+`"`) {
		t.Fatalf("越界 year 应回退当前年 %d", currentYear)
	}
}

// TestReviewStoreOff 未启用持久化时 200 渲染空态，无概览卡；首页导航不出现 /review。
func TestReviewStoreOff(t *testing.T) {
	_, ts := newTestServer(t, "")

	code, body := getHTML(t, ts, "/review")
	if code != http.StatusOK {
		t.Fatalf("无 DB 时 /review 应 200 渲染空态，实际 %d", code)
	}
	if !strings.Contains(body, "未启用持久化") {
		t.Fatal("应显示未启用持久化空态提示")
	}
	if strings.Contains(body, "stat-card") {
		t.Fatal("无 DB 时不应渲染概览卡片")
	}

	_, indexHTML := getHTML(t, ts, "/")
	if strings.Contains(indexHTML, `href="/review"`) {
		t.Fatal("未启用持久化时首页导航不应出现年度回顾入口")
	}
}

// TestReviewDanglingEventExcluded 媒体库中已删除视频的悬空会话不参与年度聚合。
func TestReviewDanglingEventExcluded(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	addReviewEvent(t, srv, id, base, 600)
	addReviewEvent(t, srv, "ghost-video-id", base.Add(time.Hour), 600)

	_, body := getHTML(t, ts, "/review?year=2026")
	if strings.Contains(body, "2026 年还没有观看记录") {
		t.Fatal("有效视频的会话应保留，不应整体空态")
	}
	// 悬空事件被剔除后只剩 1 次会话；页面不应出现 ghost 视频的链接
	if strings.Contains(body, "/watch/ghost-video-id") {
		t.Fatal("悬空视频不应出现在年度榜中")
	}
}

// TestReviewDistinctAndSessions 同一视频两条会话：去重 1 部、会话 2 次、秒数累加。
func TestReviewDistinctAndSessions(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)
	base := time.Date(2026, 2, 10, 9, 0, 0, 0, time.UTC)
	addReviewEvent(t, srv, id, base, 600)
	addReviewEvent(t, srv, id, base.Add(2*time.Hour), 900)

	_, body := getHTML(t, ts, "/review?year=2026")
	// 总时长 1500 秒 = 25 分钟，经 formatWatchHours 渲染为「25 分钟」
	if !strings.Contains(body, "25 分钟") {
		t.Fatalf("两条会话秒数应累加为 25 分钟，页面未找到对应文案")
	}
	// Top 榜只出现一次该视频链接
	if got := strings.Count(body, "/watch/"+id); got != 1 {
		t.Fatalf("同视频两条会话应聚合为 1 个榜单卡片，实际出现 %d 次", got)
	}
}
