package server

import (
	"html/template"
	"math"
)

// tmplFuncs 提供模板里使用的格式化辅助函数。
var tmplFuncs = template.FuncMap{
	// formatDuration 把秒数格式化为 h:m:s（或 m:s），0 显示为 "-"。
	"formatDuration": func(sec float64) string {
		if sec <= 0 {
			return "-"
		}
		total := int(math.Round(sec))
		h, total := total/3600, total%3600
		m, s := total/60, total%60
		if h > 0 {
			return pad2(h) + ":" + pad2(m) + ":" + pad2(s)
		}
		return pad2(m) + ":" + pad2(s)
	},
	// progressPercent 计算播放进度百分比，时长未知时返回 0。
	"progressPercent": func(position, duration float64) int {
		if duration <= 0 || position <= 0 {
			return 0
		}
		pct := int(position / duration * 100)
		if pct > 100 {
			pct = 100
		}
		return pct
	},
	// hasProgress 判断进度记录是否有效（位置大于 5 秒，避免片头抖动）。
	"hasProgress": func(position, duration float64) bool {
		if position < 5 {
			return false
		}
		// 已看到片尾 95% 以上视为看完，不再提示续播
		if duration > 0 && position/duration >= 0.95 {
			return false
		}
		return true
	},
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
