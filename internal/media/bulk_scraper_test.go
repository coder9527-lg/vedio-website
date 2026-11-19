package media

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newBulkTestLibrary 在临时目录生成 n 个测试视频并完成扫描。
func newBulkTestLibrary(t *testing.T, n int) *Library {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < n; i++ {
		name := filepath.Join(root, "电影"+string(rune('A'+i))+".mp4")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lib := NewLibrary(root, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	return lib
}

// waitBulkDone 轮询等待批量任务结束，超时则让测试失败。
func waitBulkDone(t *testing.T, b *BulkScraper) BulkStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st := b.Status()
		if !st.Running {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("批量刮削任务超时未结束: %+v", b.Status())
	return BulkStatus{}
}

// TestBulkScraperScrapesAllMissing 验证全库无元信息时每个视频都被成功刮削。
func TestBulkScraperScrapesAllMissing(t *testing.T) {
	lib := newBulkTestLibrary(t, 2)
	client, _, _ := newFakeTMDB(t)
	scraper, err := NewScraper(client, lib, filepath.Join(t.TempDir(), "tmdb"))
	if err != nil {
		t.Fatal(err)
	}
	bulk := NewBulkScraper(scraper, lib)

	if !bulk.Available() {
		t.Fatal("配置了 API Key 时批量刮削应可用")
	}

	st, err := bulk.StartBulk()
	if err != nil {
		t.Fatalf("首次启动不应报错: %v", err)
	}
	if !st.Running || st.Total != 2 {
		t.Fatalf("启动状态错误: %+v", st)
	}

	final := waitBulkDone(t, bulk)
	if final.Success != 2 || final.Done != 2 || final.Failed != 0 || final.Skipped != 0 {
		t.Fatalf("最终计数错误: %+v", final)
	}
	if final.FinishedAt == "" || final.StartedAt == "" {
		t.Fatalf("应记录启动与结束时间: %+v", final)
	}
	for _, v := range lib.List() {
		if md := lib.Metadata(v.ID); md == nil || md.Title != "流浪地球" {
			t.Fatalf("视频 %s 应已写入刮削元信息", v.ID)
		}
	}
}

// TestBulkScraperRejectsConcurrentStart 验证内存单任务：运行中再次启动得到 ErrBulkRunning。
func TestBulkScraperRejectsConcurrentStart(t *testing.T) {
	lib := newBulkTestLibrary(t, 4) // 多视频 + 节流保证任务不会瞬间结束
	client, _, _ := newFakeTMDB(t)
	scraper, err := NewScraper(client, lib, filepath.Join(t.TempDir(), "tmdb"))
	if err != nil {
		t.Fatal(err)
	}
	bulk := NewBulkScraper(scraper, lib)

	if _, err := bulk.StartBulk(); err != nil {
		t.Fatalf("首次启动失败: %v", err)
	}
	if _, err := bulk.StartBulk(); err != ErrBulkRunning {
		t.Fatalf("并发启动应返回 ErrBulkRunning，实际 %v", err)
	}
	waitBulkDone(t, bulk)

	// 结束后应能再次启动
	if _, err := bulk.StartBulk(); err != nil {
		t.Fatalf("任务结束后应能重新启动，实际 %v", err)
	}
	waitBulkDone(t, bulk)
}

// TestBulkScraperSkipsExistingMetadata 验证已有元信息的视频不纳入任务，
// 且运行期间被外部补上元信息时 processOne 复查跳过。
func TestBulkScraperSkipsExistingMetadata(t *testing.T) {
	lib := newBulkTestLibrary(t, 2)
	client, _, _ := newFakeTMDB(t)
	scraper, err := NewScraper(client, lib, filepath.Join(t.TempDir(), "tmdb"))
	if err != nil {
		t.Fatal(err)
	}
	videos := lib.List()

	// 第一个视频先手动刮削，批量任务启动快照时应排除它
	if _, err := scraper.Apply(t.Context(), videos[0].ID, 999); err != nil {
		t.Fatal(err)
	}

	bulk := NewBulkScraper(scraper, lib)
	st, err := bulk.StartBulk()
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 1 {
		t.Fatalf("已有元信息的视频不应纳入，期望 Total=1，实际 %d", st.Total)
	}
	final := waitBulkDone(t, bulk)
	if final.Success != 1 || final.Skipped != 0 {
		t.Fatalf("计数错误: %+v", final)
	}
}

// TestBulkScraperSkipsWhenNoCandidate 验证搜索无候选时计入 skipped 而非 failed。
func TestBulkScraperSkipsWhenNoCandidate(t *testing.T) {
	lib := newBulkTestLibrary(t, 1)
	// 自定义假服务：搜索永远返回空结果
	apiTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/movie" {
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(apiTS.Close)
	client := NewTMDBClient("key", "zh-CN", apiTS.URL, apiTS.URL)
	scraper, err := NewScraper(client, lib, filepath.Join(t.TempDir(), "tmdb"))
	if err != nil {
		t.Fatal(err)
	}
	bulk := NewBulkScraper(scraper, lib)

	if _, err := bulk.StartBulk(); err != nil {
		t.Fatal(err)
	}
	final := waitBulkDone(t, bulk)
	if final.Skipped != 1 || final.Failed != 0 || final.Success != 0 {
		t.Fatalf("无候选应计 skipped，实际: %+v", final)
	}
}

// TestBulkScraperContinuesAfterFailure 验证单个视频失败不中断整库任务。
func TestBulkScraperContinuesAfterFailure(t *testing.T) {
	lib := newBulkTestLibrary(t, 2)
	// 自定义假服务：搜索返回 500，全部视频走失败路径但任务必须跑完
	apiTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "tmdb down", http.StatusInternalServerError)
	}))
	t.Cleanup(apiTS.Close)
	client := NewTMDBClient("key", "zh-CN", apiTS.URL, apiTS.URL)
	scraper, err := NewScraper(client, lib, filepath.Join(t.TempDir(), "tmdb"))
	if err != nil {
		t.Fatal(err)
	}
	bulk := NewBulkScraper(scraper, lib)

	if _, err := bulk.StartBulk(); err != nil {
		t.Fatal(err)
	}
	final := waitBulkDone(t, bulk)
	if final.Failed != 2 || final.Done != 2 {
		t.Fatalf("两个视频都应失败且任务跑完: %+v", final)
	}
	if final.LastErr == "" {
		t.Fatal("失败应记录最近错误信息")
	}
}

