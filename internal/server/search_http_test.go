package server

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// addSearchFixture 在测试服务的媒体根目录写入一个英文文件名视频与同名 NFO，
// 并重新扫描让索引与元信息生效。
func addSearchFixture(t *testing.T, srv *Server) {
	t.Helper()
	root := srv.library.Root()
	dir := filepath.Join(root, "films")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inception.mp4"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("写视频失败: %v", err)
	}
	nfo := `<movie><title>盗梦空间</title><originaltitle>Inception</originaltitle>` +
		`<genre>科幻</genre><actor><name>小李子</name></actor></movie>`
	if err := os.WriteFile(filepath.Join(dir, "inception.nfo"), []byte(nfo), 0o644); err != nil {
		t.Fatalf("写 NFO 失败: %v", err)
	}
	if _, err := srv.library.Scan(); err != nil {
		t.Fatalf("重扫失败: %v", err)
	}
}

func TestSearchPageEmptyWhenNoQuery(t *testing.T) {
	_, ts, _ := newPersistTestServer(t)

	code, body := getHTML(t, ts, "/search")
	if code != 200 {
		t.Fatalf("/search 应 200，实际 %d", code)
	}
	if !strings.Contains(body, "输入关键字开始搜索") {
		t.Error("无关键字时应显示引导文案")
	}
	if strings.Contains(body, "video-card") {
		t.Error("无关键字时不应渲染视频卡片")
	}
}

func TestSearchByMetadataTitleAndActor(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	addSearchFixture(t, srv)

	for _, q := range []string{"盗梦空间", "小李子", "Inception"} {
		path := "/search?q=" + url.QueryEscape(q)
		code, body := getHTML(t, ts, path)
		if code != 200 {
			t.Fatalf("q=%s 应 200，实际 %d", q, code)
		}
		if !strings.Contains(body, "video-card") || !strings.Contains(body, "盗梦空间") {
			t.Errorf("q=%s 应命中含中文片名的卡片，body 片段: %s", q, body)
		}
	}
}

func TestSearchByFilenameStillWorks(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	addSearchFixture(t, srv)

	code, body := getHTML(t, ts, "/search?q=inception")
	if code != 200 || !strings.Contains(body, "video-card") {
		t.Fatalf("文件名匹配应命中，code=%d", code)
	}
}

func TestSearchNoResult(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	addSearchFixture(t, srv)

	code, body := getHTML(t, ts, "/search?q="+url.QueryEscape("不存在的奇幻片"))
	if code != 200 {
		t.Fatalf("应 200，实际 %d", code)
	}
	if !strings.Contains(body, "没有找到与") {
		t.Error("零结果时应显示无匹配文案")
	}
	if strings.Contains(body, "video-card") {
		t.Error("零结果时不应渲染视频卡片")
	}
}

func TestSearchPageStoreDisabledStillRenders(t *testing.T) {
	_, ts := newTestServer(t, "")

	code, body := getHTML(t, ts, "/search?q=anything")
	if code != 200 {
		t.Fatalf("无持久化时搜索页也应 200，实际 %d", code)
	}
	if strings.Contains(body, "fav-toggle") {
		t.Error("无持久化时不应渲染收藏按钮")
	}
}
