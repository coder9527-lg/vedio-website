package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// newContinueTestServer 构造带 SQLite、含 3 个视频的测试服务，返回服务与视频 ID 切片。
func newContinueTestServer(t *testing.T) (*Server, *httptest.Server, []string) {
	t.Helper()
	root := t.TempDir()
	names := []string{"a.mp4", "b.mp4", "c.mp4"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(root, n), []byte("fake-"+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatalf("NewThumbnailer 失败: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(lib, thumbs, nil, st, nil, nil, false)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	ids := make([]string, 0, len(lib.List()))
	for _, v := range lib.List() {
		ids = append(ids, v.ID)
	}
	return srv, newHTTPTestServer(t, srv.Handler()), ids
}

// TestContinuePageWithoutStore 未启用持久化时渲染友好提示页，而非报错。
func TestContinuePageWithoutStore(t *testing.T) {
	ts, _, videoID := newSubtitleUploadServer(t)
	resp := get(t, ts, "/continue")
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("无 store 续播页应 200，实际 %d", resp.StatusCode)
	}
	if !strings.Contains(body, "续播提醒需要持久化支持") {
		t.Fatal("无 store 时应提示需要持久化支持")
	}
	if strings.Contains(body, "/watch/"+videoID) {
		t.Fatal("无 store 时不应渲染续播卡片")
	}
}

// TestContinuePageAggregatesAndSorts 验证聚合口径：过滤无效/已看完进度，
// 剩余条目按最近观看倒序，并展示剩余时长、百分比与一键续播链接。
func TestContinuePageAggregatesAndSorts(t *testing.T) {
	srv, ts, ids := newContinueTestServer(t)
	if len(ids) != 3 {
		t.Fatalf("夹具应有 3 个视频，实际 %d", len(ids))
	}
	idA, idB, idC := ids[0], ids[1], ids[2]
	now := time.Now()

	// idA：看到一半（100/200s），最近观看较早
	saveAt(t, srv, idA, store.ProgressUpdate{Position: 100, Duration: 200}, now.Add(-2*time.Hour))
	// idB：接近看完但仍在阈值内（180/200s = 90%），最近观看，应排最前
	saveAt(t, srv, idB, store.ProgressUpdate{Position: 180, Duration: 200}, now.Add(-5*time.Minute))
	// idC：已看完（195/200s = 97.5%），应被过滤
	saveAt(t, srv, idC, store.ProgressUpdate{Position: 195, Duration: 200}, now.Add(-time.Minute))

	resp := get(t, ts, "/continue")
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("续播页应 200，实际 %d", resp.StatusCode)
	}

	if strings.Count(body, `class="ct-row"`) != 2 {
		t.Fatalf("应只渲染 2 张续播卡片（已看完的被过滤），实际 body=%s", body)
	}
	// idB 最近观看，应排在 idA 之前
	idxB := strings.Index(body, "data-id=\""+idB+"\"")
	idxA := strings.Index(body, "data-id=\""+idA+"\"")
	if idxB < 0 || idxA < 0 || idxB > idxA {
		t.Fatalf("续播卡片应按最近观看倒序（idB 在前），idxB=%d idxA=%d", idxB, idxA)
	}
	// 一键续播链接
	if !strings.Contains(body, "href=\"/watch/"+idB+"\"") {
		t.Fatal("续播卡片应包含一键继续播放链接")
	}
	// 剩余时长：idB 剩 20s（00:20）、idA 剩 100s（01:40），与 formatDuration 口径一致
	if !strings.Contains(body, "剩 00:20") || !strings.Contains(body, "剩 01:40") {
		t.Fatalf("应展示剩余时长，body 缺少对应文案: %s", body)
	}
	// 百分比：idB 90%
	if !strings.Contains(body, "90%") {
		t.Fatal("应展示已看百分比 90%")
	}
	// 已看完的 idC 不应出现
	if strings.Contains(body, "data-id=\""+idC+"\"") {
		t.Fatal("已看完视频不应出现在续播提醒中")
	}
}

// TestContinuePageFiltersTinyProgress 位置不足 5 秒的进度不计入续播。
func TestContinuePageFiltersTinyProgress(t *testing.T) {
	srv, ts, ids := newContinueTestServer(t)
	saveAt(t, srv, ids[0], store.ProgressUpdate{Position: 3, Duration: 200}, time.Now())

	resp := get(t, ts, "/continue")
	body := readAll(t, resp)
	resp.Body.Close()
	if strings.Contains(body, `class="ct-row"`) {
		t.Fatal("观看不足 5 秒的视频不应进入续播提醒")
	}
	if !strings.Contains(body, "暂无待续播的视频") {
		t.Fatal("空列表应展示空态引导")
	}
}

// TestContinuePageSkipsDanglingProgress 进度指向已删除视频时不渲染、不报错。
func TestContinuePageSkipsDanglingProgress(t *testing.T) {
	srv, ts, ids := newContinueTestServer(t)
	saveAt(t, srv, ids[0], store.ProgressUpdate{Position: 100, Duration: 200}, time.Now())
	// 手动写入一条悬空进度（库里不存在的视频 ID）
	if err := srv.store.SaveProgress("ghost-video", store.ProgressUpdate{Position: 100, Duration: 200}); err != nil {
		t.Fatalf("写入悬空进度失败: %v", err)
	}

	resp := get(t, ts, "/continue")
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("含悬空进度时页面仍应 200，实际 %d", resp.StatusCode)
	}
	if strings.Count(body, `class="ct-row"`) != 1 {
		t.Fatalf("悬空进度应被跳过，只剩 1 张卡片，body=%s", body)
	}
	if strings.Contains(body, "ghost-video") {
		t.Fatal("悬空视频 ID 不应出现在页面")
	}
}

// TestContinueClearProgress 清除按钮调用的 DELETE progress 接口应生效，
// 之后该视频不再出现在续播页。
func TestContinueClearProgress(t *testing.T) {
	srv, ts, ids := newContinueTestServer(t)
	saveAt(t, srv, ids[0], store.ProgressUpdate{Position: 100, Duration: 200}, time.Now())

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/videos/"+ids[0]+"/progress", nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("清除进度应 200，实际 %d", resp.StatusCode)
	}

	page := get(t, ts, "/continue")
	body := readAll(t, page)
	page.Body.Close()
	if strings.Contains(body, `class="ct-row"`) {
		t.Fatal("清除进度后续播页不应再有该卡片")
	}
}

// TestContinueEntryOnIndex 持久化开启时首页「更多」下拉与继续观看行出现续播提醒入口。
func TestContinueEntryOnIndex(t *testing.T) {
	_, ts, _ := newPersistTestServer(t)
	code, html := getHTML(t, ts, "/")
	if code != http.StatusOK {
		t.Fatalf("首页状态码 = %d", code)
	}
	if !strings.Contains(html, `href="/continue"`) {
		t.Fatal("首页应包含续播提醒入口 /continue")
	}
}

// saveAt 通过 store 写入进度后，再把 UpdatedAt 调整到指定时间，用于排序测试。
func saveAt(t *testing.T, srv *Server, videoID string, up store.ProgressUpdate, at time.Time) {
	t.Helper()
	if err := srv.store.SaveProgress(videoID, up); err != nil {
		t.Fatalf("SaveProgress 失败: %v", err)
	}
	if _, err := srv.store.DB().Exec(
		"UPDATE progress SET updated_at = ? WHERE video_id = ?", at, videoID); err != nil {
		t.Fatalf("调整 updated_at 失败: %v", err)
	}
}
