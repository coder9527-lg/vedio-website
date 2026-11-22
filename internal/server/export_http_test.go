package server

import (
	"bytes"
	"encoding/csv"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/store"
)

// utf8BOM 是 CSV 正文开头的字节序标记，Excel 据此识别 UTF-8 编码。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// downloadCSV 发起导出请求，返回状态码、响应头与原始正文。
func downloadCSV(t *testing.T, ts *httptest.Server) (int, http.Header, []byte) {
	t.Helper()
	resp, err := noRedirectClient.Get(ts.URL + "/api/v1/export/progress.csv")
	if err != nil {
		t.Fatalf("导出请求失败: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取导出正文失败: %v", err)
	}
	return resp.StatusCode, resp.Header, body
}

// parseCSV 去掉 BOM 后按 CSV 解析为二维字符串。
func parseCSV(t *testing.T, body []byte) [][]string {
	t.Helper()
	body = bytes.TrimPrefix(body, utf8BOM)
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("CSV 解析失败: %v", err)
	}
	return rows
}

func TestExportProgressCSV(t *testing.T) {
	srv, ts, id := newPersistTestServer(t)

	// 视频一：通过 API 上报两次，第二次达到看完阈值
	doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]any{"position": 30, "duration": 100, "watchDelta": 5, "sessionStart": true})
	doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/progress",
		map[string]any{"position": 96, "duration": 100, "watchDelta": 7, "sessionStart": false})
	// 视频二：直接写 store 造一条悬空记录（媒体库中不存在），验证名称回退与不丢数据
	if err := srv.store.SaveProgress("ghost-id", store.ProgressUpdate{
		Position: 10, Duration: 0, WatchDelta: 3, SessionStart: true,
	}); err != nil {
		t.Fatalf("写入悬空进度失败: %v", err)
	}

	code, header, body := downloadCSV(t, ts)
	if code != http.StatusOK {
		t.Fatalf("导出应 200，实际 %d，正文 %s", code, body)
	}
	if ct := header.Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Fatalf("Content-Type 应含 text/csv，实际 %q", ct)
	}
	cd := header.Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.HasSuffix(cd, `.csv"`) {
		t.Fatalf("Content-Disposition 异常: %q", cd)
	}
	if !bytes.HasPrefix(body, utf8BOM) {
		t.Fatal("CSV 正文应以 UTF-8 BOM 开头")
	}

	rows := parseCSV(t, body)
	if len(rows) != 3 { // 表头 + 两条记录
		t.Fatalf("应导出 3 行（表头+2），实际 %d", len(rows))
	}
	if len(rows[0]) != 9 {
		t.Fatalf("表头应为 9 列，实际 %d：%v", len(rows[0]), rows[0])
	}
	if rows[0][0] != "视频名称" || rows[0][6] != "是否看完" || rows[0][8] != "更新时间(UTC)" {
		t.Fatalf("表头内容异常: %v", rows[0])
	}

	// 不依赖行序：按视频 ID（第 2 列）建索引，逐列校验两条记录
	byID := map[string][]string{}
	for _, rec := range rows[1:] {
		byID[rec[1]] = rec
	}
	movie, ok := byID[id]
	if !ok {
		t.Fatalf("导出缺少媒体库视频 %s，实际 %v", id, byID)
	}
	// 名称命中媒体库（Video.Name 不含扩展名）、看完=是、进度 96/总时长 100、播放 1 次、百分比 96
	if movie[0] != "movie" {
		t.Fatalf("视频名称应为 movie，实际 %q", movie[0])
	}
	if movie[2] != "96" || movie[3] != "100" || movie[4] != "1" {
		t.Fatalf("媒体库视频数值列异常: %v", movie)
	}
	if movie[6] != "是" || movie[7] != "96" {
		t.Fatalf("看完标记或百分比异常: %v", movie)
	}
	if _, err := time.Parse("2006-01-02 15:04:05", movie[8]); err != nil {
		t.Fatalf("更新时间格式异常 %q: %v", movie[8], err)
	}

	// 悬空视频：媒体库 miss，名称回退为 ID；时长 0 时百分比留空
	ghost, ok := byID["ghost-id"]
	if !ok {
		t.Fatalf("导出缺少悬空视频记录，实际 %v", byID)
	}
	if ghost[0] != "ghost-id" {
		t.Fatalf("悬空视频名称应回退为 ID，实际 %q", ghost[0])
	}
	if ghost[6] != "否" || ghost[7] != "" {
		t.Fatalf("悬空记录看完标记/百分比异常: %v", ghost)
	}
}

func TestExportProgressRequiresStore(t *testing.T) {
	_, ts := newTestServer(t, "") // store=nil
	code, _, body := downloadCSV(t, ts)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("未启用持久化应 503，实际 %d", code)
	}
	if !strings.Contains(string(body), "persistence disabled") {
		t.Fatalf("503 应返回 JSON 错误提示，实际 %s", body)
	}
}

func TestExportProgressEmptyOnlyHeader(t *testing.T) {
	_, ts, _ := newPersistTestServer(t) // 有库但无任何进度
	code, _, body := downloadCSV(t, ts)
	if code != http.StatusOK {
		t.Fatalf("空库导出也应 200，实际 %d", code)
	}
	rows := parseCSV(t, body)
	if len(rows) != 1 || len(rows[0]) != 9 {
		t.Fatalf("空库应仅 1 行 9 列表头，实际 %v", rows)
	}
}
