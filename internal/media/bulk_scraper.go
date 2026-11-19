package media

import (
	"context"
	"errors"
	"sync"
	"time"
)

// bulkInterval 限制批量刮削对 TMDB 的请求节奏：
// 每个视频处理完后等待一个窗口，避免大媒体库触发 API 速率限制。
const bulkInterval = 300 * time.Millisecond

// ErrBulkRunning 在已有批量任务运行时再次启动返回。
var ErrBulkRunning = errors.New("批量刮削任务正在运行中")

// BulkStatus 描述一次批量刮削任务的进度快照（线程安全复制，可直接 JSON 序列化）。
type BulkStatus struct {
	Running    bool   `json:"running"`              // 是否仍在运行
	Total      int    `json:"total"`                // 本次任务纳入的视频总数
	Done       int    `json:"done"`                 // 已处理数量（成功、跳过、失败均计入）
	Success    int    `json:"success"`              // 成功匹配并应用刮削结果的数量
	Skipped    int    `json:"skipped"`              // 已有元信息或无候选而跳过的数量
	Failed     int    `json:"failed"`               // 失败数量
	Current    string `json:"current,omitempty"`    // 正在处理的视频名（空表示尚未开始/已结束）
	LastErr    string `json:"lastError,omitempty"`  // 最近一次错误信息（不中断整体任务）
	StartedAt  string `json:"startedAt,omitempty"`  // RFC3339 启动时间
	FinishedAt string `json:"finishedAt,omitempty"` // RFC3339 结束时间（运行中为空）
}

// BulkScraper 在 Scraper 之上编排「全库缺失元信息自动刮削」：
// 内存单任务模型，同一时刻只允许一个批量任务，状态可轮询。
// 任务状态仅保存在内存，进程重启即丢失（不会有半截任务残留）。
type BulkScraper struct {
	scraper *Scraper
	library *Library

	mu      sync.Mutex
	running bool
	status  BulkStatus
}

// NewBulkScraper 创建批量刮削编排器。
func NewBulkScraper(scraper *Scraper, library *Library) *BulkScraper {
	return &BulkScraper{scraper: scraper, library: library}
}

// Available 与底层刮削器保持一致：未配置 TMDB API Key 时整体不可用。
func (b *BulkScraper) Available() bool {
	return b != nil && b.scraper != nil && b.scraper.Available()
}

// StartBulk 启动一个后台批量刮削 goroutine。
// 已有任务运行时返回 ErrBulkRunning（HTTP 层映射为 409）。
// 仅处理当前没有任何元信息的视频；任务启动时快照视频列表，
// 运行期间新增/删除的视频不影响本次任务。
func (b *BulkScraper) StartBulk() (BulkStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return b.snapshotLocked(), ErrBulkRunning
	}

	// 启动时快照待处理视频，避免长任务期间与文件监听/watcher 竞态
	var pending []*Video
	for _, v := range b.library.List() {
		if b.library.Metadata(v.ID) == nil {
			pending = append(pending, v)
		}
	}

	b.running = true
	b.status = BulkStatus{
		Running:   true,
		Total:     len(pending),
		StartedAt: time.Now().Format(time.RFC3339),
	}
	go b.run(pending)
	return b.snapshotLocked(), nil
}

// run 是批量任务主体：逐个视频搜索→取首个候选→应用，
// 单个视频失败只记录错误并继续，视频之间按 bulkInterval 节流。
func (b *BulkScraper) run(pending []*Video) {
	// 服务没有全局可取消的 context 传入 Scraper，这里用后台 context；
	// 任务粒度为「整库一次」，进程退出时 goroutine 随进程消亡。
	ctx := context.Background()

	for _, v := range pending {
		b.mu.Lock()
		b.status.Current = v.Name
		b.mu.Unlock()

		b.processOne(ctx, v)

		b.mu.Lock()
		b.status.Done++
		b.mu.Unlock()

		// 末个视频无需再等节流窗口
		if b.status.Done < b.status.Total {
			time.Sleep(bulkInterval)
		}
	}

	b.mu.Lock()
	b.running = false
	b.status.Running = false
	b.status.Current = ""
	b.status.FinishedAt = time.Now().Format(time.RFC3339)
	b.mu.Unlock()
}

// processOne 处理单个视频：Apply 前复查元信息，规避任务排队期间
// 被手动刮削或 watcher 本地 NFO 刷新抢先填充的竞态。
func (b *BulkScraper) processOne(ctx context.Context, v *Video) {
	// 复查：启动快照后可能已被其他路径补上元信息
	if b.library.Metadata(v.ID) != nil {
		b.bump(func(st *BulkStatus) { st.Skipped++ })
		return
	}

	hits, err := b.scraper.Search(ctx, v.ID)
	if err != nil {
		b.recordErr(err)
		b.bump(func(st *BulkStatus) { st.Failed++ })
		return
	}
	if len(hits) == 0 {
		// 无候选不算错误：可能是文件名无法识别，留待用户手动刮削
		b.bump(func(st *BulkStatus) { st.Skipped++ })
		return
	}

	if _, err := b.scraper.Apply(ctx, v.ID, hits[0].ID); err != nil {
		b.recordErr(err)
		b.bump(func(st *BulkStatus) { st.Failed++ })
		return
	}
	b.bump(func(st *BulkStatus) { st.Success++ })
}

// bump 在锁内对状态计数做一次修改。
func (b *BulkScraper) bump(fn func(st *BulkStatus)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fn(&b.status)
}

// recordErr 记录最近一次错误（只保留最新一条，避免长任务无界增长）。
func (b *BulkScraper) recordErr(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status.LastErr = err.Error()
}

// Status 返回当前任务状态快照；从未启动过任务时返回零值（running=false）。
func (b *BulkScraper) Status() BulkStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked()
}

// snapshotLocked 复制状态，调用方必须持有 b.mu。
func (b *BulkScraper) snapshotLocked() BulkStatus {
	return b.status
}
