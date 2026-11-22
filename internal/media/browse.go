package media

import (
	"sort"
	"strings"
)

// BrowseDir 是目录浏览中的一个直接子目录，Count/Size 为整棵子树的递归聚合值。
type BrowseDir struct {
	Name  string // 单段目录名
	Path  string // 相对媒体根的完整路径（斜杠分隔），供拼 /browse/{Path}
	Count int    // 子树内视频文件总数（递归）
	Size  int64  // 子树内视频字节总数（递归）
}

// BrowseView 是单个目录的一次浏览快照，目录树完全由视频的 RelPath 反推。
type BrowseView struct {
	Path   string // 规范化后的相对路径，根目录为 ""
	Dirs   []BrowseDir
	Videos []*Video // 该目录下的直接视频
	Exists bool     // 根目录恒 true（含空库）；非根路径无任何视频归属时 false
}

// Browse 返回 rel 目录的浏览视图：直接子目录（递归聚合视频数与大小）与直接视频。
// rel 为空表示媒体根目录；RelPath 统一使用斜杠分隔，故这里只用 strings/sort，不碰 filepath。
// 索引只含视频文件，物理空目录或仅含非视频文件的目录不会出现在结果中。
func (l *Library) Browse(rel string) BrowseView {
	l.mu.RLock()
	defer l.mu.RUnlock()

	view := BrowseView{
		Path:   rel,
		Dirs:   make([]BrowseDir, 0),
		Videos: make([]*Video, 0),
	}
	// 根目录恒存在（即使空库）；非根路径是否存在由是否有视频归属决定。
	if rel == "" {
		view.Exists = true
	}

	// 前缀为空时 TrimPrefix 原样返回字符串，根散文件的 remainder 即其自身 RelPath。
	prefix := ""
	if rel != "" {
		prefix = rel + "/"
	}
	dirMap := make(map[string]*BrowseDir)

	for _, v := range l.videos {
		// 跳过不属于该目录子树的视频：既不是目录本身（目录无视频条目），也不在其下一级。
		if rel != "" && v.RelPath != rel && !strings.HasPrefix(v.RelPath, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(v.RelPath, prefix)
		if i := strings.IndexByte(remainder, '/'); i >= 0 {
			// remainder 仍含分隔符：归入第一个直接子目录并递归聚合。
			name := remainder[:i]
			dir, ok := dirMap[name]
			if !ok {
				dir = &BrowseDir{Name: name, Path: prefix + name}
				dirMap[name] = dir
			}
			dir.Count++
			dir.Size += v.Size
		} else {
			// 无分隔符：视频直接位于当前目录。
			view.Videos = append(view.Videos, v)
		}
	}

	for _, d := range dirMap {
		view.Dirs = append(view.Dirs, *d)
	}
	// map 遍历无序，显式排序保证输出确定：目录按名称、视频按相对路径。
	sort.Slice(view.Dirs, func(i, j int) bool { return view.Dirs[i].Name < view.Dirs[j].Name })
	sort.Slice(view.Videos, func(i, j int) bool { return view.Videos[i].RelPath < view.Videos[j].RelPath })

	if rel != "" {
		view.Exists = len(view.Dirs)+len(view.Videos) > 0
	}
	return view
}
