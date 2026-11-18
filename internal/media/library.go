package media

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultExts 是默认识别的视频文件扩展名。
var DefaultExts = map[string]bool{
	".mp4": true, ".m4v": true, ".mkv": true, ".mov": true,
	".avi": true, ".webm": true, ".ts": true, ".flv": true,
	".wmv": true, ".mpg": true, ".mpeg": true, ".3gp": true, ".rmvb": true,
}

// Video 表示一个被索引的视频文件。
type Video struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	RelPath string    `json:"relPath"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	Ext     string    `json:"ext"`
	absPath string
}

// AbsPath 返回视频在容器/主机上的绝对路径，供流式传输与缩略图使用。
func (v *Video) AbsPath() string { return v.absPath }

// Library 是视频文件的内存索引，线程安全。
type Library struct {
	root string
	exts map[string]bool

	mu       sync.RWMutex
	videos   map[string]*Video
	subs     map[string][]*Subtitle // 视频 ID → 外挂字幕列表
	metadata map[string]*Metadata   // 视频 ID → 刮削元信息（NFO/本地海报，可能为 nil）

	// scraperImageDir 是在线刮削图片与 metadata.json 的缓存目录，非空时扫描会恢复历史刮削结果
	scraperImageDir string
}

func NewLibrary(root string, exts map[string]bool) *Library {
	return &Library{
		root:     root,
		exts:     exts,
		videos:   map[string]*Video{},
		subs:     map[string][]*Subtitle{},
		metadata: map[string]*Metadata{},
	}
}

// Scan 递归扫描媒体目录并重建索引，跳过无权限访问的条目。
func (l *Library) Scan() (int, error) {
	found := make(map[string]*Video)

	err := filepath.WalkDir(l.root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// 单个条目无权限时不中断整体扫描
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !l.exts[ext] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(l.root, path)
		if err != nil {
			return nil
		}
		id := makeID(rel)
		found[id] = &Video{
			ID:      id,
			Name:    strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())),
			RelPath: filepath.ToSlash(rel),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Ext:     ext,
			absPath: path,
		}
		return nil
	})

	// 全量重建字幕与元信息索引：按视频逐个发现同目录外挂字幕、NFO 与海报
	subs := make(map[string][]*Subtitle, len(found))
	meta := make(map[string]*Metadata, len(found))
	for id, v := range found {
		if s := findSubtitles(l.root, v); len(s) > 0 {
			subs[id] = s
		}
		local := loadMetadata(l.root, v)
		// 在线刮削历史结果从缓存目录恢复，并与本地 NFO 重新合并（NFO 优先）
		if saved := l.loadPersistedMetadata(id); saved != nil {
			merged := mergeMetadata(local, saved)
			if local != nil {
				// 本地新增了 NFO：来源升级为混合；否则保留持久化时的来源标记
				merged.Source = mergedSource(local)
			}
			meta[id] = merged
		} else if local != nil {
			meta[id] = local
		}
	}

	l.mu.Lock()
	l.videos = found
	l.subs = subs
	l.metadata = meta
	l.mu.Unlock()
	return len(found), err
}

// Query 控制列表查询的过滤与排序。
// Sort 支持: "name"（按名称）、"newest"（修改时间倒序）、"oldest"（修改时间正序）、"size"（体积倒序），默认按路径。
type Query struct {
	Keyword string
	Sort    string
	IDs     map[string]bool // 非空时只返回这些 ID（用于收藏/继续观看等视图）
	Limit   int             // <= 0 表示不限制
}

// List 返回按相对路径排序的全部视频。
func (l *Library) List() []*Video {
	return l.Query(Query{})
}

// Query 按条件过滤并排序视频，零值 Query 等价于 List。
func (l *Library) Query(q Query) []*Video {
	l.mu.RLock()
	defer l.mu.RUnlock()

	keyword := strings.ToLower(strings.TrimSpace(q.Keyword))
	out := make([]*Video, 0, len(l.videos))
	for _, v := range l.videos {
		if len(q.IDs) > 0 && !q.IDs[v.ID] {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(v.Name), keyword) &&
			!strings.Contains(strings.ToLower(v.RelPath), keyword) {
			continue
		}
		out = append(out, v)
	}

	less := func(i, j int) bool { return out[i].RelPath < out[j].RelPath }
	switch q.Sort {
	case "name":
		less = func(i, j int) bool {
			if out[i].Name == out[j].Name {
				return out[i].RelPath < out[j].RelPath
			}
			return out[i].Name < out[j].Name
		}
	case "newest":
		less = func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) }
	case "oldest":
		less = func(i, j int) bool { return out[i].ModTime.Before(out[j].ModTime) }
	case "size":
		less = func(i, j int) bool {
			if out[i].Size == out[j].Size {
				return out[i].RelPath < out[j].RelPath
			}
			return out[i].Size > out[j].Size
		}
	}
	sort.Slice(out, less)

	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out
}

// Metadata 返回视频的刮削元信息；没有任何 NFO/海报时返回 nil。
func (l *Library) Metadata(id string) *Metadata {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.metadata[id]
}

// loadPersistedMetadata 从刮削缓存目录读取历史在线刮削结果；
// 未配置缓存目录、文件缺失或解析失败时返回 nil（静默降级，不影响扫描）。
func (l *Library) loadPersistedMetadata(videoID string) *Metadata {
	if l.scraperImageDir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(l.scraperImageDir, videoID, "metadata.json"))
	if err != nil {
		return nil
	}
	var md Metadata
	if err := json.Unmarshal(raw, &md); err != nil {
		return nil
	}
	if md.empty() {
		return nil
	}
	return &md
}

// SetMetadata 直接写入视频的刮削元信息（在线刮削合并后回写）；
// md 为 nil 或全空时删除该视频的元信息记录。
func (l *Library) SetMetadata(videoID string, md *Metadata) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if md == nil || md.empty() {
		delete(l.metadata, videoID)
		return
	}
	l.metadata[videoID] = md
}

// RefreshMetadata 重新为单个视频加载 NFO 与本地海报/背景图
// （NFO 或图片独立增删时由 Watcher 调用）。
func (l *Library) RefreshMetadata(videoID string) {
	l.mu.RLock()
	v, ok := l.videos[videoID]
	l.mu.RUnlock()
	if !ok {
		return
	}
	md := loadMetadata(l.root, v)
	l.mu.Lock()
	if md != nil {
		l.metadata[videoID] = md
	} else {
		delete(l.metadata, videoID)
	}
	l.mu.Unlock()
}

// Get 按 ID 查找视频。
func (l *Library) Get(id string) (*Video, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	v, ok := l.videos[id]
	return v, ok
}

// Root 返回媒体库根目录的绝对路径。
func (l *Library) Root() string { return l.root }

// SetScraperImageDir 登记在线刮削缓存目录，使重新扫描时能恢复历史刮削结果。
func (l *Library) SetScraperImageDir(dir string) { l.scraperImageDir = dir }

// IsVideoExt 判断扩展名是否为受支持的视频。
func (l *Library) IsVideoExt(name string) bool {
	return l.exts[strings.ToLower(filepath.Ext(name))]
}

// Subtitles 返回某个视频的外挂字幕列表（无字幕时返回 nil）。
func (l *Library) Subtitles(videoID string) []*Subtitle {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := l.subs[videoID]
	// 返回拷贝，避免调用方修改内部切片
	if len(out) == 0 {
		return nil
	}
	cp := make([]*Subtitle, len(out))
	copy(cp, out)
	return cp
}

// GetSubtitle 按视频 ID 与字幕 ID 查找字幕。
func (l *Library) GetSubtitle(videoID, subID string) (*Subtitle, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, s := range l.subs[videoID] {
		if s.ID == subID {
			return s, true
		}
	}
	return nil, false
}

// RefreshSubtitles 重新发现单个视频的外挂字幕（字幕文件独立增删时由 Watcher 调用）。
func (l *Library) RefreshSubtitles(videoID string) {
	l.mu.RLock()
	v, ok := l.videos[videoID]
	l.mu.RUnlock()
	if !ok {
		return
	}
	subtitles := findSubtitles(l.root, v)
	l.mu.Lock()
	if len(subtitles) > 0 {
		l.subs[videoID] = subtitles
	} else {
		delete(l.subs, videoID)
	}
	l.mu.Unlock()
}

// Upsert 增量加入或更新单个文件；非视频扩展名、文件不存在或读取失败时忽略。
func (l *Library) Upsert(path string) (*Video, bool) {
	if !l.IsVideoExt(path) {
		return nil, false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, false
	}
	rel, err := filepath.Rel(l.root, path)
	if err != nil {
		return nil, false
	}
	v := &Video{
		ID:      makeID(rel),
		Name:    strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		RelPath: filepath.ToSlash(rel),
		Size:    info.Size(),
		ModTime: info.ModTime(),
		Ext:     strings.ToLower(filepath.Ext(path)),
		absPath: path,
	}
	// 刷新该视频的外挂字幕（字幕文件可能与视频先后落地，事件防抖后统一重扫）
	subtitles := findSubtitles(l.root, v)
	md := loadMetadata(l.root, v)

	l.mu.Lock()
	l.videos[v.ID] = v
	if len(subtitles) > 0 {
		l.subs[v.ID] = subtitles
	} else {
		delete(l.subs, v.ID)
	}
	if md != nil {
		l.metadata[v.ID] = md
	} else {
		delete(l.metadata, v.ID)
	}
	l.mu.Unlock()
	return v, true
}

// Remove 增量移除单个文件或目录（目录时移除其下全部条目），返回被移除的数量。
func (l *Library) Remove(path string) int {
	rel, err := filepath.Rel(l.root, path)
	if err != nil {
		return 0
	}
	rel = filepath.ToSlash(rel)

	l.mu.Lock()
	defer l.mu.Unlock()

	removed := 0
	for id, v := range l.videos {
		if v.RelPath == rel || strings.HasPrefix(v.RelPath, rel+"/") {
			delete(l.videos, id)
			delete(l.subs, id)
			delete(l.metadata, id)
			removed++
		}
	}
	return removed
}

// Len 返回当前索引的视频数量。
func (l *Library) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.videos)
}

// makeID 用相对路径的 sha1 前 16 位作为稳定 ID，重扫后保持不变。
func makeID(rel string) string {
	sum := sha1.Sum([]byte(rel))
	return hex.EncodeToString(sum[:])[:16]
}
