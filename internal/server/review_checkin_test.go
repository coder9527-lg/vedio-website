package server

import (
	"testing"
	"time"
)

// useShanghaiLocal 把测试进程时区切到 Asia/Shanghai 并注册还原，保证按本地自然日归并的断言稳定。
func useShanghaiLocal(t *testing.T) {
	t.Helper()
	origLocal := time.Local
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	time.Local = loc
	t.Cleanup(func() { time.Local = origLocal })
}

// TestCheckinLevelBoundaries 验证打卡色档边界：阈值 600 秒，边界值恰好落在高档。
func TestCheckinLevelBoundaries(t *testing.T) {
	cases := []struct {
		seconds float64
		want    int
	}{
		{0, 0},
		{599, 0},   // 差 1 秒未达标，不打卡
		{600, 1},   // 恰好 10 分钟
		{1799, 1},  // 一档上沿
		{1800, 2},  // 恰好 30 分钟
		{3599, 2},  // 二档上沿
		{3600, 3},  // 恰好 1 小时
		{7199, 3},  // 三档上沿
		{7200, 4},  // 恰好 2 小时
		{99999, 4}, // 超长观看封顶四档
	}
	for _, c := range cases {
		if got := checkinLevel(c.seconds); got != c.want {
			t.Errorf("checkinLevel(%.0f) = %d，期望 %d", c.seconds, got, c.want)
		}
	}
}

// TestHeatCellLayout 验证热力图格子布局：周一对齐的前置占位、总长度为 7 的倍数、
// 月份标签列号与闰年天数。
func TestHeatCellLayout(t *testing.T) {
	useShanghaiLocal(t)

	t.Run("2026 年 1 月 1 日周四，前置占位 3 格", func(t *testing.T) {
		srv := &Server{}
		now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.Local)
		data := &reviewData{Year: 2026}
		// 1 月 1 日恰好打卡 600 秒，用于定位真实格下标
		daySeconds := map[string]float64{"2026-01-01": 600}

		srv.aggregateCheckin(data, daySeconds, now)

		if len(data.HeatCells)%7 != 0 {
			t.Fatalf("格子总数应为 7 的倍数，实际 %d", len(data.HeatCells))
		}
		if data.HeatWeeks != len(data.HeatCells)/7 {
			t.Fatalf("HeatWeeks=%d 与格子数 %d 不一致", data.HeatWeeks, len(data.HeatCells))
		}
		// 前置 3 个占位格（InMonth=false），第 4 格才是 1 月 1 日
		for i := 0; i < 3; i++ {
			if data.HeatCells[i].InMonth {
				t.Fatalf("下标 %d 应为年初占位格", i)
			}
		}
		first := data.HeatCells[3]
		if !first.InMonth || first.Date != "2026-01-01" || first.Day != 1 || first.Level != 1 {
			t.Fatalf("第 4 格应为 2026-01-01 打卡格，实际 %+v", first)
		}
		// 平年 365 个真实格 + 3 前置 + 4 年末补齐 = 371（53 列）
		inMonth := 0
		for _, c := range data.HeatCells {
			if c.InMonth {
				inMonth++
			}
		}
		if inMonth != 365 {
			t.Fatalf("2026 平年应有 365 个真实格，实际 %d", inMonth)
		}
		if data.HeatWeeks != 53 {
			t.Fatalf("2026 年应排 53 列，实际 %d", data.HeatWeeks)
		}
		// 月份标签：1 月在第 0 列；2 月 1 日是年序日 32，列号 (3+31)/7=4
		if data.HeatMonths[0].Col != 0 || data.HeatMonths[0].Label != "1月" {
			t.Fatalf("1 月标签列号异常: %+v", data.HeatMonths[0])
		}
		if data.HeatMonths[1].Col != 4 || data.HeatMonths[1].Label != "2月" {
			t.Fatalf("2 月标签应在第 4 列，实际 %+v", data.HeatMonths[1])
		}
		if len(data.HeatMonths) != 12 {
			t.Fatalf("应输出 12 个月份标签，实际 %d", len(data.HeatMonths))
		}
		// 未来日置灰：now=6 月 15 日，6 月 16 日 IsFuture=true，6 月 15 日不置灰
		var future, today *heatCell
		for i := range data.HeatCells {
			switch data.HeatCells[i].Date {
			case "2026-06-16":
				future = &data.HeatCells[i]
			case "2026-06-15":
				today = &data.HeatCells[i]
			}
		}
		if future == nil || !future.IsFuture {
			t.Fatal("2026-06-16 应标记为未来日")
		}
		if today == nil || today.IsFuture {
			t.Fatal("2026-06-15 是今天，不应标记为未来日")
		}
	})

	t.Run("2024 闰年 366 个真实格", func(t *testing.T) {
		srv := &Server{}
		// 查看往年：now 故意落在 2026 年，验证往年 IsFuture 恒为 false
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
		data := &reviewData{Year: 2024}

		srv.aggregateCheckin(data, nil, now)

		inMonth := 0
		for _, c := range data.HeatCells {
			if c.InMonth {
				inMonth++
			}
			if c.IsFuture {
				t.Fatal("往年热力图不应出现未来日置灰")
			}
		}
		if inMonth != 366 {
			t.Fatalf("2024 闰年应有 366 个真实格，实际 %d", inMonth)
		}
		if len(data.HeatCells)%7 != 0 {
			t.Fatalf("格子总数应为 7 的倍数，实际 %d", len(data.HeatCells))
		}
		// 2024-01-01 是周一，lead=0，年末补齐 6 格，共 371 格 53 列
		if data.HeatCells[0].Date != "2024-01-01" {
			t.Fatalf("2024-01-01 是周一，首格即真实格，实际 %+v", data.HeatCells[0])
		}
		if data.HeatWeeks != 53 {
			t.Fatalf("2024 年应排 53 列，实际 %d", data.HeatWeeks)
		}
	})
}

