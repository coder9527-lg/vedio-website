package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/media"
)

// newArrivalTestServer 构造入库时间分三档的最小夹具（无持久化）：
//
//	fresh.mp4：3 天前 → 最近 7 天
//	week.mp4 ：10 天前 → 最近 30 天
//	month.mp4：20 天前 → 最近 30 天
//	old.mp4  ：60 天前 → 更早
//
// 文件 ModTime 在扫描前用 os.Chtimes 显式设定，避免依赖写入时刻。
func newArrivalTestServer(t *testing.T) (*Server, map[string]string) {
	t.Helper()
	root := t.TempDir()
	now := time.Now()
	files := map[string]time.Duration{
		"fresh.mp4": 3 * 24 * time.Hour,
		"week.mp4":  10 * 24 * time.Hour,
		"month.mp4": 20 * 24 * time.Hour,
		"old.mp4":   60 * 24 * time.Hour,
	}
	for rel, age := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatalf("Chtimes 失败: %v", err)
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

// TestNewArrivalPageGroups 时间线页按三档分组渲染且组内按入库时间倒序。
func TestNewArrivalPageGroups(t *testing.T) {
	srv, ids := newArrivalTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/new")
	if code != http.StatusOK {
		t.Fatalf("/new 状态码 = %d，期望 200", code)
	}
	containsAll(t, html,
		"新片时间线",
		"最近 7 天",
		"最近 30 天",
		"更早",
		"new-badge",
		"new-date",
		"/video/"+ids["fresh.mp4"],
		"/video/"+ids["old.mp4"],
	)

	// 最近 7 天组只含 fresh，且其位置应在「更早」组的 old 之前
	i7 := strings.Index(html, "最近 7 天")
	i30 := strings.Index(html, "最近 30 天")
	iOld := strings.Index(html, "更早")
	if !(i7 >= 0 && i30 > i7 && iOld > i30) {
		t.Fatalf("分组顺序错误：7天=%d 30天=%d 更早=%d", i7, i30, iOld)
	}
	freshAt := strings.Index(html, ids["fresh.mp4"])
	oldAt := strings.Index(html, ids["old.mp4"])
	if !(freshAt >= 0 && oldAt > freshAt) {
		t.Fatalf("时间线应按入库时间倒序，fresh 应在 old 之前")
	}
}

// TestNewArrivalBadgeOnHome 首页卡片对窗口内新片打「新」角标，超窗口不打。
func TestNewArrivalBadgeOnHome(t *testing.T) {
	srv, ids := newArrivalTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/")
	if code != http.StatusOK {
		t.Fatalf("首页状态码 = %d，期望 200", code)
	}
	// 首页含新片时间线导航入口
	if !strings.Contains(html, `href="/new"`) {
		t.Fatal("首页缺少新片时间线导航入口 /new")
	}
	// fresh（3 天）在 30 天窗口内，其卡片片段应含「新」角标；old（60 天）超窗口不应含
	cardOf := func(id string) string {
		idx := strings.Index(html, id)
		if idx < 0 {
			t.Fatalf("首页未渲染视频卡片 %s", id)
		}
		// 卡片以 video-card-wrap 包裹，截取当前 ID 到下一个 wrap 起点之间的片段
		rest := html[idx:]
		if next := strings.Index(rest[len(id):], "video-card-wrap"); next >= 0 {
			rest = rest[:len(id)+next]
		}
		return rest
	}
	if !strings.Contains(cardOf(ids["fresh.mp4"]), "new-badge") {
		t.Fatal("fresh 卡片在新片窗口内，应带「新」角标")
	}
	if strings.Contains(cardOf(ids["old.mp4"]), "new-badge") {
		t.Fatal("old 卡片超出新片窗口，不应带「新」角标")
	}
}

// TestIsNewArrivalWindow 直接校验角标判定口径的边界。
func TestIsNewArrivalWindow(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		age  time.Duration
		want bool
	}{
		{"刚入库", time.Minute, true},
		{"29 天", 29 * 24 * time.Hour, true},
		{"恰好 30 天", newArrivalWindow, true},
		{"31 天", 31 * 24 * time.Hour, false},
		{"零值时间", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := &media.Video{ModTime: now.Add(-c.age)}
			if c.name == "零值时间" {
				v.ModTime = time.Time{}
			}
			if got := isNewArrival(v, now); got != c.want {
				t.Fatalf("isNewArrival(%s) = %v，期望 %v", c.name, got, c.want)
			}
		})
	}
	if isNewArrival(nil, now) {
		t.Fatal("nil 视频不应判定为新片")
	}
}

// TestNewArrivalEmptyLibrary 空库渲染空态而不是报错。
func TestNewArrivalEmptyLibrary(t *testing.T) {
	root := t.TempDir()
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
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/new")
	if code != http.StatusOK {
		t.Fatalf("空库 /new 状态码 = %d，期望 200", code)
	}
	if !strings.Contains(html, "媒体库还没有视频") {
		t.Fatal("空库应渲染空态文案")
	}
}
