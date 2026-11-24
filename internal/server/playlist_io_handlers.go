package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// 播放列表导入导出的体积与数量上限。
const (
	maxPlaylistImport   = 10 << 20 // 单个导入文件 10 MiB
	maxImportPlaylists  = 200      // 单次导入的播放列表数量上限
	maxImportNameLen    = 100      // 列表名长度上限，与 store.normalizePlaylistName 口径一致
	playlistFileVersion = 1        // 导出文件格式版本
)

// ---------- 导出文件契约 ----------

// playlistExportFile 是播放列表导出 JSON 的顶层结构。
type playlistExportFile struct {
	Version    int                `json:"version"`
	ExportedAt time.Time          `json:"exportedAt"`
	Playlists  []exportedPlaylist `json:"playlists"`
}

// exportedPlaylist 是导出文件中的单个播放列表（仅名称与有序条目，不含 ID）。
type exportedPlaylist struct {
	Name  string         `json:"name"`
	Items []exportedItem `json:"items"`
}

// exportedItem 是导出条目的视频定位信息：videoId 为主键，relPath 作跨实例兜底。
type exportedItem struct {
	VideoID   string `json:"videoId"`
	RelPath   string `json:"relPath"`
	SortOrder int    `json:"sortOrder"`
}

// ---------- 导入响应 ----------

// playlistImportResult 汇总一次导入的处理结果。
type playlistImportResult struct {
	Created       int                `json:"created"`       // 新建列表数
	AddedItems    int                `json:"addedItems"`    // 实际写入的条目总数
	SkippedItems  int                `json:"skippedItems"`  // 本机媒体库缺失被跳过的条目数
	Playlists     []importedListView `json:"playlists"`     // 每个新列表的明细
	SkippedVideos []skippedVideoView `json:"skippedVideos"` // 缺失视频明细（序列化为 [] 而非 null）
}

// importedListView 是单个新列表的导入结果。
type importedListView struct {
	Name       string `json:"name"`
	ID         string `json:"id"`
	AddedItems int    `json:"addedItems"`
}

// skippedVideoView 记录一条被跳过的缺失视频，便于用户人工核对。
type skippedVideoView struct {
	PlaylistName string `json:"playlistName"`
	VideoID      string `json:"videoId"`
	RelPath      string `json:"relPath"`
}

// handleExportPlaylists 导出全部播放列表为单个 JSON 文件。
// 仅在启用持久化时可用；空库也返回 200（playlists 为空数组），导出空表是合法结果。
func (s *Server) handleExportPlaylists(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	summaries, err := s.store.ListPlaylists()
	if err != nil {
		log.Printf("查询播放列表摘要用于导出失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "导出失败"})
		return
	}

	out := playlistExportFile{
		Version:    playlistFileVersion,
		ExportedAt: time.Now().UTC(),
		Playlists:  make([]exportedPlaylist, 0, len(summaries)),
	}
	for _, sm := range summaries {
		pl, err := s.store.GetPlaylist(sm.ID)
		if err != nil {
			log.Printf("查询播放列表 %s 详情用于导出失败: %v", sm.ID, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "导出失败"})
			return
		}
		if pl == nil {
			continue // 列表在两次查询间被删除，跳过即可
		}
		ep := exportedPlaylist{Name: pl.Name, Items: make([]exportedItem, 0, len(pl.Items))}
		for _, it := range pl.Items {
			relPath := ""
			// 悬空条目（视频已从媒体库删除）仍保留记录，relPath 留空，保证备份不丢数据
			if v, ok := s.library.Get(it.VideoID); ok {
				relPath = v.RelPath
			}
			ep.Items = append(ep.Items, exportedItem{
				VideoID:   it.VideoID,
				RelPath:   relPath,
				SortOrder: it.SortOrder,
			})
		}
		out.Playlists = append(out.Playlists, ep)
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		log.Printf("序列化播放列表导出文件失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "导出失败"})
		return
	}
	// 文件名只含 ASCII，时间用 UTC，规避 RFC 5987 编码分支
	fileName := "playlists-" + time.Now().UTC().Format("20060102-150405") + ".json"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fileName+`"`)
	if _, err := w.Write(data); err != nil {
		log.Printf("写入播放列表导出响应失败: %v", err)
	}
}