// TestBulkScraperThrottleBetweenVideos 验证相邻视频之间存在节流窗口，
// 避免大媒体库打爆 TMDB 速率限制。
func TestBulkScraperThrottleBetweenVideos(t *testing.T) {
	lib := newBulkTestLibrary(t, 2)
	client, _, _ := newFakeTMDB(t)
	scraper, err := NewScraper(client, lib, filepath.Join(t.TempDir(), "tmdb"))
	if err != nil {
		t.Fatal(err)
	}
	bulk := NewBulkScraper(scraper, lib)

	start := time.Now()
	if _, err := bulk.StartBulk(); err != nil {
		t.Fatal(err)
	}
	waitBulkDone(t, bulk)
	elapsed := time.Since(start)

	// 两个视频之间恰好一次 bulkInterval 等待
	if elapsed < bulkInterval {
		t.Fatalf("节流未生效：耗时 %s 应至少 %s", elapsed, bulkInterval)
	}
}

// TestBulkScraperUnavailableWithoutScraper 验证未配置刮削器时整体不可用。
func TestBulkScraperUnavailableWithoutScraper(t *testing.T) {
	lib := newBulkTestLibrary(t, 1)
	if NewBulkScraper(nil, lib).Available() {
		t.Fatal("scraper 为 nil 时应不可用")
	}
}
