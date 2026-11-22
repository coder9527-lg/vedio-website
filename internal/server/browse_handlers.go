package server

import (
	"net/http"
	"path"
	"strings"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// crumb 是面包屑的一个节点，Href 在 Go 侧拼好后交给模板自动转义；
// Current 为 true 的末节点渲染为纯文本而非链接。
type crumb struct {
	Name    string
	Href    string
	Current bool
}

// browsePageData 是目录浏览页的展示模型，目录树由 media 层纯内存聚合。
type browsePageData struct {
	basePage
	Path   string // 当前目录相对媒体根的路径，根目录为空
	IsRoot bool
	Crumbs []crumb // 首节点固定为「媒体库」
	Dirs   []media.BrowseDir
	Videos []*media.Video
}

// sanitizeBrowsePath 把 URL 中的目录片段白名单式规范化为媒体根内的相对路径。
// ServeMux 已做过一次 URL 清洗，这里是纵深防御：锚定根目录后所有 .. 与多余斜杠都会被吃掉。
// 返回 ok=false 表示路径含非法成分（反斜杠或逃逸媒体根），调用方应响应 404。
func sanitizeBrowsePath(raw string) (rel string, ok bool) {
	if strings.Contains(raw, `\`) {
		return "", false
	}
	c := strings.TrimPrefix(path.Clean("/"+raw), "/")
	if c == "" || c == "." {
		return "", true
	}
	if c == ".." || strings.HasPrefix(c, ".."+"/") {
		return "", false
	}
	return c, true
}

// browseCrumbs 由相对路径生成面包屑，首节点固定指向 /browse，末节点由模板渲染为纯文本。
func browseCrumbs(rel string) []crumb {
	crumbs := []crumb{{Name: "媒体库", Href: "/browse"}}
	if rel == "" {
		// 根目录只有一个节点，它本身就是末节点
		crumbs[0].Current = true
		return crumbs
	}
	segs := strings.Split(rel, "/")
	acc := ""
	for i, seg := range segs {
		acc = path.Join(acc, seg)
		crumbs = append(crumbs, crumb{
			Name:    seg,
			Href:    "/browse/" + acc,
			Current: i == len(segs)-1,
		})
	}
	return crumbs
}

// handleBrowse 渲染目录浏览页：/browse 为媒体根，/browse/{rest...} 逐级进入子目录。
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	rel, ok := sanitizeBrowsePath(r.PathValue("rest"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	view := s.library.Browse(rel)
	if !view.Exists {
		http.NotFound(w, r)
		return
	}
	s.render(w, "browse.html", browsePageData{
		basePage: basePage{Title: "目录浏览", AuthEnabled: s.authEnabled()},
		Path:     view.Path,
		IsRoot:   view.Path == "",
		Crumbs:   browseCrumbs(view.Path),
		Dirs:     view.Dirs,
		Videos:   view.Videos,
	})
}
