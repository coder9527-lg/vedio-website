package server

import (
	"encoding/csv"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/store"
)

// csvHeader 是进度汇总导出的固定 9 列表头，顺序与下方逐行写入严格对应。
var csvHeader = []string{
	"视频名称", "视频ID", "当前进度(秒)", "总时长(秒)", "观看次数",
	"累计观看(秒)", "是否看完", "进度百分比", "更新时间(UTC)",
}

// handleExportProgress 导出全量播放进度为带 UTF-8 BOM 的 CSV 文件。
// 仅在启用持久化时可用（requireStore 守卫，未启用返回 503 JSON）；
// 空库也返回 200，仅含 BOM 与表头行——导出空表是合法结果。
func (s *Server) handleExportProgress(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	rows, err := s.store.AllProgress()
	if err != nil {
		log.Printf("查询全量进度用于 CSV 导出失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "导出失败"})
		return
	}

	// 文件名只含 ASCII，时间用 UTC（与库内 updated_at 口径一致），规避 RFC 5987 编码分支
	fileName := "watch-progress-" + time.Now().UTC().Format("20060102-150405") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fileName+`"`)

	// UTF-8 BOM：保证 Excel 双击打开中文表头与视频名不乱码
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return
	}
	for _, p := range rows {
		if err := cw.Write(csvRecord(p, s.videoDisplayName(p.VideoID))); err != nil {
			return
		}
	}
	cw.Flush()
	// 响应头与部分正文可能已发出，Flush 后出错只能记录日志、无法再改状态码
	if err := cw.Error(); err != nil {
		log.Printf("写入进度 CSV 失败: %v", err)
	}
}

// videoDisplayName 取视频展示名；视频已从媒体库删除（悬空 ID）时回退为 ID，
// 保证备份导出不丢记录。
func (s *Server) videoDisplayName(videoID string) string {
	if v, ok := s.library.Get(videoID); ok {
		return v.Name
	}
	return videoID
}

// csvRecord 把一条进度记录按 csvHeader 顺序转为 9 个字符串字段。
// 秒数统一四舍五入为整数；时长未知（0）时百分比留空，避免除零与伪精度。
func csvRecord(p store.Progress, name string) []string {
	completed := "否"
	if p.Completed {
		completed = "是"
	}
	percent := ""
	if p.Duration > 0 {
		v := math.Round(p.Position / p.Duration * 100)
		if v > 100 {
			v = 100 // 进度上报可能略超总时长，展示封顶 100%
		}
		percent = strconv.Itoa(int(v))
	}
	return []string{
		name,
		p.VideoID,
		strconv.Itoa(int(math.Round(p.Position))),
		strconv.Itoa(int(math.Round(p.Duration))),
		strconv.FormatInt(p.PlayCount, 10),
		strconv.Itoa(int(math.Round(p.WatchTimeSeconds))),
		completed,
		percent,
		p.UpdatedAt.UTC().Format("2006-01-02 15:04:05"),
	}
}