// TestStreakCalc 验证最长/当前连续打卡口径：中断清零、今天未打卡不中断、往年不计算当前连续。
func TestStreakCalc(t *testing.T) {
	useShanghaiLocal(t)
	srv := &Server{}

	t.Run("当前年：连续 3 天中断 1 天后再打卡，今天未打卡不中断当前连续", func(t *testing.T) {
		// now=2026-03-10，打卡日为 3/5、3/6、3/7（连续 3 天），跳过 3/8，3/9 再打卡；
		// 今天 3/10 未打卡，当前连续应从昨天 3/9 起向前数，遇 3/8 空缺停止，为 1 天
		now := time.Date(2026, 3, 10, 20, 0, 0, 0, time.Local)
		data := &reviewData{Year: 2026}
		daySeconds := map[string]float64{
			"2026-03-05": 600,
			"2026-03-06": 600,
			"2026-03-07": 600,
			"2026-03-09": 600,
		}

		srv.aggregateCheckin(data, daySeconds, now)

		if data.CheckinDays != 4 {
			t.Fatalf("打卡天数应为 4，实际 %d", data.CheckinDays)
		}
		if data.LongestStreak != 3 {
			t.Fatalf("最长连续应为 3，实际 %d", data.LongestStreak)
		}
		if data.CurrentStreak != 1 {
			t.Fatalf("今天未打卡不应中断，当前连续应为 1，实际 %d", data.CurrentStreak)
		}
	})

	t.Run("当前年：今天已打卡计入当前连续", func(t *testing.T) {
		// 打卡日 3/9、3/10（今天），当前连续为 2
		now := time.Date(2026, 3, 10, 21, 0, 0, 0, time.Local)
		data := &reviewData{Year: 2026}
		daySeconds := map[string]float64{
			"2026-03-09": 600,
			"2026-03-10": 600,
		}

		srv.aggregateCheckin(data, daySeconds, now)

		if data.CurrentStreak != 2 {
			t.Fatalf("今天已打卡时当前连续应为 2，实际 %d", data.CurrentStreak)
		}
		if data.LongestStreak != 2 {
			t.Fatalf("最长连续应为 2，实际 %d", data.LongestStreak)
		}
	})

	t.Run("当前年：昨天未打卡时当前连续为 0", func(t *testing.T) {
		// 仅 3/5 打卡，距今天 3/10 已断开
		now := time.Date(2026, 3, 10, 8, 0, 0, 0, time.Local)
		data := &reviewData{Year: 2026}
		daySeconds := map[string]float64{"2026-03-05": 600}

		srv.aggregateCheckin(data, daySeconds, now)

		if data.CurrentStreak != 0 {
			t.Fatalf("昨今均未打卡时当前连续应为 0，实际 %d", data.CurrentStreak)
		}
		if data.LongestStreak != 1 {
			t.Fatalf("最长连续应为 1，实际 %d", data.LongestStreak)
		}
	})

	t.Run("往年：当前连续恒为 0，最长连续仍正常计算", func(t *testing.T) {
		now := time.Date(2026, 3, 10, 8, 0, 0, 0, time.Local)
		data := &reviewData{Year: 2025}
		daySeconds := map[string]float64{
			"2025-12-29": 600,
			"2025-12-30": 600,
			"2025-12-31": 600,
		}

		srv.aggregateCheckin(data, daySeconds, now)

		if data.CurrentStreak != 0 {
			t.Fatalf("往年当前连续应为 0，实际 %d", data.CurrentStreak)
		}
		if data.LongestStreak != 3 {
			t.Fatalf("往年最长连续应为 3，实际 %d", data.LongestStreak)
		}
		if data.CheckinDays != 3 {
			t.Fatalf("打卡天数应为 3，实际 %d", data.CheckinDays)
		}
	})

	t.Run("不足 10 分钟的日子不打卡，不进连续统计", func(t *testing.T) {
		now := time.Date(2026, 3, 10, 8, 0, 0, 0, time.Local)
		data := &reviewData{Year: 2026}
		daySeconds := map[string]float64{
			"2026-03-09": 599, // 差 1 秒
			"2026-03-08": 300,
		}

		srv.aggregateCheckin(data, daySeconds, now)

		if data.CheckinDays != 0 || data.LongestStreak != 0 || data.CurrentStreak != 0 {
			t.Fatalf("不足阈值不应产生打卡，实际 days=%d longest=%d current=%d",
				data.CheckinDays, data.LongestStreak, data.CurrentStreak)
		}
		// 未打卡日格子仍渲染为 lv0
		cell := findHeatCell(t, data.HeatCells, "2026-03-09")
		if cell.Level != 0 || cell.Seconds != 599 {
			t.Fatalf("未打卡日应为 lv0 且保留秒数，实际 %+v", cell)
		}
	})
}

// findHeatCell 按日期取出真实格，找不到时终止测试。
func findHeatCell(t *testing.T, cells []heatCell, date string) heatCell {
	t.Helper()
	for _, c := range cells {
		if c.Date == date {
			return c
		}
	}
	t.Fatalf("热力图中未找到日期 %s", date)
	return heatCell{}
}
