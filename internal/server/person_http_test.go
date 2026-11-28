package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// newPersonTestServer 构造含演员/导演元信息的最小夹具（无持久化，纯内存库）：
//
//	films/alpha.mp4：导演「诺兰」，演员「小李子」「玛丽」
//	films/beta.mp4：导演「诺兰」，演员「小李子」
//	films/gamma.mp4：导演「张三」，演员「玛丽」
//	plain/no-meta.mp4：无元信息，不贡献人物
func newPersonTestServer(t *testing.T) (*Server, map[string]string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"films/alpha.mp4": "x",
		"films/alpha.nfo": `<movie><title>阿尔法</title><director>诺兰</director>` +
			`<actor><name>小李子</name></actor><actor><name>玛丽</name></actor></movie>`,
		"films/beta.mp4": "x",
		"films/beta.nfo": `<movie><title>贝塔</title><director>诺兰</director>` +
			`<actor><name>小李子</name></actor></movie>`,
		"films/gamma.mp4": "x",
		"films/gamma.nfo": `<movie><title>伽马</title><director>张三</director>` +
			`<actor><name>玛丽</name></actor><actor><name>Tom Hanks</name></actor></movie>`,
		"plain/no-meta.mp4": "x",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
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
	ids := map[string]string{}
	for _, v := range lib.List() {
		ids[v.RelPath] = v.ID
	}
	return srv, ids
}

// TestPersonPageAggregation 人物聚合页按角色分组，且只含匹配影片。
func TestPersonPageAggregation(t *testing.T) {
	srv, ids := newPersonTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/person/"+url.PathEscape("诺兰"))
	if code != http.StatusOK {
		t.Fatalf("人物页状态码 = %d，期望 200", code)
	}
	containsAll(t, html,
		`data-person-page`,
		`data-person-name`,
		"诺兰",
		"共 2 部影片",
		"导演（2）",
		`href="/watch/`+ids["films/alpha.mp4"]+`"`,
		`href="/watch/`+ids["films/beta.mp4"]+`"`,
	)
	// 诺兰不是任何影片的演员，不应渲染参演分组
	if strings.Contains(html, "参演（") {
		t.Error("诺兰页不应出现参演分组")
	}
	// 无元信息视频与不匹配影片不得出现
	if strings.Contains(html, ids["plain/no-meta.mp4"]) || strings.Contains(html, ids["films/gamma.mp4"]) {
		t.Error("人物页混入了不相关影片")
	}
}

// TestPersonPageActorAndDirector 同一人物既是演员又是导演时两个分组都渲染，
// 去重计数只算影片数而非角色数。
func TestPersonPageActorAndDirector(t *testing.T) {
	srv, ids := newPersonTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	// 小李子在 alpha、beta 中参演，不担任导演
	code, html := getHTML(t, ts, "/person/"+url.PathEscape("小李子"))
	if code != http.StatusOK {
		t.Fatalf("人物页状态码 = %d", code)
	}
	containsAll(t, html,
		"参演（2）",
		`href="/watch/`+ids["films/alpha.mp4"]+`"`,
		`href="/watch/`+ids["films/beta.mp4"]+`"`,
	)
	if strings.Contains(html, "导演（") {
		t.Error("小李子页不应出现导演分组")
	}

	// 玛丽参演 alpha、gamma 共 2 部
	code, html = getHTML(t, ts, "/person/"+url.PathEscape("玛丽"))
	if code != http.StatusOK {
		t.Fatalf("人物页状态码 = %d", code)
	}
	if !strings.Contains(html, "共 2 部影片") || !strings.Contains(html, "参演（2）") {
		t.Errorf("玛丽页聚合数量不正确: %s", html)
	}
}

// TestPersonPageNotFound 全库无匹配人物时返回 404。
func TestPersonPageNotFound(t *testing.T) {
	srv, _ := newPersonTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	if code, _ := getHTML(t, ts, "/person/"+url.PathEscape("不存在的人")); code != http.StatusNotFound {
		t.Errorf("未知人物状态码 = %d，期望 404", code)
	}
}

// TestPersonPageCaseInsensitive 人名匹配大小写不敏感（gamma.nfo 中演员写作 Tom Hanks）。
func TestPersonPageCaseInsensitive(t *testing.T) {
	srv, ids := newPersonTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/person/tom%20hanks")
	if code != http.StatusOK {
		t.Fatalf("小写人名应大小写不敏感命中，code=%d", code)
	}
	containsAll(t, html,
		"参演（1）",
		`href="/watch/`+ids["films/gamma.mp4"]+`"`,
	)
}

// TestWatchPageRendersPersonLinks 播放页元信息区把导演/演员渲染为人物聚合页链接。
func TestWatchPageRendersPersonLinks(t *testing.T) {
	srv, ids := newPersonTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/watch/"+ids["films/alpha.mp4"])
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	containsAll(t, html,
		`class="person-chip"`,
		`href="/person/`+url.PathEscape("诺兰"),
		`href="/person/`+url.PathEscape("小李子"),
		`href="/person/`+url.PathEscape("玛丽"),
	)
}
