package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// newGenreTestServer 构造含 3 个不同类型视频的测试服务（无持久化，纯内存库）。
func newGenreTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"action/a.mp4": "x",
		"action/a.nfo": `<movie><title>动作片</title><genre>动作</genre><genre>科幻</genre></movie>`,
		"comedy/b.mkv": "x",
		"comedy/b.nfo": `<movie><title>喜剧片</title><genre>喜剧</genre></movie>`,
		"plain/c.mp4":  "x", // 无元信息
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
		t.Fatal(err)
	}
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(lib, thumbs, nil, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return srv, newHTTPTestServer(t, srv.Handler())
}

func TestGenreFilterHTTP(t *testing.T) {
	_, ts := newGenreTestServer(t)

	// 首页应渲染类型 chips，且含三个类型
	code, body := getBody(t, ts, "/")
	if code != 200 || !strings.Contains(body, "genre-tabs") {
		t.Fatalf("首页应渲染类型筛选区，code=%d", code)
	}
	for _, g := range []string{"动作", "科幻", "喜剧"} {
		if !strings.Contains(body, g) {
			t.Fatalf("chips 缺少类型 %q", g)
		}
	}

	// 按类型筛选：动作只命中 a，且卡片标题出现「动作片」
	code, body = getBody(t, ts, "/?genre=%E5%8A%A8%E4%BD%9C") // 动作
	if code != 200 {
		t.Fatalf("筛选页应 200，got %d", code)
	}
	if !strings.Contains(body, "动作片") || strings.Contains(body, "喜剧片") {
		t.Fatalf("类型筛选结果不正确：应只含动作片")
	}

	// 不存在的类型：视频网格为空（无卡片链接），但 chips 仍渲染
	code, body = getBody(t, ts, "/?genre=nonexistent")
	if code != 200 {
		t.Fatalf("未知类型应 200，got %d", code)
	}
	if strings.Contains(body, `class="video-card"`) {
		t.Fatal("未知类型不应渲染任何视频卡片")
	}
	if !strings.Contains(body, "genre-tabs") {
		t.Fatal("空结果时类型 chips 应保留")
	}

	// 类型与关键字组合正交：科幻 + 文件名关键字 a
	code, body = getBody(t, ts, "/?genre=%E7%A7%91%E5%B9%BB&q=a") // 科幻 + q=a
	if code != 200 || !strings.Contains(body, "动作片") {
		t.Fatalf("类型+关键字组合应命中动作片")
	}
}

func TestGenreChipsKeepOtherParams(t *testing.T) {
	_, ts := newGenreTestServer(t)

	// 带排序访问时，chip 链接应保留 sort 参数（五元组正交）
	_, body := getBody(t, ts, "/?sort=name")
	// 「动作」chip 的 href 应同时包含 sort=name 与 genre 参数
	if !strings.Contains(body, "sort=name") {
		t.Fatal("chip 链接应保留当前排序参数")
	}
	if !strings.Contains(body, "genre=") {
		t.Fatal("chip 链接应携带 genre 参数")
	}
}
