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
