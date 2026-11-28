package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// newVideoDetailTestServer 构造带类型元信息的最小夹具（无持久化）：
//
//	films/alpha.mp4：动作、科幻
//	films/beta.mp4：动作
//	films/gamma.mp4：爱情（与 alpha 无共同类型）
//	plain/lonely.mp4：无元信息、独立目录，用于验证零元信息降级
func newVideoDetailTestServer(t *testing.T) (*Server, map[string]string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"films/alpha.mp4": "x",
		"films/alpha.nfo": `<movie><title>阿尔法</title><year>2024</year><runtime>120</runtime>` +
			`<plot>阿尔法的剧情简介。</plot><genre>动作</genre><genre>科幻</genre>` +
			`<director>诺兰</director><actor><name>小李子</name></actor><studio>制片厂</studio></movie>`,
		"films/beta.mp4": "x",
		"films/beta.nfo": `<movie><title>贝塔</title><genre>动作</genre></movie>`,
		"films/gamma.mp4": "x",
		"films/gamma.nfo": `<movie><title>伽马</title><genre>爱情</genre></movie>`,
		"plain/lonely.mp4": "x",
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

// TestVideoDetailRendersMetadata 详情页渲染刮削元信息全字段与文件信息。
func TestVideoDetailRendersMetadata(t *testing.T) {
	srv, ids := newVideoDetailTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/video/"+ids["films/alpha.mp4"])
	if code != http.StatusOK {
		t.Fatalf("详情页状态码 = %d，期望 200", code)
	}
	containsAll(t, html,
		`data-video-detail`,
		"阿尔法",
		"2024",
		"120 分钟",
		"阿尔法的剧情简介。",
		"诺兰",
		"小李子",
		"制片厂",
		`href="/watch/`+ids["films/alpha.mp4"]+`"`,
		"films/alpha.mp4", // 文件路径
	)
	// 类型渲染为带筛选参数的链接（模板输出小写百分号编码，断言时归一化大小写）
	if !strings.Contains(strings.ToLower(html), strings.ToLower(`/?genre=`+url.QueryEscape("动作"))) {
		t.Error("详情页未渲染类型筛选链接")
	}
	// 人物链接到人物聚合页
	if !strings.Contains(html, `/person/`+url.PathEscape("诺兰")) {
		t.Error("详情页未渲染导演人物链接")
	}
}

// TestVideoDetailSimilarByGenre 共同类型推荐命中同类型视频，并排除自身与不相关视频。
func TestVideoDetailSimilarByGenre(t *testing.T) {
	srv, ids := newVideoDetailTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	_, html := getHTML(t, ts, "/video/"+ids["films/alpha.mp4"])
	containsAll(t, html,
		"更多类似",
		"共同类型",
	)
	// 仅在推荐区内断言，避免 hero 区当前视频 ID 干扰
	similar := html[strings.Index(html, "更多类似"):]
	if !strings.Contains(similar, `href="/video/`+ids["films/beta.mp4"]+`"`) {
		t.Error("共同类型推荐未命中 beta")
	}
	// gamma 是爱情片，与 alpha 无共同类型，且级别 4 同目录兜底前 12 名内可能收录，
	// 但它必须以「同目录」而非「共同类型」理由出现；这里验证共同类型理由只挂在 beta 上
	if strings.Contains(similar, `href="/video/`+ids["films/gamma.mp4"]+`"`) &&
		!strings.Contains(similar, "同目录") {
		t.Error("gamma 不应以共同类型理由出现")
	}
	// 自身不得出现在推荐位
	if strings.Contains(similar, `href="/video/`+ids["films/alpha.mp4"]+`"`) {
		t.Error("自身出现在推荐位")
	}
}

// TestVideoDetailNoMetadataFallback 零元信息视频详情页不报错，且同目录伙伴兜底推荐。
func TestVideoDetailNoMetadataFallback(t *testing.T) {
	srv, ids := newVideoDetailTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/video/"+ids["plain/lonely.mp4"])
	if code != http.StatusOK {
		t.Fatalf("无元信息详情页状态码 = %d，期望 200", code)
	}
	containsAll(t, html,
		`data-video-detail`,
		"暂无刮削简介",
		"plain/lonely.mp4",
	)
	// lonely 独立目录无伙伴，也无元信息/标签，推荐区应整块隐藏
	if strings.Contains(html, "更多类似") {
		t.Error("无任何候选时不应渲染推荐区")
	}

	// alpha 与 beta/gamma 同目录：gamma 虽无共同类型，但应经同目录兜底出现
	_, htmlAlpha := getHTML(t, ts, "/video/"+ids["films/gamma.mp4"])
	if !strings.Contains(htmlAlpha, `href="/video/`+ids["films/alpha.mp4"]+`"`) {
		t.Error("同目录兜底推荐缺失")
	}
	if !strings.Contains(htmlAlpha, "同目录") {
		t.Error("同目录兜底应标注推荐理由")
	}
}

// TestHomeRendersDetailEntry 首页电影卡片渲染「详情」覆盖按钮与跳转处理。
func TestHomeRendersDetailEntry(t *testing.T) {
	srv, ids := newVideoDetailTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/")
	if code != http.StatusOK {
		t.Fatalf("首页状态码 = %d", code)
	}
	containsAll(t, html,
		`data-detail-href="/video/`+ids["films/alpha.mp4"]+`"`,
		"card-detail-link",
		"data-detail-href",
	)
}

// TestWatchPageRendersDetailEntry 播放页 header 常驻「影片详情」入口（不依赖持久化）。
func TestWatchPageRendersDetailEntry(t *testing.T) {
	srv, ids := newVideoDetailTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	_, html := getHTML(t, ts, "/watch/"+ids["films/alpha.mp4"])
	containsAll(t, html,
		`href="/video/`+ids["films/alpha.mp4"]+`"`,
		"影片详情",
	)
}

// TestVideoDetailNotFound 未知视频 ID 返回 404。
func TestVideoDetailNotFound(t *testing.T) {
	srv, _ := newVideoDetailTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	if code, _ := getHTML(t, ts, "/video/does-not-exist"); code != http.StatusNotFound {
		t.Errorf("未知视频状态码 = %d，期望 404", code)
	}
}

// newVideoDetailPersistServer 在持久化夹具上叠加两部同目录影片，用于标签/进度相关用例。
func newVideoDetailPersistServer(t *testing.T) (*Server, map[string]string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"a.mp4": "x",
		"a.nfo": `<movie><title>甲片</title><genre>动作</genre></movie>`,
		"b.mp4": "x",
		"b.nfo": `<movie><title>乙片</title><genre>动作</genre></movie>`,
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatal(err)
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
	ids := map[string]string{}
	for _, v := range lib.List() {
		ids[v.RelPath] = v.ID
	}
	return srv, ids
}

// TestVideoDetailPersistState 持久化开启时详情页展示收藏态、稍后再看态与有效进度。
func TestVideoDetailPersistState(t *testing.T) {
	srv, ids := newVideoDetailPersistServer(t)
	ts := newHTTPTestServer(t, srv.Handler())
	id := ids["a.mp4"]

	if err := srv.store.SetFavorite(id, true); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.AddWatchLater(id); err != nil {
		t.Fatal(err)
	}
	// 有效进度：60 秒 / 120 秒 = 50%
	if err := srv.store.SaveProgress(id, store.ProgressUpdate{Position: 60, Duration: 120}); err != nil {
		t.Fatal(err)
	}

	_, html := getHTML(t, ts, "/video/"+id)
	containsAll(t, html,
		`data-vd-fav`,
		"★ 已收藏",
		"已加入稍后再看",
		`data-vd-progress`,
		"50%",
		"剩 01:00",
	)
}

// TestVideoDetailSimilarByTag 共同用户标签可作为推荐来源（持久化专属）。
func TestVideoDetailSimilarByTag(t *testing.T) {
	srv, ids := newVideoDetailPersistServer(t)
	ts := newHTTPTestServer(t, srv.Handler())
	a, b := ids["a.mp4"], ids["b.mp4"]

	// 给两片打上同名标签；SetVideoTag 会在标签缺失时自动建标签
	if _, _, err := srv.store.SetVideoTag(a, "精选"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.store.SetVideoTag(b, "精选"); err != nil {
		t.Fatal(err)
	}

	code, html := getHTML(t, ts, "/video/"+a)
	if code != http.StatusOK {
		t.Fatalf("详情页状态码 = %d", code)
	}
	if !strings.Contains(html, b) {
		t.Error("标签推荐未命中同标签影片")
	}
}
