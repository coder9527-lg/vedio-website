package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// newRelatedTestServer 构造含同目录多集、异目录视频与根散文件的最小夹具（store=nil，
// 相关视频是纯媒体库数据，与持久化开关无关）。
//
//	剧A/S01E01.mkv、剧A/S01E02.mkv、剧A/S01E03.mkv
//	电影B/单片.mkv
//	根散片.mkv
func newRelatedTestServer(t *testing.T) (*Server, map[string]string) {
	t.Helper()
	root := t.TempDir()
	files := []string{
		"剧A/剧A.S01E01.mkv",
		"剧A/剧A.S01E02.mkv",
		"剧A/剧A.S01E03.mkv",
		"电影B/单片.mkv",
		"根散片.mkv",
	}
	for _, rel := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("fake"), 0o644); err != nil {
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
	// 按 RelPath 建名称 → ID 映射，测试用名称取 ID，避免硬编码哈希
	ids := make(map[string]string, len(files))
	for _, v := range lib.List() {
		ids[v.RelPath] = v.ID
	}
	return srv, ids
}

// TestWatchRelatedSameDir 同目录伙伴出现在相关视频区，异目录与自身不出现。
func TestWatchRelatedSameDir(t *testing.T) {
	srv, ids := newRelatedTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/watch/"+ids["剧A/剧A.S01E01.mkv"])
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	containsAll(t, html,
		`data-related`,
		`class="shelf-row related-row"`,
		`href="/watch/`+ids["剧A/剧A.S01E02.mkv"]+`"`,
		`href="/watch/`+ids["剧A/剧A.S01E03.mkv"]+`"`,
	)

	// 自身链接只应出现在播放器/下载等既有位置，相关卡片里不得包含
	relatedSeg := html[strings.Index(html, "data-related"):]
	if strings.Contains(relatedSeg, `href="/watch/`+ids["剧A/剧A.S01E01.mkv"]+`"`) {
		t.Error("相关视频区不应包含当前视频自身")
	}
	// 异目录视频不得混入
	if strings.Contains(relatedSeg, ids["电影B/单片.mkv"]) {
		t.Error("相关视频区不应包含异目录视频")
	}
	// 同目录只有 2 个伙伴，相关卡片数应为 2
	if got := strings.Count(relatedSeg, "related-card"); got != 2 {
		t.Errorf("相关卡片数 = %d，期望 2", got)
	}
}

// TestWatchRelatedOnlyChild 目录内独苗与根散文件均无同目录伙伴，区块整体不渲染。
func TestWatchRelatedOnlyChild(t *testing.T) {
	srv, ids := newRelatedTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	for _, rel := range []string{"电影B/单片.mkv", "根散片.mkv"} {
		code, html := getHTML(t, ts, "/watch/"+ids[rel])
		if code != http.StatusOK {
			t.Fatalf("播放页状态码 = %d", code)
		}
		if strings.Contains(html, "data-related") {
			t.Errorf("%s 无同目录伙伴，不应渲染相关视频区", rel)
		}
	}
}