// handleImportPlaylists 接收 multipart 上传的播放列表 JSON，全部以新建方式导入，
// 不修改或覆盖任何现有列表；本机媒体库缺失的条目跳过并在响应中报告。
func (s *Server) handleImportPlaylists(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	// maxMemory 仅决定内存/临时文件分界，体积上限在读取后二次校验
	if err := r.ParseMultipartForm(maxPlaylistImport); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无法解析上传表单"})
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少导入文件"})
		return
	}
	defer file.Close()

	// 多读 1 字节用于判断是否超限，避免信任客户端声明的大小
	data, err := io.ReadAll(io.LimitReader(file, maxPlaylistImport+1))
	if err != nil {
		log.Printf("读取播放列表导入文件失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "读取导入文件失败"})
		return
	}
	if len(data) > maxPlaylistImport {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "导入文件不能超过 10 MiB"})
		return
	}
	if len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "导入文件为空"})
		return
	}

	var doc playlistExportFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // 拒绝未知字段，及早发现格式不匹配
	if err := dec.Decode(&doc); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "导入文件不是有效的播放列表 JSON"})
		return
	}
	if dec.More() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "导入文件不是有效的播放列表 JSON"})
		return
	}

	// 整体校验全部在写库前完成，杜绝半成品写入
	if doc.Version != playlistFileVersion {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不支持的导入文件版本"})
		return
	}
	if len(doc.Playlists) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "导入文件中没有播放列表"})
		return
	}
	if len(doc.Playlists) > maxImportPlaylists {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "单次最多导入 200 个播放列表"})
		return
	}
	for _, pl := range doc.Playlists {
		name := strings.TrimSpace(pl.Name)
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "存在名称为空的播放列表"})
			return
		}
		if len(name) > maxImportNameLen {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "存在名称超过 100 个字符的播放列表"})
			return
		}
	}

	result := playlistImportResult{
		Playlists:     make([]importedListView, 0, len(doc.Playlists)),
		SkippedVideos: []skippedVideoView{},
	}
	// relPath -> videoID 兜底索引惰性构建：仅当出现 videoID 未命中时才扫描一次媒体库
	var relPathIndex map[string]string
	ensureRelPathIndex := func() map[string]string {
		if relPathIndex != nil {
			return relPathIndex
		}
		relPathIndex = make(map[string]string)
		for _, v := range s.library.List() {
			if v.RelPath != "" {
				relPathIndex[v.RelPath] = v.ID
			}
		}
		return relPathIndex
	}
	// resolveVideoID 先按主键 videoID 直查，未命中再用 relPath 兜底；都失败返回空串。
	resolveVideoID := func(item exportedItem) string {
		if item.VideoID != "" {
			if _, ok := s.library.Get(item.VideoID); ok {
				return item.VideoID
			}
		}
		if item.RelPath != "" {
			if id, ok := ensureRelPathIndex()[item.RelPath]; ok {
				return id
			}
		}
		return ""
	}

	for _, pl := range doc.Playlists {
		name := strings.TrimSpace(pl.Name)
		created, err := s.store.CreatePlaylist(name)
		if err != nil {
			log.Printf("导入时创建播放列表 %q 失败: %v", name, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "导入失败"})
			return
		}
		// 保持导出条目原序收集可解析的 videoID；重复 ID 交给 AddPlaylistItems 内部去重
		videoIDs := make([]string, 0, len(pl.Items))
		for _, item := range pl.Items {
			id := resolveVideoID(item)
			if id == "" {
				result.SkippedItems++
				result.SkippedVideos = append(result.SkippedVideos, skippedVideoView{
					PlaylistName: name,
					VideoID:      item.VideoID,
					RelPath:      item.RelPath,
				})
				continue
			}
			videoIDs = append(videoIDs, id)
		}
		added, err := s.store.AddPlaylistItems(created.ID, videoIDs)
		if err != nil {
			log.Printf("导入时向播放列表 %s 写入条目失败: %v", created.ID, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "导入失败"})
			return
		}
		result.Created++
		result.AddedItems += added
		result.Playlists = append(result.Playlists, importedListView{
			Name:       name,
			ID:         created.ID,
			AddedItems: added,
		})
	}
	writeJSON(w, http.StatusOK, result)
}
