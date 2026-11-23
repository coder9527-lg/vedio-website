package media

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// 缓存命名契约：
//   - 缩略图：<ThumbDir>/<16位小写hex>.jpg，平铺，扩展名恒为 .jpg
//   - TMDB 刮削：<TMDBImageDir>/<16位小写hex>/{poster.*,backdrop.*,metadata.json,...}，一级子目录
var (
	thumbFileRe = regexp.MustCompile(`^[0-9a-f]{16}\.jpg$`)
	idDirRe     = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// orphanGracePeriod 是孤儿条目的 mtime 保护期：写入后 10 分钟内不参与清理，
// 防止 ffmpeg 缩略图直写、TMDB 图片下载进行中被误删。
const orphanGracePeriod = 10 * time.Minute

// ErrLibraryEmpty 是空库硬护栏：媒体卷未挂载、首轮 Scan 未完成时，
// 所有合规缓存都会被误判为孤儿，此场景下禁止任何清理。
var ErrLibraryEmpty = errors.New("媒体库为空，已跳过缓存清理以防止误判")

// CacheEntry 是一个缓存文件或缓存子目录的体检结果。
type CacheEntry struct {
	Name      string    // 相对缓存根的条目名（缩略图为 "xxx.jpg"，TMDB 为 16hex 目录名）
	Path      string    // 绝对路径
	Size      int64     // 文件字节数；目录为内部全部常规文件递归求和
	ModTime   time.Time // 缺失条目为零值
	IsDir     bool
	Orphan    bool // 命名合规但对应 ID 不在库中
	Protected bool // 孤儿且 mtime 在保护期内：展示但清理跳过
	Missing   bool // 库里有 ID 但缓存无对应条目
}

// CacheReport 是一类缓存的完整体检结果。
type CacheReport struct {
	Kind         string // "thumb" / "tmdb"
	Configured   bool   // 目录配置非空
	Exists       bool   // 目录实际存在且可读
	TotalCount   int    // 合规条目总数（缩略图文件数 / TMDB 子目录数）
	TotalSize    int64
	Orphans      []CacheEntry // 命名合规但 ID 不在库；按 Name 升序，切片非 nil
	Unidentified []CacheEntry // 不合命名契约的条目：只展示，永不自动删；按 Name 升序，非 nil
	Missing      []CacheEntry // 库内 ID 缺失对应缓存；按 Name 升序，非 nil（仅统计，不清理）
	MissingCount int
}

// CacheHealth 是两类缓存的合并报告。
type CacheHealth struct {
	Thumb CacheReport
	TMDB  CacheReport
}

// InspectCache 对缩略图与 TMDB 刮削两类缓存做只读体检。
// 目录为空串时对应 Report.Configured=false；目录不存在时 Exists=false，均不返回错误。
func InspectCache(lib *Library, thumbDir, imageDir string) CacheHealth {
	return CacheHealth{
		Thumb: inspectThumbDir(lib, thumbDir),
		TMDB:  inspectTMDBDir(lib, imageDir),
	}
}

// DeleteThumbOrphans 清理缩略图孤儿文件（跳过保护期条目），返回删除数与回收字节数。
// 内部重新扫描，不信任页面快照（防 TOCTOU）；空库返回 ErrLibraryEmpty。
func DeleteThumbOrphans(lib *Library, thumbDir string) (int, int64, error) {
	if lib.Len() == 0 {
		return 0, 0, ErrLibraryEmpty
	}
	rep := inspectThumbDir(lib, thumbDir)
	return deleteOrphanEntries(rep.Orphans, thumbDir, false)
}

// DeleteScraperOrphans 清理 TMDB 孤儿子目录（整个 16hex 目录 RemoveAll），
// 返回删除数与回收字节数。空库返回 ErrLibraryEmpty。
func DeleteScraperOrphans(lib *Library, imageDir string) (int, int64, error) {
	if lib.Len() == 0 {
		return 0, 0, ErrLibraryEmpty
	}
	rep := inspectTMDBDir(lib, imageDir)
	return deleteOrphanEntries(rep.Orphans, imageDir, true)
}

// newCacheReport 初始化一份保证切片非 nil 的报告。
func newCacheReport(kind string) CacheReport {
	return CacheReport{
		Kind:         kind,
		Orphans:      []CacheEntry{},
		Unidentified: []CacheEntry{},
		Missing:      []CacheEntry{},
	}
}

// videoIDSet 收集库内全部视频 ID，用于缓存命中判定。
func videoIDSet(lib *Library) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, v := range lib.List() {
		ids[v.ID] = struct{}{}
	}
	return ids
}

