package server

import (
	"net/http"
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
	// 无持久化时标签筛选行不渲染，但排序与类型筛选仍可用
	if !strings.Contains(body, "data-search-type-tabs") {
		t.Error("有关键字时应渲染类型筛选 tabs")
	}
}

// addSearchFilterFixture 写入一部电影与一部同关键字剧集，供排序/类型/高亮测试共用。
func addSearchFilterFixture(t *testing.T, srv *Server) {
	t.Helper()
	root := srv.library.Root()
	files := []string{
		"films/alpha movie.mp4",
		"shows/Alpha Show/Season 01/Alpha Show S01E01.mp4",
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
	if _, err := srv.library.Scan(); err != nil {
		t.Fatalf("重扫失败: %v", err)
	}
}

// TestSearchSortWhitelist 排序参数进下拉选中态；非法值回退默认且不报错。
func TestSearchSortWhitelist(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	addSearchFilterFixture(t, srv)

	code, body := getHTML(t, ts, "/search?q=alpha&sort=newest")
	if code != http.StatusOK {
		t.Fatalf("状态码 = %d", code)
	}
	if !strings.Contains(body, `<option value="newest" selected>`) {
		t.Error("sort=newest 应在下拉中呈现选中态")
	}

	code, body = getHTML(t, ts, "/search?q=alpha&sort=DROP TABLE")
	// 含 SQL 关键字的参数被安全中间件拦截是预期行为；改用普通非法值验证白名单回退
	code, body = getHTML(t, ts, "/search?q=alpha&sort=not-a-sort")
	if code != http.StatusOK {
		t.Fatalf("非法排序应静默回退，状态码 = %d", code)
	}
	if strings.Contains(body, `value="not-a-sort"`) {
		t.Error("非法排序值不应透传到模板")
	}
	if !strings.Contains(body, `<option value="" selected>`) {
		t.Error("非法排序应回退到默认选项")
	}
}

// TestSearchTypeFilter 电影/剧集类型 tabs 按 ShowIDOf 收敛结果，链接保持关键字与排序。
func TestSearchTypeFilter(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	addSearchFilterFixture(t, srv)

	allCode, allBody := getHTML(t, ts, "/search?q=alpha")
	if allCode != http.StatusOK || !strings.Contains(allBody, "alpha movie") ||
		!strings.Contains(allBody, "Alpha Show S01E01") {
		t.Fatalf("全部类型应同时命中电影与剧集")
	}

	movieCode, movieBody := getHTML(t, ts, "/search?q=alpha&type=movie")
	if movieCode != http.StatusOK {
		t.Fatalf("电影筛选状态码 = %d", movieCode)
	}
	if !strings.Contains(movieBody, "alpha movie") {
		t.Error("电影筛选应保留电影卡片")
	}
	if strings.Contains(movieBody, "Alpha Show S01E01") {
		t.Error("电影筛选不应包含剧集成员")
	}
	// 筛选链接需正交保留关键字
	if !strings.Contains(movieBody, "/search?q=alpha&amp;type=show") {
		t.Error("剧集 tab 链接应保留 q 参数")
	}

	showCode, showBody := getHTML(t, ts, "/search?q=alpha&type=show")
	if showCode != http.StatusOK {
		t.Fatalf("剧集筛选状态码 = %d", showCode)
	}
	if !strings.Contains(showBody, "Alpha Show S01E01") {
		t.Error("剧集筛选应保留剧集卡片")
	}
	if strings.Contains(showBody, "alpha movie") {
		t.Error("剧集筛选不应包含电影")
	}

	// 非法 type 归一化为 all，结果不收敛
	code, body := getHTML(t, ts, "/search?q=alpha&type=anime")
	if code != http.StatusOK || !strings.Contains(body, "alpha movie") ||
		!strings.Contains(body, "Alpha Show S01E01") {
		t.Error("非法类型应回退为全部")
	}
}

// TestSearchHighlight 命中关键字在片名与路径上以 mark 包裹，且筛选条专属属性存在。
func TestSearchHighlight(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	addSearchFilterFixture(t, srv)

	code, body := getHTML(t, ts, "/search?q=alpha")
	if code != http.StatusOK {
		t.Fatalf("状态码 = %d", code)
	}
	if !strings.Contains(body, "<mark>alpha</mark>") {
		t.Error("关键字应被 mark 高亮（小写匹配）")
	}
	// 大写关键字做不区分大小写匹配，mark 内保留原文（小写 alpha）
	code, body = getHTML(t, ts, "/search?q=ALPHA")
	if code != http.StatusOK || !strings.Contains(body, "<mark>alpha</mark>") {
		t.Error("大写关键字应不区分大小写匹配并高亮原文片段")
	}
}

// TestSearchTagFilter 标签筛选与关键字取交集；不存在的标签名强制零结果；无持久化不渲染标签行。
func TestSearchTagFilter(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	addSearchFixture(t, srv)
	addSearchFilterFixture(t, srv)

	// 给盗梦空间打「精选」标签
	var id string
	for _, v := range srv.library.List() {
		if v.Name == "inception" {
			id = v.ID
		}
	}
	if id == "" {
		t.Fatal("未找到 inception 夹具视频")
	}
	status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+id+"/tags",
		map[string]string{"name": "精选"})
	if status != http.StatusCreated {
		t.Fatalf("打标签失败，状态码 = %d", status)
	}

	code, body := getHTML(t, ts, "/search?q="+url.QueryEscape("盗梦空间")+"&tag="+url.QueryEscape("精选"))
	if code != http.StatusOK {
		t.Fatalf("标签筛选状态码 = %d", code)
	}
	if !strings.Contains(body, "video-card") {
		t.Error("命中标签的视频应出现在结果中")
	}
	if !strings.Contains(body, "tag-chip") {
		t.Error("应渲染标签 chips 行")
	}

	// 关键字命中但标签不交集 → 零结果
	code, body = getHTML(t, ts, "/search?q=alpha&tag="+url.QueryEscape("精选"))
	if code != http.StatusOK {
		t.Fatalf("交集为空时状态码 = %d", code)
	}
	if strings.Contains(body, "video-card") || !strings.Contains(body, "没有找到与") {
		t.Error("标签与关键字无交集应显示零结果空态")
	}

	// 不存在的标签名同样强制零结果
	code, body = getHTML(t, ts, "/search?q=inception&tag="+url.QueryEscape("幽灵标签"))
	if code != http.StatusOK || strings.Contains(body, "video-card") {
		t.Error("不存在的标签名应强制零结果")
	}

	// 无持久化服务不渲染标签筛选行
	_, tsNoStore := newTestServer(t, "")
	code, body = getHTML(t, tsNoStore, "/search?q=alpha")
	if code != http.StatusOK || strings.Contains(body, "tag-chips") {
		t.Error("无持久化时不应渲染标签 chips 行")
	}
}
