package server

import (
	"html/template"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// tmplFuncs 提供模板里使用的格式化辅助函数。
var tmplFuncs = template.FuncMap{
	// humanSize 把字节数格式化为人类可读大小，供片库存储分析等页面使用。
	"humanSize": humanSize,
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
	// joinStrings 用分隔符连接字符串切片，供元信息列表展示。
	"joinStrings": func(items []string, sep string) string {
		return strings.Join(items, sep)
	},
	// indexURL 生成保持 type/view/sort/q/genre/tag 六元组正交的首页链接；
	// overrides 以 key/value 成对传入覆盖项，空值表示从 URL 中移除该参数。
	"indexURL": func(current map[string]string, overrides ...string) string {
		params := url.Values{}
		for k, v := range current {
			if v != "" {
				params.Set(k, v)
			}
		}
		for i := 0; i+1 < len(overrides); i += 2 {
			k, v := overrides[i], overrides[i+1]
			if v == "" {
				params.Del(k)
			} else {
				params.Set(k, v)
			}
		}
		if encoded := params.Encode(); encoded != "" {
			return "/?" + encoded
		}
		return "/"
	},
	// scrapeSource 把元信息来源标记转为中文展示文案。
	"scrapeSource": func(source string) string {
		switch source {
		case "nfo":
			return "NFO 刮削"
		case "tmdb":
			return "TMDB 在线刮削"
		case "nfo+tmdb":
			return "NFO + TMDB"
		default:
			return "本地文件"
		}
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
	// formatWatchHours 把累计观看秒数格式化为统计语义文案：
	// >=1 小时显示「X 小时 Y 分」，不足 1 小时显示「Y 分钟」，0 显示「0 分钟」。
	"formatWatchHours": func(sec float64) string {
		total := int(math.Round(sec))
		if total <= 0 {
			return "0 分钟"
		}
		h, m := total/3600, (total%3600)/60
		if h > 0 {
			if m > 0 {
				return itoa(h) + " 小时 " + itoa(m) + " 分"
			}
			return itoa(h) + " 小时"
		}
		if m == 0 {
			m = 1 // 不足 1 分钟但有零星秒数，避免显示 0 分钟
		}
		return itoa(m) + " 分钟"
	},
}

// humanSize 把字节数格式化为人类可读的文件大小（B/KB/MB/GB/TB，保留 1 位小数）。
func humanSize(n int64) string {
	if n < 1024 {
		return itoa(int(n)) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	val := float64(n) / 1024
	idx := 0
	for val >= 1024 && idx < len(units)-1 {
		val /= 1024
		idx++
	}
	return strconvFormatFloat(val, 1) + " " + units[idx]
}

// strconvFormatFloat 包装 strconv.FormatFloat，避免在模板辅助文件里扩散导入。
func strconvFormatFloat(f float64, precision int) string {
	return strconv.FormatFloat(f, 'f', precision, 64)
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
