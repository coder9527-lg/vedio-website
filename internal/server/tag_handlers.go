package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/store"
)

// tagRequest 是打标请求体，name 为标签名（服务端 trim 并校验长度）。
type tagRequest struct {
	Name string `json:"name"`
}

// handleTagList 返回全库标签及各自引用视频数，供首页 chips 与播放页自动补全。
func (s *Server) handleTagList(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	tags, err := s.store.ListTags()
	if err != nil {
		log.Printf("查询标签列表失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询标签失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// handleVideoTagList 返回单个视频的标签。
func (s *Server) handleVideoTagList(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	tags, err := s.store.VideoTags(id)
	if err != nil {
		log.Printf("查询视频标签失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询标签失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// handleVideoTagSet 给视频打标签：标签自动创建，重复打标幂等。
// 新建关联返回 201，已存在返回 200。
func (s *Server) handleVideoTagSet(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取请求体失败"})
		return
	}
	var req tagRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "标签名不能为空"})
		return
	}
	tag, created, err := s.store.SetVideoTag(id, name)
	if err != nil {
		// 归一化错误（空名/超长）属客户端问题，返回 400；其余按 500 处理
		if errors.Is(err, store.ErrInvalidTag) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("打标签失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "打标签失败"})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"tag": tag, "created": created})
}

// handleVideoTagRemove 取消视频的某个标签（取消后空标签由 store 自动回收），幂等返回 204。
func (s *Server) handleVideoTagRemove(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.library.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
		return
	}
	tagID, err := strconv.ParseInt(r.PathValue("tagId"), 10, 64)
	if err != nil || tagID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "标签 ID 非法"})
		return
	}
	if err := s.store.RemoveVideoTag(id, tagID); err != nil {
		log.Printf("取消标签失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "取消标签失败"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// batchItem 标识批量操作的一个对象：type 取 video 或 show，id 为对应 ID。
type batchItem struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// batchSetRequest 是批量打标请求体：给 items 展开后的全部视频打上 name 标签。
type batchSetRequest struct {
	Name  string      `json:"name"`
	Items []batchItem `json:"items"`
}

// batchRemoveRequest 是批量取消标签请求体：从 items 展开后的全部视频上取消 tagId 标签。
type batchRemoveRequest struct {
	TagID int64       `json:"tagId"`
	Items []batchItem `json:"items"`
}

// expandBatchItems 把 items 展开为去重后的视频 ID：show 类型展开为该剧全部单集，
// video 类型直接收集，未知类型与空 ID 忽略。
func (s *Server) expandBatchItems(items []batchItem) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, it := range items {
		switch it.Type {
		case "video":
			add(it.ID)
		case "show":
			for vid := range s.library.ShowVideos(it.ID) {
				add(vid)
			}
		}
	}
	return out
}

// filterExisting 过滤出库中仍存在的视频 ID，返回有效列表与缺失数量。
// 缺失 ID 不计入 store 调用，由 handler 并入 skipped。
func (s *Server) filterExisting(ids []string) (valid []string, missing int) {
	for _, id := range ids {
		if _, ok := s.library.Get(id); ok {
			valid = append(valid, id)
		} else {
			missing++
		}
	}
	return valid, missing
}

// handleTagRename 全局重命名标签。重名（大小写不敏感）返回 409，标签不存在返回 404。
func (s *Server) handleTagRename(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	tagID, err := strconv.ParseInt(r.PathValue("tagId"), 10, 64)
	if err != nil || tagID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "标签 ID 非法"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取请求体失败"})
		return
	}
	var req tagRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	tag, err := s.store.RenameTag(tagID, req.Name)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrTagConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		case errors.Is(err, store.ErrInvalidTag):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, store.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "标签不存在"})
		default:
			log.Printf("重命名标签失败: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "重命名标签失败"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tag": tag})
}

// handleTagDelete 全局删除标签及其全部视频关联，成功返回 204。
func (s *Server) handleTagDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	tagID, err := strconv.ParseInt(r.PathValue("tagId"), 10, 64)
	if err != nil || tagID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "标签 ID 非法"})
		return
	}
	if err := s.store.DeleteTag(tagID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "标签不存在"})
			return
		}
		log.Printf("删除标签失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "删除标签失败"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBatchTagSet 给一批视频（含剧集展开成员）批量打同一标签。
// 新增了任意关联返回 201，全部幂等跳过或目标缺失返回 200。
func (s *Server) handleBatchTagSet(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取请求体失败"})
		return
	}
	var req batchSetRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "标签名不能为空"})
		return
	}
	expanded := s.expandBatchItems(req.Items)
	if len(expanded) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有可操作的视频"})
		return
	}
	valid, missing := s.filterExisting(expanded)
	// 全部目标缺失：短路返回零计数，不创建悬空标签，也不做 404
	if len(valid) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"added": 0, "skipped": len(expanded), "total": len(expanded), "tag": nil,
		})
		return
	}
	added, storeSkipped, _, err := s.store.BatchSetVideoTags(req.Name, valid)
	if err != nil {
		if errors.Is(err, store.ErrInvalidTag) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("批量打标失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "批量打标失败"})
		return
	}
	tag, err := s.store.TagByName(req.Name)
	if err != nil {
		log.Printf("回查标签失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "批量打标失败"})
		return
	}
	status := http.StatusOK
	if added > 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{
		"added":   added,
		"skipped": missing + storeSkipped,
		"total":   len(expanded),
		"tag":     tag,
	})
}

// handleBatchTagRemove 从一批视频上批量取消某标签，标签不存在返回 404。
func (s *Server) handleBatchTagRemove(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取请求体失败"})
		return
	}
	var req batchRemoveRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	if req.TagID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "标签 ID 非法"})
		return
	}
	expanded := s.expandBatchItems(req.Items)
	if len(expanded) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有可操作的视频"})
		return
	}
	valid, missing := s.filterExisting(expanded)
	// store 先校验标签存在性再处理空列表，故 valid 为空时仍能正确返回 404
	removed, storeSkipped, _, err := s.store.BatchRemoveVideoTags(req.TagID, valid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "标签不存在"})
			return
		}
		log.Printf("批量取消标签失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "批量取消标签失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"removed": removed,
		"skipped": missing + storeSkipped,
		"total":   len(expanded),
	})
}
