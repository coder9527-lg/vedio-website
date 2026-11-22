package media

import (
	"sort"
	"strings"
)

// rootDirBucket 是媒体根目录下散文件（相对路径不含分隔符）的归桶名称。
const rootDirBucket = "根目录"

// StorageStats 是全库存储占用的内存快照，全部由基本类型组成，不依赖展示层。
type StorageStats struct {
	TotalSize  int64            // 全库视频字节总数
	TotalCount int              // 全库视频文件总数
	DirCount   int              // 一级目录桶数量（含「根目录」桶）
	Dirs       []StorageBucket  // 按一级目录聚合，按 Size 降序、Name 升序排列
	Exts       []StorageBucket  // 按扩展名聚合，按 Size 降序、Name 升序排列
	Duplicates []DuplicateGroup // 疑似重复组，按组 Size 降序、Name 升序排列
}

// StorageBucket 是一个目录/扩展名桶的聚合值。
type StorageBucket struct {
	Name  string // 目录名或扩展名（如 ".mkv"）
	Count int    // 桶内视频文件数
	Size  int64  // 桶内字节总数
}

// DuplicateGroup 是一组疑似重复视频：去扩展名后的 Name 相同且字节数 Size 完全一致。
type DuplicateGroup struct {
	Name   string   // 去扩展名后的文件名
	Size   int64    // 组内单个文件的字节数（同组必然相等）
	Videos []*Video // 组内视频，按 RelPath 升序排列
}

// dupKey 是重复检测的分组键，用结构体而非拼接字符串，避免文件名内含分隔符造成碰撞。
type dupKey struct {
	name string
	size int64
}

// StorageStats 在单次读锁快照内聚合全库存储占用。
// map 遍历本身无序，所有输出切片都显式排序以保证结果确定性。
func (l *Library) StorageStats() StorageStats {
	l.mu.RLock()
	defer l.mu.RUnlock()

	stats := StorageStats{
		Dirs:       make([]StorageBucket, 0),
		Exts:       make([]StorageBucket, 0),
		Duplicates: make([]DuplicateGroup, 0),
	}

	dirMap := make(map[string]*StorageBucket)
	extMap := make(map[string]*StorageBucket)
	dupMap := make(map[dupKey][]*Video)

	for _, v := range l.videos {
		stats.TotalCount++
		stats.TotalSize += v.Size

		// 目录桶：只取相对路径的第一段；不含分隔符的根目录散文件归入「根目录」。
		dirName := rootDirBucket
		if i := strings.IndexByte(v.RelPath, '/'); i >= 0 {
			dirName = v.RelPath[:i]
		}
		bucket, ok := dirMap[dirName]
		if !ok {
			bucket = &StorageBucket{Name: dirName}
			dirMap[dirName] = bucket
		}
		bucket.Count++
		bucket.Size += v.Size

		// 扩展名桶：Ext 在扫描时已统一为小写。
		extBucket, ok := extMap[v.Ext]
		if !ok {
			extBucket = &StorageBucket{Name: v.Ext}
			extMap[v.Ext] = extBucket
		}
		extBucket.Count++
		extBucket.Size += v.Size

		key := dupKey{name: v.Name, size: v.Size}
		dupMap[key] = append(dupMap[key], v)
	}

	for _, b := range dirMap {
		stats.Dirs = append(stats.Dirs, *b)
	}
	for _, b := range extMap {
		stats.Exts = append(stats.Exts, *b)
	}

	// 仅保留成员数不少于 2 的分组，单文件组不构成疑似重复。
	for key, videos := range dupMap {
		if len(videos) < 2 {
			continue
		}
		sort.Slice(videos, func(i, j int) bool { return videos[i].RelPath < videos[j].RelPath })
		stats.Duplicates = append(stats.Duplicates, DuplicateGroup{
			Name:   key.name,
			Size:   key.size,
			Videos: videos,
		})
	}

	// 桶与重复组统一按 Size 降序、Name 升序排列，保证输出确定。
	lessBucket := func(s []StorageBucket) func(i, j int) bool {
		return func(i, j int) bool {
			if s[i].Size == s[j].Size {
				return s[i].Name < s[j].Name
			}
			return s[i].Size > s[j].Size
		}
	}
	sort.Slice(stats.Dirs, lessBucket(stats.Dirs))
	sort.Slice(stats.Exts, lessBucket(stats.Exts))
	sort.Slice(stats.Duplicates, func(i, j int) bool {
		if stats.Duplicates[i].Size == stats.Duplicates[j].Size {
			return stats.Duplicates[i].Name < stats.Duplicates[j].Name
		}
		return stats.Duplicates[i].Size > stats.Duplicates[j].Size
	})

	stats.DirCount = len(stats.Dirs)
	return stats
}
