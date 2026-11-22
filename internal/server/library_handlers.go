package server

import (
	"math"
	"net/http"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// libraryTopFiles 是大文件榜展示条数上限。
const libraryTopFiles = 10

// libraryBucket 是存储分析页目录/扩展名分布的一行展示模型。
type libraryBucket struct {
	Name    string
	Count   int
	Size    int64
	Percent int // 相对全库总字节的占比，0-100
}

// duplicateView 是疑似重复组的展示模型。
type duplicateView struct {
	Name   string
	Size   int64
	Count  int
	Videos []*media.Video
}

// libraryStorageData 是片库存储分析页的模板数据，纯内存聚合，不依赖 SQLite。
type libraryStorageData struct {
	basePage
	TotalCount int
	TotalSize  int64
	AvgSize    int64
	DirCount   int
	Dirs       []libraryBucket
	Exts       []libraryBucket
	TopFiles   []*media.Video
	Duplicates []duplicateView
}

// handleLibraryStorage 渲染片库存储分析页：目录/扩展名占用、大文件榜与疑似重复。
func (s *Server) handleLibraryStorage(w http.ResponseWriter, r *http.Request) {
	stats := s.library.StorageStats()

	data := libraryStorageData{
		basePage:   basePage{Title: "片库存储分析", AuthEnabled: s.authEnabled()},
		TotalCount: stats.TotalCount,
		TotalSize:  stats.TotalSize,
		DirCount:   stats.DirCount,
		Dirs:       make([]libraryBucket, 0, len(stats.Dirs)),
		Exts:       make([]libraryBucket, 0, len(stats.Exts)),
		TopFiles:   s.library.Query(media.Query{Sort: "size", Limit: libraryTopFiles}),
		Duplicates: make([]duplicateView, 0, len(stats.Duplicates)),
	}

	// 空库时避免除零：平均大小与占比一律保持零值。
	if stats.TotalCount > 0 {
		data.AvgSize = stats.TotalSize / int64(stats.TotalCount)
	}
	toBuckets := func(src []media.StorageBucket) []libraryBucket {
		out := make([]libraryBucket, 0, len(src))
		for _, b := range src {
			item := libraryBucket{Name: b.Name, Count: b.Count, Size: b.Size}
			if stats.TotalSize > 0 {
				item.Percent = int(math.Round(float64(b.Size) * 100 / float64(stats.TotalSize)))
			}
			out = append(out, item)
		}
		return out
	}
	data.Dirs = toBuckets(stats.Dirs)
	data.Exts = toBuckets(stats.Exts)

	for _, g := range stats.Duplicates {
		data.Duplicates = append(data.Duplicates, duplicateView{
			Name:   g.Name,
			Size:   g.Size,
			Count:  len(g.Videos),
			Videos: g.Videos,
		})
	}

	s.render(w, "library.html", data)
}
