package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/coder9527-lg/vedio-website/internal/store"
)

// dataHealthSections 定义体检页七个分区的固定顺序、中文标题与说明。
// Key 同时是 store 白名单逻辑表名与前端按钮的 data-table 入参，三处共用同一标识。
var dataHealthSections = []struct {
	Key   string
	Title string
	Hint  string
}{
	{"progress", "播放进度", "续播位置与观看累计"},
	{"favorites", "收藏", ""},
	{"playlist_item", "播放列表条目", "只删除悬空条目，播放列表本身保留"},
	{"bookmarks", "书签", "时间戳笔记"},
	{"skip_settings", "片头片尾设置", ""},
	{"watch_events", "观看历史", "历史播放会话流水"},
	{"video_tag", "标签关联", "清理后自动回收不再被引用的空标签"},
}

// dataHealthSectionView 是一个表分区的展示模型。
type dataHealthSectionView struct {
	Key       string
	Title     string
	Hint      string
	Count     int64
	SampleIDs []string
}

// dataHealthPage 是数据健康检查页的模板数据。
type dataHealthPage struct {
	basePage
	LibraryTotal int
	TotalOrphans int64
	Sections     []dataHealthSectionView
	LibraryEmpty bool
}

// existingVideoIDs 汇总当前媒体库全部视频 ID，并返回视频总数。
// 与 handleRescan 调 Store.Prune 时的口径完全一致。
func (s *Server) existingVideoIDs() (map[string]bool, int) {
	videos := s.library.List()
	existing := make(map[string]bool, len(videos))
	for _, v := range videos {
		existing[v.ID] = true
	}
	return existing, len(videos)
}

// reportCount 按分区 Key 读取体检报告中的对应计数；Key 全部来自硬编码白名单，
// 未命中只可能是编程错误，直接返回 0。
func reportCount(report store.OrphanReport, key string) int64 {
	switch key {
	case "progress":
		return report.Progress
	case "favorites":
		return report.Favorites
	case "playlist_item":
		return report.PlaylistItem
	case "bookmarks":
		return report.Bookmarks
	case "skip_settings":
		return report.SkipSettings
	case "watch_events":
		return report.WatchEvents
	case "video_tag":
		return report.VideoTag
	default:
		return 0
	}
}

// handleDataHealthPage 渲染数据健康检查页：按七张表分区展示孤儿记录数与悬空 video_id 样例。
func (s *Server) handleDataHealthPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	existing, total := s.existingVideoIDs()
	report, err := s.store.OrphanReport(existing)
	if err != nil {
		log.Printf("数据健康体检失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "数据健康体检失败"})
		return
	}

	sections := make([]dataHealthSectionView, 0, len(dataHealthSections))
	for _, meta := range dataHealthSections {
		ids, err := s.store.OrphanSampleIDs(meta.Key, existing, 5)
		if err != nil {
			log.Printf("查询 %s 孤儿样例失败: %v", meta.Key, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "数据健康体检失败"})
			return
		}
		sections = append(sections, dataHealthSectionView{
			Key:       meta.Key,
			Title:     meta.Title,
			Hint:      meta.Hint,
			Count:     reportCount(report, meta.Key),
			SampleIDs: ids,
		})
	}

	s.render(w, "data_health.html", dataHealthPage{
		basePage:     basePage{Title: "数据健康检查", AuthEnabled: s.authEnabled()},
		LibraryTotal: total,
		TotalOrphans: report.Total(),
		Sections:     sections,
		LibraryEmpty: total == 0,
	})
}

// dataHealthCleanRequest 是按表清理接口的请求体。
type dataHealthCleanRequest struct {
	Table string `json:"table"`
}

// handleDataHealthClean 清理单张表的孤儿记录，body 形如 {"table":"progress"}。
func (s *Server) handleDataHealthClean(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	var req dataHealthCleanRequest
	// 请求体很小（单字段），限 16KB 并拒绝未知字段，挡住异常 body 与拼错字段
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	// store 层白名单会再校验一次，这里提前拦截给出明确的 400 语义
	if !store.IsOrphanTable(req.Table) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不支持的数据表"})
		return
	}
	existing, _ := s.existingVideoIDs()
	removed, err := s.store.DeleteOrphansByTable(req.Table, existing)
	if err != nil {
		log.Printf("按表清理孤儿记录失败 table=%s: %v", req.Table, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "清理失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"table": req.Table, "removed": removed})
}

// handleDataHealthCleanAll 在单事务内一键清理全部七张表的孤儿记录。
func (s *Server) handleDataHealthCleanAll(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	existing, _ := s.existingVideoIDs()
	report, err := s.store.DeleteAllOrphans(existing)
	if err != nil {
		log.Printf("一键清理孤儿记录失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "清理失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"removed": report.Total(),
		"detail": map[string]int64{
			"progress":      report.Progress,
			"favorites":     report.Favorites,
			"playlist_item": report.PlaylistItem,
			"bookmarks":     report.Bookmarks,
			"skip_settings": report.SkipSettings,
			"watch_events":  report.WatchEvents,
			"video_tag":     report.VideoTag,
		},
	})
}
