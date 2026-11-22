package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestBrowseRootPage 根浏览页列出直接子目录与根散文件，并渲染面包屑首节点。
func TestBrowseRootPage(t *testing.T) {
	_, ts := newShowTestServer(t)

	code, html := getHTML(t, ts, "/browse")
	if code != http.StatusOK {
		t.Fatalf("/browse 应 200，实际 %d", code)
	}
	containsAll(t, html,
		"目录浏览",
		"剧A",
		"电影 2024 1080p.mkv",
		"媒体库",
		// href 上下文由 html/template 自动百分号编码（小写十六进制），斜杠原样保留
		`href="/browse/%e5%89%a7A"`,
		"个视频",
	)
}

// TestBrowseSubDirPage 进入子目录后展示直接视频的播放链接与完整面包屑。
func TestBrowseSubDirPage(t *testing.T) {
	srv, ts := newShowTestServer(t)

	code, html := getHTML(t, ts, "/browse/剧A")
	if code != http.StatusOK {
		t.Fatalf("/browse/剧A 应 200，实际 %d", code)
	}
	// 剧A 下两集都是直接视频，应有两个 /watch/ 链接与两个面包屑节点
	if got := strings.Count(html, "/watch/"); got != 2 {
		t.Fatalf("剧A 页面应含 2 个播放链接，实际 %d", got)
	}
	containsAll(t, html, "媒体库", "剧A", "S01E01", "S01E02")
	// 当前目录节点渲染为纯文本（aria-current），不再是可点击链接
	if !strings.Contains(html, `aria-current="page"`) {
		t.Fatal("末级面包屑应标记 aria-current")
	}

	// 仅剧A 下的两集应出现在本页，根散文件（电影）不得泄漏进来
	for _, v := range srv.library.List() {
		hasLink := strings.Contains(html, "/watch/"+v.ID)
		if strings.HasPrefix(v.RelPath, "剧A/") {
			if !hasLink {
				t.Fatalf("页面缺少剧A 视频链接 /watch/%s", v.ID)
			}
		} else if hasLink {
			t.Fatalf("页面不应包含目录外视频 %s 的链接", v.RelPath)
		}
	}
}

// TestBrowseNotFound 索引中没有任何视频归属的目录返回 404。
func TestBrowseNotFound(t *testing.T) {
	_, ts := newShowTestServer(t)

	code, _ := getHTML(t, ts, "/browse/不存在的目录")
	if code != http.StatusNotFound {
		t.Fatalf("不存在目录应 404，实际 %d", code)
	}
}

// TestBrowseTraversalDefense 各类逃逸媒体根的路径都不得返回 200。
func TestBrowseTraversalDefense(t *testing.T) {
	_, ts := newShowTestServer(t)

	paths := []string{
		"/browse/../etc",
		"/browse/" + url.PathEscape("../etc"),
		"/browse//etc",
		"/browse/" + url.PathEscape(`..\etc`),
	}
	for _, p := range paths {
		code, _ := getHTML(t, ts, p)
		if code == http.StatusOK {
			t.Fatalf("非法路径 %s 不应返回 200", p)
		}
	}
}

// TestBrowseTrailingSlash 尾斜杠经规范化收敛后与无尾斜杠等价。
func TestBrowseTrailingSlash(t *testing.T) {
	_, ts := newShowTestServer(t)

	code, html := getHTML(t, ts, "/browse/剧A/")
	if code != http.StatusOK {
		t.Fatalf("尾斜杠路径应 200，实际 %d", code)
	}
	if !strings.Contains(html, "S01E01") {
		t.Fatal("尾斜杠收敛后应展示剧A 内容")
	}
}

// TestBrowseAuthRequired 开启访问密码后，目录浏览页与子路径均跳转登录。
func TestBrowseAuthRequired(t *testing.T) {
	_, ts := newTestServer(t, "secret")

	for _, p := range []string{"/browse", "/browse/剧A"} {
		resp := get(t, ts, p)
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s 未登录应 303，实际 %d", p, resp.StatusCode)
		}
		loc, err := resp.Location()
		if err != nil {
			t.Fatalf("%s 缺少 Location 头: %v", p, err)
		}
		if !strings.HasPrefix(loc.Path, "/login") {
			t.Fatalf("%s 应跳转登录页，实际 %s", p, loc.String())
		}
	}
}
