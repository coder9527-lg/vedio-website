package server

import (
	"net/http"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// personData 是演员/导演聚合页的视图模型：同一人物的影片按角色分组展示。
type personData struct {
	basePage
	Name       string
	Directed   []*media.Video // 该人物担任导演的影片（按相对路径排序）
	ActedIn    []*media.Video // 该人物参演的影片（按相对路径排序）
	Metadata   map[string]*media.Metadata
	TotalCount int // 去重后的影片总数（同一影片可能同时出现在两个分组）
}

// handlePerson 聚合全库元信息中匹配指定人物（演员或导演）的影片。
// 名字来自路径变量，按去空白后的精确匹配（大小写不敏感）；无任何匹配返回 404。
func (s *Server) handlePerson(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	target := strings.ToLower(name)

	data := personData{
		basePage: basePage{Title: name, AuthEnabled: s.authEnabled()},
		Name:     name,
		Metadata: map[string]*media.Metadata{},
	}
	seen := map[string]bool{}

	for _, v := range s.library.List() {
		md := s.library.Metadata(v.ID)
		if md == nil {
			continue
		}
		isDirector := containsName(md.Directors, target)
		isActor := containsName(md.Actors, target)
		if !isDirector && !isActor {
			continue
		}
		if isDirector {
			data.Directed = append(data.Directed, v)
		}
		if isActor {
			data.ActedIn = append(data.ActedIn, v)
		}
		data.Metadata[v.ID] = md
		if !seen[v.ID] {
			seen[v.ID] = true
			data.TotalCount++
		}
	}

	if data.TotalCount == 0 {
		http.NotFound(w, r)
		return
	}
	s.render(w, "person.html", data)
}

// containsName 判断名字切片中是否存在目标（调用方须已把目标转小写并去空白）。
func containsName(names []string, targetLower string) bool {
	for _, n := range names {
		if strings.ToLower(strings.TrimSpace(n)) == targetLower {
			return true
		}
	}
	return false
}
