package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// 本文件聚焦分享公开链路与管理端 API 的端到端行为。

// createShareViaAPI 通过管理端 API 创建分享并解析响应。
func createShareViaAPI(t *testing.T, base, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("创建分享请求失败: %v", err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// readAllString 读取响应体为字符串。
func readAllString(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	return string(b)
}

// newCookieJar 创建测试用 Cookie 容器。
func newCookieJar() (http.CookieJar, error) { return cookiejar.New(nil) }

// removeFile 删除测试文件，失败即终止。
func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("删除文件失败 %s: %v", path, err)
	}
}

func TestShareCreateValidation(t *testing.T) {
	_, ts, videoID := newPersistTestServer(t)
	base := ts.URL + "/api/v1/videos/" + videoID + "/shares"

	// 非法 hours
	if code, _ := createShareViaAPI(t, base, `{"hours":12,"passcode":""}`); code != http.StatusBadRequest {
		t.Fatalf("非法 hours 应 400，got %d", code)
	}
	// 提取码超长
	longCode := strings.Repeat("a", maxSharePasscodeLen+1)
	if code, _ := createShareViaAPI(t, base, `{"hours":24,"passcode":"`+longCode+`"}`); code != http.StatusBadRequest {
		t.Fatalf("超长提取码应 400，got %d", code)
	}
	// 不存在的视频
	if code, _ := createShareViaAPI(t, ts.URL+"/api/v1/videos/missing/shares", `{"hours":24}`); code != http.StatusNotFound {
		t.Fatalf("不存在视频应 404，got %d", code)
	}
	// 合法创建
	code, out := createShareViaAPI(t, base, `{"hours":168,"passcode":""}`)
	if code != http.StatusCreated {
		t.Fatalf("合法创建应 201，got %d", code)
	}
	if out["token"] == nil || out["hasPasscode"] != false {
		t.Fatalf("响应字段异常: %v", out)
	}
}

