package media

import (
	"crypto/sha1"
	"encoding/hex"
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

	mu     sync.RWMutex
	videos map[string]*Video
}

func NewLibrary(root string, exts map[string]bool) *Library {
	return &Library{
		root:   root,
		exts:   exts,
		videos: map[string]*Video{},
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

	l.mu.Lock()
	l.videos = found
	l.mu.Unlock()
	return len(found), err
}

// List 返回按相对路径排序的全部视频。
func (l *Library) List() []*Video {
	l.mu.RLock()
	defer l.mu.RUnlock()

	out := make([]*Video, 0, len(l.videos))
	for _, v := range l.videos {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out
}

// Get 按 ID 查找视频。
func (l *Library) Get(id string) (*Video, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	v, ok := l.videos[id]
	return v, ok
}

// IsVideoExt 判断扩展名是否为受支持的视频。
func (l *Library) IsVideoExt(name string) bool {
	return l.exts[strings.ToLower(filepath.Ext(name))]
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
	l.mu.Lock()
	l.videos[v.ID] = v
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