// inspectThumbDir 体检缩略图目录：平铺枚举，合规文件按库 ID 判定孤儿/命中。
func inspectThumbDir(lib *Library, dir string) CacheReport {
	rep := newCacheReport("thumb")
	if dir == "" {
		return rep
	}
	rep.Configured = true
	entries, err := os.ReadDir(dir)
	if err != nil {
		// 目录不存在或不可读：Exists 保持 false，不报错中断整页体检
		return rep
	}
	rep.Exists = true

	ids := videoIDSet(lib)
	now := time.Now()
	for _, ent := range entries {
		full := filepath.Join(dir, ent.Name())
		info, err := ent.Info()
		if err != nil {
			continue // 单条目读取失败跳过，不中断整体扫描
		}
		if !thumbFileRe.MatchString(ent.Name()) {
			// 子目录、散文件、非 .jpg、非 hex 命名：未识别条目只展示
			rep.Unidentified = append(rep.Unidentified, entryFromInfo(ent.Name(), full, info))
			continue
		}
		rep.TotalCount++
		rep.TotalSize += info.Size()
		id := strings.TrimSuffix(ent.Name(), ".jpg")
		if _, ok := ids[id]; !ok {
			e := entryFromInfo(ent.Name(), full, info)
			e.Orphan = true
			if now.Sub(info.ModTime()) < orphanGracePeriod {
				e.Protected = true
			}
			rep.Orphans = append(rep.Orphans, e)
		}
	}

	// 缺失清单：库内 ID 没有对应缩略图文件（只统计，访问 /thumb/{id} 时按需生成）
	for id := range ids {
		p := filepath.Join(dir, id+".jpg")
		if _, err := os.Stat(p); err != nil {
			rep.Missing = append(rep.Missing, CacheEntry{
				Name:    id + ".jpg",
				Path:    p,
				Missing: true,
			})
		}
	}
	sortReport(&rep)
	return rep
}

// inspectTMDBDir 体检 TMDB 刮削目录：仅一级 16hex 子目录为合规条目。
func inspectTMDBDir(lib *Library, dir string) CacheReport {
	rep := newCacheReport("tmdb")
	if dir == "" {
		return rep
	}
	rep.Configured = true
	entries, err := os.ReadDir(dir)
	if err != nil {
		return rep
	}
	rep.Exists = true

	ids := videoIDSet(lib)
	now := time.Now()
	for _, ent := range entries {
		full := filepath.Join(dir, ent.Name())
		info, err := ent.Info()
		if err != nil {
			continue
		}
		if !info.IsDir() || !idDirRe.MatchString(ent.Name()) {
			// 一级散文件或非 hex 目录名：未识别条目只展示
			rep.Unidentified = append(rep.Unidentified, entryFromInfo(ent.Name(), full, info))
			continue
		}
		size := dirSize(full)
		rep.TotalCount++
		rep.TotalSize += size
		if _, ok := ids[ent.Name()]; !ok {
			e := entryFromInfo(ent.Name(), full, info)
			e.Size = size
			e.Orphan = true
			if now.Sub(info.ModTime()) < orphanGracePeriod {
				e.Protected = true
			}
			rep.Orphans = append(rep.Orphans, e)
		}
	}

	// 缺失清单：库内 ID 没有对应刮削子目录
	for id := range ids {
		p := filepath.Join(dir, id)
		if _, err := os.Stat(p); err != nil {
			rep.Missing = append(rep.Missing, CacheEntry{
				Name:    id,
				Path:    p,
				IsDir:   true,
				Missing: true,
			})
		}
	}
	sortReport(&rep)
	return rep
}

// entryFromInfo 从文件信息构造体检条目。
func entryFromInfo(name, full string, info os.FileInfo) CacheEntry {
	return CacheEntry{
		Name:    name,
		Path:    full,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}
}

// dirSize 递归求目录内全部常规文件的字节总和；遍历出错时跳过该条目。
func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// sortReport 对三类清单按 Name 升序排序，并同步缺失计数。
func sortReport(rep *CacheReport) {
	sort.Slice(rep.Orphans, func(i, j int) bool { return rep.Orphans[i].Name < rep.Orphans[j].Name })
	sort.Slice(rep.Unidentified, func(i, j int) bool { return rep.Unidentified[i].Name < rep.Unidentified[j].Name })
	sort.Slice(rep.Missing, func(i, j int) bool { return rep.Missing[i].Name < rep.Missing[j].Name })
	rep.MissingCount = len(rep.Missing)
}

// deleteOrphanEntries 删除给定孤儿条目（Protected 跳过）。
// 条目名只来自服务端 ReadDir，不接受任何客户端入参；删除前再做一次根目录归属校验防穿越。
// 单个条目删除失败不中断后续清理。isDir 为 true 时按目录整体 RemoveAll。
func deleteOrphanEntries(orphans []CacheEntry, root string, isDir bool) (removed int, reclaimed int64, err error) {
	for _, e := range orphans {
		if e.Protected {
			continue
		}
		// 防御性校验：条目名不得含目录分隔符
		if e.Name != filepath.Base(e.Name) {
			continue
		}
		target := filepath.Join(root, e.Name)
		rel, relErr := filepath.Rel(root, target)
		if relErr != nil || rel == ".." || filepath.IsAbs(rel) ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		var delErr error
		if isDir {
			delErr = os.RemoveAll(target)
		} else {
			delErr = os.Remove(target)
		}
		if delErr != nil {
			continue
		}
		removed++
		reclaimed += e.Size
	}
	return removed, reclaimed, nil
}
