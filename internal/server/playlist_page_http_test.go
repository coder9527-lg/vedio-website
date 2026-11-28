package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

// 未启用持久化时合集详情页仍可访问，渲染 DB_PATH 提示而非报错。
func TestCollectionPageWithoutStore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.mp4"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := media.NewLibrary(root, media.DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	thumbs, _ := media.NewThumbnailer(t.TempDir(), "ffmpeg")
	srv, err := New(lib, thumbs, nil, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ts := newHTTPTestServer(t, srv.Handler())

	resp, err := http.Get(ts.URL + "/playlists/whatever")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("无持久化时详情页期望 200，实际 %d", resp.StatusCode)
	}
	if !contains(body, "合集功能需要持久化支持") {
		t.Error("无持久化时应渲染 DB_PATH 提示")
	}
}

// 合集详情页：成员按存储顺序渲染、缺失合集 404、悬空成员被剔除。
func TestCollectionPageRendersItems(t *testing.T) {
	srv, ts, videoID := newPersistTestServer(t)

	// 再添加两个视频，库内共 3 个
	root := srv.library.Root()
	for _, name := range []string{"b.mp4", "c.mp4"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := srv.library.Scan(); err != nil {
		t.Fatal(err)
	}
	all := srv.library.List()
	if len(all) != 3 {
		t.Fatalf("期望 3 个视频，实际 %d", len(all))
	}

	pl, err := srv.store.CreatePlaylist("指环王三部曲")
	if err != nil {
		t.Fatalf("创建合集失败: %v", err)
	}
	// 按 b → 夹具自带 → c 的顺序加入
	var bID, cID string
	for _, v := range all {
		switch v.Name {
		case "b":
			bID = v.ID
		case "c":
			cID = v.ID
		}
	}
	for _, id := range []string{bID, videoID, cID} {
		if _, err := srv.store.AddPlaylistItem(pl.ID, id); err != nil {
			t.Fatalf("加入成员失败: %v", err)
		}
	}

	resp, err := http.Get(ts.URL + "/playlists/" + pl.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("详情页期望 200，实际 %d", resp.StatusCode)
	}
	for _, want := range []string{"指环王三部曲", "共 3 部", "3 部影片", "data-col-id=\"" + pl.ID + "\"", "播放全部", "col-row"} {
		if !contains(body, want) {
			t.Errorf("详情页缺少 %q", want)
		}
	}
	if got := strings.Count(body, "class=\"col-row\""); got != 3 {
		t.Errorf("期望渲染 3 个成员行，实际 %d", got)
	}
	// 回归：内联 JS 的 colID 不得被 printf %q 二次转义混入字面引号
	if strings.Contains(body, `const colID = "\"`) || !strings.Contains(body, `const colID = "`+pl.ID+`"`) {
		t.Error("colID 渲染异常，存在 printf 百分号q 二次转义")
	}
	// 首行序号为 0，末行下移按钮应禁用
	if !contains(body, `<span class="col-index">0</span>`) {
		t.Error("成员序号应从 0 开始渲染")
	}

	// 不存在的合集 → 404
	resp2, err := http.Get(ts.URL + "/playlists/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("缺失合集期望 404，实际 %d", resp2.StatusCode)
	}

	// 悬空成员：直接写入一个库内不存在的视频 ID，详情页应跳过且不报错
	if _, err := srv.store.AddPlaylistItem(pl.ID, "ghost-video"); err != nil {
		t.Fatal(err)
	}
	resp3, err := http.Get(ts.URL + "/playlists/" + pl.ID)
	if err != nil {
		t.Fatal(err)
	}
	body3 := readAll(t, resp3)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK || !contains(body3, "共 3 部") {
		t.Errorf("悬空成员应被剔除，仍显示 3 部，status=%d", resp3.StatusCode)
	}
	if got := strings.Count(body3, "class=\"col-row\""); got != 3 {
		t.Errorf("剔除悬空成员后期望 3 行，实际 %d", got)
	}
}

// 片库合集视图：标签与卡片上的详情页入口存在（需持久化）。
func TestPlaylistViewHasCollectionEntry(t *testing.T) {
	srv, ts, videoID := newPersistTestServer(t)
	pl, err := srv.store.CreatePlaylist("测试合集")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.AddPlaylistItem(pl.ID, videoID); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(ts.URL + "/?view=playlists")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("合集视图期望 200，实际 %d", resp.StatusCode)
	}
	if !contains(body, "📂 合集") || !contains(body, "我的合集") {
		t.Error("合集视图应突出合集入口")
	}
	if !contains(body, "/playlists/"+pl.ID) {
		t.Error("合集卡片应包含详情页链接")
	}
}

// 确保 store.Playlist 类型在本测试文件被引用，避免后续重构误删导入。
var _ = store.Playlist{}
