package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// newRandomPersistServer 构造含指定视频且开启持久化的服务，返回服务与名称 → ID 映射。
func newRandomPersistServer(t *testing.T, names []string) (*Server, map[string]string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fake"), 0o644); err != nil {
			t.Fatalf("写入测试视频失败: %v", err)
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
	ids := make(map[string]string, len(names))
	for _, v := range lib.List() {
		ids[v.RelPath] = v.ID
	}
	return srv, ids
}

// TestRandomNoStorePicksFromAll 未启用持久化时全库随机：反复请求都 303 到库内合法视频。
func TestRandomNoStorePicksFromAll(t *testing.T) {
	srv, ids := newRelatedTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	valid := make(map[string]bool, len(ids))
	for _, id := range ids {
		valid["/watch/"+id] = true
	}
	for i := 0; i < 30; i++ {
		resp, err := noRedirectClient.Get(ts.URL + "/random")
		if err != nil {
			t.Fatalf("请求 /random 失败: %v", err)
		}
		if resp.StatusCode != http.StatusSeeOther {
			resp.Body.Close()
			t.Fatalf("状态码 = %d，期望 303", resp.StatusCode)
		}
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		if !valid[loc] {
			t.Fatalf("重定向目标 %q 不在媒体库 ID 集合内", loc)
		}
	}
}

// TestRandomSinglePersisted 单视频库：随机结果唯一指向该视频。
func TestRandomSinglePersisted(t *testing.T) {
	_, ts, vid := newPersistTestServer(t)

	resp, err := noRedirectClient.Get(ts.URL + "/random")
	if err != nil {
		t.Fatalf("请求 /random 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("状态码 = %d，期望 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/watch/"+vid {
		t.Fatalf("Location = %q，期望 /watch/%s", loc, vid)
	}
}

// TestRandomFallbackAllCompleted 唯一视频已看完时未看完池为空，回退全库仍能播。
func TestRandomFallbackAllCompleted(t *testing.T) {
	_, ts, vid := newPersistTestServer(t)

	// 96/100 达到 95% 看完阈值，黏滞标记 completed=true
	if status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+vid+"/progress",
		map[string]float64{"position": 96, "duration": 100}); status != http.StatusOK {
		t.Fatalf("上报看完进度应 200，实际 %d", status)
	}

	resp, err := noRedirectClient.Get(ts.URL + "/random")
	if err != nil {
		t.Fatalf("请求 /random 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("全部看完回退应 303，实际 %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/watch/"+vid {
		t.Fatalf("回退全库应指向唯一视频，Location = %q", loc)
	}
}

// TestRandomExcludesCompleted 多视频库中已看完视频不进未看完池，结果永远落在未看完视频上。
func TestRandomExcludesCompleted(t *testing.T) {
	srv, ids := newRandomPersistServer(t, []string{"a.mp4", "b.mp4", "c.mp4"})
	ts := newHTTPTestServer(t, srv.Handler())

	// a、b 标记看完，c 保持未看，未看完池应只剩 c
	for _, name := range []string{"a.mp4", "b.mp4"} {
		if status, _ := doJSON(t, ts, http.MethodPut, "/api/v1/videos/"+ids[name]+"/progress",
			map[string]float64{"position": 96, "duration": 100}); status != http.StatusOK {
			t.Fatalf("标记 %s 看完应 200，实际 %d", name, status)
		}
	}
	want := "/watch/" + ids["c.mp4"]
	for i := 0; i < 30; i++ {
		resp, err := noRedirectClient.Get(ts.URL + "/random")
		if err != nil {
			t.Fatalf("请求 /random 失败: %v", err)
		}
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || loc != want {
			t.Fatalf("第 %d 次随机 = %d %q，期望 303 %s", i, resp.StatusCode, loc, want)
		}
	}
}

// TestRandomEmptyLibrary 空库返回 404。
func TestRandomEmptyLibrary(t *testing.T) {
	lib := media.NewLibrary(t.TempDir(), media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("空库扫描失败: %v", err)
	}
	thumbs, err := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	if err != nil {
		t.Fatalf("NewThumbnailer 失败: %v", err)
	}
	srv, err := New(lib, thumbs, nil, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	ts := newHTTPTestServer(t, srv.Handler())

	resp, err := noRedirectClient.Get(ts.URL + "/random")
	if err != nil {
		t.Fatalf("请求 /random 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("空库状态码 = %d，期望 404", resp.StatusCode)
	}
}

// TestRandomEntryRendered 首页下拉与播放页 header 均渲染常驻「随便看看」入口（不依赖持久化）。
func TestRandomEntryRendered(t *testing.T) {
	srv, ids := newRelatedTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())
	anyID := ids["根散片.mkv"]

	code, homeHTML := getHTML(t, ts, "/")
	if code != http.StatusOK {
		t.Fatalf("首页状态码 = %d", code)
	}
	containsAll(t, homeHTML, `href="/random"`, "🎲 随便看看")

	code, watchHTML := getHTML(t, ts, "/watch/"+anyID)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	containsAll(t, watchHTML, `href="/random"`, "🎲 随便看看")
}