func TestShareListAndDelete(t *testing.T) {
	srv, ts, videoID := newPersistTestServer(t)
	base := ts.URL + "/api/v1/videos/" + videoID + "/shares"

	createShareViaAPI(t, base, `{"hours":24,"passcode":"abc"}`)
	createShareViaAPI(t, base, `{"hours":168}`)

	res, err := http.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("列表应 200，got %d", res.StatusCode)
	}
	var out struct {
		Shares []shareView `json:"shares"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Shares) != 2 {
		t.Fatalf("应有 2 条分享，got %d", len(out.Shares))
	}
	// 带码分享标记正确
	foundPasscode := false
	for _, sh := range out.Shares {
		if sh.HasPasscode {
			foundPasscode = true
		}
	}
	if !foundPasscode {
		t.Fatal("应至少有一条带提取码的分享")
	}

	// 撤销
	token := out.Shares[0].Token
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/shares/"+token, nil)
	delRes, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delRes.Body.Close()
	if delRes.StatusCode != http.StatusOK {
		t.Fatalf("撤销应 200，got %d", delRes.StatusCode)
	}
	// 再撤销应 404
	delReq2, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/shares/"+token, nil)
	delRes2, _ := http.DefaultClient.Do(delReq2)
	delRes2.Body.Close()
	if delRes2.StatusCode != http.StatusNotFound {
		t.Fatalf("重复撤销应 404，got %d", delRes2.StatusCode)
	}
	// store 层确认已删
	if sh, _ := srv.store.GetShare(token); sh != nil {
		t.Fatal("撤销后 store 不应再查到")
	}
}

func TestSharePublicFlowWithoutPasscode(t *testing.T) {
	srv, ts, videoID := newPersistTestServer(t)
	sh, err := srv.store.CreateShare(videoID, 24, "")
	if err != nil {
		t.Fatal(err)
	}

	// 落地页 200 且含下载入口
	res, err := http.Get(ts.URL + "/share/" + sh.Token)
	if err != nil {
		t.Fatal(err)
	}
	body := readAllString(t, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "下载文件") {
		t.Fatalf("无码分享页应 200 且含下载按钮，got %d", res.StatusCode)
	}

	// 下载：应返回附件头与文件内容
	dlRes, err := http.Get(ts.URL + "/share/" + sh.Token + "/download")
	if err != nil {
		t.Fatal(err)
	}
	defer dlRes.Body.Close()
	if dlRes.StatusCode != http.StatusOK {
		t.Fatalf("下载应 200，got %d", dlRes.StatusCode)
	}
	if cd := dlRes.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("应强制 attachment，got %q", cd)
	}
	if got := readAllString(t, dlRes.Body); got != "fake-video" {
		t.Fatalf("下载内容不符: %q", got)
	}

	// 不存在的 token 404
	missRes, _ := http.Get(ts.URL + "/share/deadbeefdeadbeefdeadbeefdeadbeef")
	missRes.Body.Close()
	if missRes.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在分享应 404，got %d", missRes.StatusCode)
	}
}

func TestSharePublicFlowWithPasscode(t *testing.T) {
	srv, ts, videoID := newPersistTestServer(t)
	sh, err := srv.store.CreateShare(videoID, 24, "secret123")
	if err != nil {
		t.Fatal(err)
	}
	pageURL := ts.URL + "/share/" + sh.Token

	// 未校验直接下载应 403
	dlRes, _ := http.Get(pageURL + "/download")
	dlRes.Body.Close()
	if dlRes.StatusCode != http.StatusForbidden {
		t.Fatalf("未输提取码下载应 403，got %d", dlRes.StatusCode)
	}

	// 错误提取码：403 且页面报错
	form := url.Values{"passcode": {"wrong"}}
	badRes, err := http.PostForm(pageURL+"/verify", form)
	if err != nil {
		t.Fatal(err)
	}
	badBody := readAllString(t, badRes.Body)
	badRes.Body.Close()
	if badRes.StatusCode != http.StatusForbidden || !strings.Contains(badBody, "提取码错误") {
		t.Fatalf("错码应 403 并提示，got %d", badRes.StatusCode)
	}

	// 正确提取码：302 + Set-Cookie
	jar, _ := newCookieJar()
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // 不自动跟随，先验证 302
		},
	}
	okForm := url.Values{"passcode": {"secret123"}}
	okRes, err := client.PostForm(pageURL+"/verify", okForm)
	if err != nil {
		t.Fatal(err)
	}
	okRes.Body.Close()
	if okRes.StatusCode != http.StatusSeeOther {
		t.Fatalf("正确提取码应 302，got %d", okRes.StatusCode)
	}
	if len(okRes.Cookies()) == 0 {
		t.Fatal("应下发分享 Cookie")
	}

	// 带 Cookie 直接下载成功（自动跟随跳转的普通 client）
	client.CheckRedirect = nil
	dl2, err := client.Get(pageURL + "/download")
	if err != nil {
		t.Fatal(err)
	}
	defer dl2.Body.Close()
	if dl2.StatusCode != http.StatusOK || readAllString(t, dl2.Body) != "fake-video" {
		t.Fatalf("Cookie 校验后下载应成功，got %d", dl2.StatusCode)
	}

	// 查询参数提取码也可直接下载（下载工具场景）
	dl3, err := http.Get(pageURL + "/download?passcode=secret123")
	if err != nil {
		t.Fatal(err)
	}
	defer dl3.Body.Close()
	if dl3.StatusCode != http.StatusOK {
		t.Fatalf("query 提取码下载应 200，got %d", dl3.StatusCode)
	}
	// 错码 query 仍 403
	dl4, _ := http.Get(pageURL + "/download?passcode=nope")
	dl4.Body.Close()
	if dl4.StatusCode != http.StatusForbidden {
		t.Fatalf("错误 query 提取码应 403，got %d", dl4.StatusCode)
	}
}

func TestShareExpiredLazyCleanup(t *testing.T) {
	srv, ts, videoID := newPersistTestServer(t)
	sh, err := srv.store.CreateShare(videoID, 24, "")
	if err != nil {
		t.Fatal(err)
	}
	// 手动把过期时间改到过去
	if _, err := srv.store.DB().Exec(
		`UPDATE shares SET expires_at = ? WHERE token = ?`,
		time.Now().UTC().Add(-time.Minute), sh.Token); err != nil {
		t.Fatal(err)
	}

	res, _ := http.Get(ts.URL + "/share/" + sh.Token)
	res.Body.Close()
	if res.StatusCode != http.StatusGone {
		t.Fatalf("过期分享应 410，got %d", res.StatusCode)
	}
	// 惰性删除已生效
	if got, _ := srv.store.GetShare(sh.Token); got != nil {
		t.Fatal("过期记录应已被惰性清理")
	}
}

func TestShareMissingVideo(t *testing.T) {
	srv, ts, videoID := newPersistTestServer(t)
	sh, err := srv.store.CreateShare(videoID, 24, "")
	if err != nil {
		t.Fatal(err)
	}
	// 删掉媒体文件后重扫，视频从库中消失
	v, ok := srv.library.Get(videoID)
	if !ok {
		t.Fatal("视频应存在")
	}
	removeFile(t, v.AbsPath())
	if _, err := srv.library.Scan(); err != nil {
		t.Fatal(err)
	}
	res, _ := http.Get(ts.URL + "/share/" + sh.Token)
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("视频已删除的分享应 404，got %d", res.StatusCode)
	}
	if got, _ := srv.store.GetShare(sh.Token); got != nil {
		t.Fatal("悬空分享应被同步删除")
	}
}
