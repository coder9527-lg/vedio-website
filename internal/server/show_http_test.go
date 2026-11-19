package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// newShowTestServer 构造含一部两集剧 + 一部电影的测试服务。
func newShowTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"剧A/剧A.S01E01.mkv":  "a",
		"剧A/剧A.S01E02.mkv":  "b",
		"电影 2024 1080p.mkv": "c",
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
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
	srv, err := New(lib, thumbs, nil, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return srv, newHTTPTestServer(t, srv.Handler())
}

func TestShowAPIListAndGet(t *testing.T) {
	_, ts := newShowTestServer(t)

	resp, err := noRedirectClient.Get(ts.URL + "/api/v1/shows")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列表状态码 %d", resp.StatusCode)
	}
	var list struct {
		Count int `json:"count"`
		Shows []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			EpisodeCount int    `json:"episodeCount"`
		} `json:"shows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || len(list.Shows) != 1 {
		t.Fatalf("期望 1 部剧，实际 %+v", list)
	}
	if list.Shows[0].Title != "剧A" || list.Shows[0].EpisodeCount != 2 {
		t.Fatalf("剧集摘要不符: %+v", list.Shows[0])
	}
	showID := list.Shows[0].ID

	// 详情
	status, body := doJSON(t, ts, http.MethodGet, "/api/v1/shows/"+showID, nil)
	if status != http.StatusOK {
		t.Fatalf("详情应 200，实际 %d", status)
	}
	if body["title"] != "剧A" {
		t.Fatalf("详情标题不符: %v", body["title"])
	}

	// 不存在 → 404
	status, body = doJSON(t, ts, http.MethodGet, "/api/v1/shows/nonexistent", nil)
	if status != http.StatusNotFound || body["error"] != "show not found" {
		t.Fatalf("不存在剧集应 404，实际 %d %v", status, body)
	}

	// 页面 200 且含两集链接
	page := get(t, ts, "/shows/"+showID)
	raw, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("剧集页应 200，实际 %d", page.StatusCode)
	}
	html := string(raw)
	if !strings.Contains(html, "S01E01") || !strings.Contains(html, "/watch/") {
		t.Fatal("剧集页缺少集列表内容")
	}

	// 未知 ID 页面 → 404
	notFound := get(t, ts, "/shows/nonexistent")
	notFound.Body.Close()
	if notFound.StatusCode != http.StatusNotFound {
		t.Fatalf("未知剧集页应 404，实际 %d", notFound.StatusCode)
	}
}

func TestIndexShowCardCollapseAndTypeFilter(t *testing.T) {
	_, ts := newShowTestServer(t)

	// 默认视图：两集折叠为一张剧集卡片，电影一张卡片，共 2 张；页面只出现一次剧名链接
	resp := get(t, ts, "/")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("首页应 200，实际 %d", resp.StatusCode)
	}
	if strings.Count(html, "/shows/") != 1 {
		t.Fatalf("默认视图应只有 1 个剧集卡片链接，实际出现 %d 次", strings.Count(html, "/shows/"))
	}
	if !strings.Contains(html, "电影 2024 1080p") {
		t.Fatal("默认视图缺少电影卡片")
	}

	// type=movie：只剩电影，无剧集卡片
	resp = get(t, ts, "/?type=movie")
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	html = string(raw)
	if strings.Contains(html, "/shows/") {
		t.Fatal("电影视图不应出现剧集卡片")
	}
	if !strings.Contains(html, "电影 2024 1080p") {
		t.Fatal("电影视图应保留电影卡片")
	}

	// type=show：只剩剧集卡片
	resp = get(t, ts, "/?type=show")
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	html = string(raw)
	if strings.Count(html, "/shows/") != 1 {
		t.Fatal("剧集视图应有 1 个剧集卡片")
	}
	if strings.Contains(html, "电影 2024 1080p") {
		t.Fatal("剧集视图不应出现电影")
	}

	// 非法 type 回退 all
	resp = get(t, ts, "/?type=hack")
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Count(string(raw), "/shows/") != 1 {
		t.Fatal("非法 type 应回退全部视图")
	}
}

func TestWatchPageInjectsNextEpisode(t *testing.T) {
	srv, ts := newShowTestServer(t)
	lib := srv.library

	// 找到 S01E01 的视频 ID
	var firstID, secondID string
	for _, v := range lib.List() {
		switch {
		case strings.Contains(v.RelPath, "S01E01"):
			firstID = v.ID
		case strings.Contains(v.RelPath, "S01E02"):
			secondID = v.ID
		}
	}
	if firstID == "" || secondID == "" {
		t.Fatal("未定位到两集视频")
	}

	// 第一集页面包含下一集跳转信息（JS 上下文中的引号会被模板转义，因此分别断言 ID 与连播标记）
	resp := get(t, ts, "/watch/"+firstID)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(raw)
	if !strings.Contains(html, "data-auto-next") || !strings.Contains(html, secondID) {
		t.Fatal("第一集播放页应注入下一集连播信息")
	}

	// 末集页面不应有下一集
	resp = get(t, ts, "/watch/"+secondID)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), "auto-next") {
		t.Fatal("末集不应渲染连播脚本标记")
	}

	// 电影页面无连播
	var movieID string
	for _, v := range lib.List() {
		if !lib.IsShowEpisode(v.ID) {
			movieID = v.ID
		}
	}
	resp = get(t, ts, "/watch/"+movieID)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), "auto-next") {
		t.Fatal("电影页不应渲染连播脚本标记")
	}
}
