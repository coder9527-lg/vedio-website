package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 本文件聚焦分享管理中心（/shares HTML 页面）的端到端行为。

// TestShareCenterListPage 验证管理页列出全部分享及关键渲染片段。
func TestShareCenterListPage(t *testing.T) {
	srv, ts, videoID := newPersistTestServer(t)

	sh1, err := srv.store.CreateShare(videoID, 24, "")
	if err != nil {
		t.Fatal(err)
	}
	sh2, err := srv.store.CreateShare(videoID, 168, "secret123")
	if err != nil {
		t.Fatal(err)
	}

	code, body := getHTML(t, ts, "/shares")
	if code != http.StatusOK {
		t.Fatalf("管理页应 200，got %d", code)
	}
	for _, want := range []string{
		"分享管理",
		"/share/" + sh1.Token,
		"/share/" + sh2.Token,
		"已设置",            // 带码分享的提取码标记
		"无提取码",           // 无码分享
		"清理已过期",          // 顶部操作
		`data-copy`,      // 复制链接按钮
		`/shares/delete`, // 撤销表单
	} {
		if !strings.Contains(body, want) {
			t.Errorf("管理页缺少片段 %q", want)
		}
	}
}

// TestShareCenterEmptyState 新库无分享时显示空态。
func TestShareCenterEmptyState(t *testing.T) {
	_, ts, _ := newPersistTestServer(t)
	code, body := getHTML(t, ts, "/shares")
	if code != http.StatusOK || !strings.Contains(body, "还没有创建过分享") {
		t.Fatalf("空库应 200 且显示空态，got %d", code)
	}
}

// TestShareCenterStoreOff 未启用持久化时渲染降级空态而非报错。
func TestShareCenterStoreOff(t *testing.T) {
	ts, _, _ := newSubtitleUploadServer(t)
	code, body := getHTML(t, ts, "/shares")
	if code != http.StatusOK || !strings.Contains(body, "未启用持久化") {
		t.Fatalf("store=nil 应 200 且提示未启用持久化，got %d", code)
	}
}

// TestShareCenterRevoke 从管理页撤销一条分享，PRG 回列表且记录消失。
func TestShareCenterRevoke(t *testing.T) {
	srv, ts, videoID := newPersistTestServer(t)
	sh, err := srv.store.CreateShare(videoID, 24, "")
	if err != nil {
		t.Fatal(err)
	}

	// 正常撤销：不自动跟随，应 303 回 /shares
	form := url.Values{"token": {sh.Token}}
	res, err := noRedirectClient.PostForm(ts.URL+"/shares/delete", form)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/shares" {
		t.Fatalf("撤销应 303 跳 /shares，got %d loc=%q", res.StatusCode, res.Header.Get("Location"))
	}
	if got, _ := srv.store.GetShare(sh.Token); got != nil {
		t.Fatal("撤销后 store 不应再查到")
	}
	// 列表页确认 token 已消失
	if _, body := getHTML(t, ts, "/shares"); strings.Contains(body, sh.Token) {
		t.Fatal("撤销后管理页不应再出现该 token")
	}

	// 缺少 token：400
	bad, err := noRedirectClient.PostForm(ts.URL+"/shares/delete", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺少 token 应 400，got %d", bad.StatusCode)
	}

	// 撤销不存在的 token：404
	miss, err := noRedirectClient.PostForm(ts.URL+"/shares/delete",
		url.Values{"token": {"deadbeefdeadbeefdeadbeefdeadbeef"}})
	if err != nil {
		t.Fatal(err)
	}
	miss.Body.Close()
	if miss.StatusCode != http.StatusNotFound {
		t.Fatalf("撤销不存在的分享应 404，got %d", miss.StatusCode)
	}
}

// TestShareCenterCleanExpired 一键清理只删过期记录，并通过 query 回显条数。
func TestShareCenterCleanExpired(t *testing.T) {
	srv, ts, videoID := newPersistTestServer(t)

	// 一条过期、一条未过期
	expired, err := srv.store.CreateShare(videoID, 24, "")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := srv.store.CreateShare(videoID, 168, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.DB().Exec(
		`UPDATE shares SET expires_at = ? WHERE token = ?`,
		time.Now().UTC().Add(-time.Minute), expired.Token); err != nil {
		t.Fatal(err)
	}

	res, err := noRedirectClient.PostForm(ts.URL+"/shares/clean-expired", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("清理应 303，got %d", res.StatusCode)
	}
	loc := res.Header.Get("Location")
	if loc != "/shares?cleared=1" {
		t.Fatalf("应带 cleared=1 跳回，got %q", loc)
	}
	// 过期记录已删、未过期仍在
	if got, _ := srv.store.GetShare(expired.Token); got != nil {
		t.Fatal("过期分享应已被清理")
	}
	if got, _ := srv.store.GetShare(fresh.Token); got == nil {
		t.Fatal("未过期分享不应被清理")
	}
	// 回显提示
	if _, body := getHTML(t, ts, loc); !strings.Contains(body, "已清理 1 条过期分享") {
		t.Fatal("管理页应显示清理条数提示")
	}

	// 再次清理（已无过期）应 cleared=0 且不显示提示
	res2, err := noRedirectClient.PostForm(ts.URL+"/shares/clean-expired", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.Header.Get("Location") != "/shares?cleared=0" {
		t.Fatalf("无过期可清理时应跳 cleared=0，got %q", res2.Header.Get("Location"))
	}
	if _, body := getHTML(t, ts, "/shares"); strings.Contains(body, "已清理") {
		t.Fatal("cleared=0 时不应显示清理提示")
	}
}

// TestShareCenterExpiredBadge 过期分享在管理页显示"已过期"徽标。
func TestShareCenterExpiredBadge(t *testing.T) {
	srv, ts, videoID := newPersistTestServer(t)
	sh, err := srv.store.CreateShare(videoID, 24, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.DB().Exec(
		`UPDATE shares SET expires_at = ? WHERE token = ?`,
		time.Now().UTC().Add(-time.Minute), sh.Token); err != nil {
		t.Fatal(err)
	}
	// 注意：直接访问公开页会触发惰性清理，所以只访问管理页（管理页不做惰性删除）
	_, body := getHTML(t, ts, "/shares")
	if !strings.Contains(body, "已过期") {
		t.Fatal("过期分享应显示已过期徽标")
	}
}
