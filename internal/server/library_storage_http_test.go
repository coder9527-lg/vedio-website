package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLibraryStoragePageRenders 持久化开启时页面 200 并渲染全部区块。
func TestLibraryStoragePageRenders(t *testing.T) {
	_, ts, _ := newPersistTestServer(t)
	code, html := getHTML(t, ts, "/library/storage")
	if code != http.StatusOK {
		t.Fatalf("存储分析页状态码 = %d", code)
	}
	containsAll(t, html,
		`片库存储分析`,
		`视频总数`,
		`总占用`,
		`平均单文件`,
		`一级目录`,
		`目录占用分布`,
		`扩展名分布`,
		`大文件 Top`,
		`疑似重复文件`,
	)
}

// TestLibraryStorageWithoutStore 未启用持久化（store=nil）时页面同样完整可用，证明不依赖 SQLite。
func TestLibraryStorageWithoutStore(t *testing.T) {
	ts, _, _ := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/library/storage")
	if code != http.StatusOK {
		t.Fatalf("store=nil 时存储分析页状态码 = %d", code)
	}
	// 夹具 Movie.mkv 内容为 "fake"（4 字节），humanSize 应渲染为 "4 B"
	if !strings.Contains(html, "4 B") {
		t.Error("页面应包含 humanSize 渲染出的容量文本")
	}
}

// TestLibraryStorageTopFilesLimit 大文件榜至多展示 10 条，即便库内文件更多。
func TestLibraryStorageTopFilesLimit(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	root := srv.library.Root()
	for i := 0; i < 11; i++ {
		p := filepath.Join(root, "top", "f"+itoa(i)+".mp4")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
		// 每个文件字节数不同，确保排序与上限逻辑都被覆盖
		if err := os.WriteFile(p, []byte(strings.Repeat("x", i+1)), 0o644); err != nil {
			t.Fatalf("写入测试文件失败: %v", err)
		}
	}
	if _, err := srv.library.Scan(); err != nil {
		t.Fatalf("重新扫描失败: %v", err)
	}

	code, html := getHTML(t, ts, "/library/storage")
	if code != http.StatusOK {
		t.Fatalf("存储分析页状态码 = %d", code)
	}
	if got := strings.Count(html, `href="/watch/`); got > libraryTopFiles {
		t.Errorf("大文件榜链接数 = %d，应不超过 %d", got, libraryTopFiles)
	}
}

// TestLibraryStorageDuplicates 同名且同大小的文件在页面上形成疑似重复组。
func TestLibraryStorageDuplicates(t *testing.T) {
	srv, ts, _ := newPersistTestServer(t)
	root := srv.library.Root()
	for _, dir := range []string{"dir-a", "dir-b"} {
		p := filepath.Join(root, dir, "dup.mp4")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
		if err := os.WriteFile(p, []byte("same-bytes"), 0o644); err != nil {
			t.Fatalf("写入测试文件失败: %v", err)
		}
	}
	if _, err := srv.library.Scan(); err != nil {
		t.Fatalf("重新扫描失败: %v", err)
	}

	code, html := getHTML(t, ts, "/library/storage")
	if code != http.StatusOK {
		t.Fatalf("存储分析页状态码 = %d", code)
	}
	if !strings.Contains(html, "dup") {
		t.Error("页面应展示疑似重复组名 dup")
	}
	if !strings.Contains(html, `dir-a/dup.mp4`) || !strings.Contains(html, `dir-b/dup.mp4`) {
		t.Error("页面应列出重复组内两个文件的相对路径")
	}
}

// TestLibraryStorageRequiresAuth 开启认证后匿名访问被重定向到登录页。
func TestLibraryStorageRequiresAuth(t *testing.T) {
	_, ts := newTestServer(t, "secret-pass")
	resp, err := noRedirectClient.Get(ts.URL + "/library/storage")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("匿名访问状态码 = %d，期望 303", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Errorf("应重定向到登录页，实际 Location = %q", resp.Header.Get("Location"))
	}
}
