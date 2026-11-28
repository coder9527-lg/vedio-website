package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestServiceWorkerRootRoute 锁死第二十轮 PWA 基座的 Service Worker 根路由契约：
// 物理文件在 /static/ 下，但必须根路径挂载并带 Service-Worker-Allowed 头才能管控整站。
func TestServiceWorkerRootRoute(t *testing.T) {
	ts, _, _ := newSubtitleUploadServer(t)

	resp, err := http.Get(ts.URL + "/sw.js")
	if err != nil {
		t.Fatalf("GET /sw.js 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/sw.js 状态码 = %d，期望 200", resp.StatusCode)
	}
	ctype := resp.Header.Get("Content-Type")
	if !strings.Contains(ctype, "javascript") {
		t.Errorf("Content-Type = %q，期望含 javascript", ctype)
	}
	if got := resp.Header.Get("Service-Worker-Allowed"); got != "/" {
		t.Errorf("Service-Worker-Allowed = %q，期望 /", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取 sw.js 失败: %v", err)
	}
	html := string(body)
	for _, want := range []string{
		// 缓存版本化与生命周期
		`vw-cache-v10`,
		`caches.open`,
		`skipWaiting`,
		`clients.claim`,
		// 媒体流与鉴权内容不缓存
		`'/stream/'`,
		`'/hls/'`,
		// 导航 network-first 与静态 SWR 策略
		`request.mode === 'navigate'`,
		`/static/offline.html`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("sw.js 缺少契约内容 %q", want)
		}
	}
}

// TestPWAStaticAssets 验证 manifest 与离线页经 embed 静态服务可访问且内容正确。
func TestPWAStaticAssets(t *testing.T) {
	ts, _, _ := newSubtitleUploadServer(t)

	resp, err := http.Get(ts.URL + "/static/manifest.webmanifest")
	if err != nil {
		t.Fatalf("GET manifest 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest 状态码 = %d", resp.StatusCode)
	}
	if ctype := resp.Header.Get("Content-Type"); !strings.Contains(ctype, "manifest") {
		t.Errorf("manifest Content-Type = %q，期望含 manifest", ctype)
	}
	manifest, _ := io.ReadAll(resp.Body)
	ms := string(manifest)
	for _, want := range []string{
		`"display": "standalone"`,
		`"start_url": "/"`,
		`"scope": "/"`,
		`/static/icon.svg`,
		`#0f172a`,
	} {
		if !strings.Contains(ms, want) {
			t.Errorf("manifest 缺少 %q", want)
		}
	}

	offResp, err := http.Get(ts.URL + "/static/offline.html")
	if err != nil {
		t.Fatalf("GET offline.html 失败: %v", err)
	}
	defer offResp.Body.Close()
	if offResp.StatusCode != http.StatusOK {
		t.Fatalf("offline.html 状态码 = %d", offResp.StatusCode)
	}

	// pwa.js 注册脚本本身也要可访问
	pwaResp, err := http.Get(ts.URL + "/static/js/pwa.js")
	if err != nil {
		t.Fatalf("GET pwa.js 失败: %v", err)
	}
	defer pwaResp.Body.Close()
	if pwaResp.StatusCode != http.StatusOK {
		t.Fatalf("pwa.js 状态码 = %d", pwaResp.StatusCode)
	}
	pwaJS, _ := io.ReadAll(pwaResp.Body)
	ps := string(pwaJS)
	if !strings.Contains(ps, `'serviceWorker' in navigator`) {
		t.Error("pwa.js 缺少能力守卫")
	}
	if !strings.Contains(ps, `register('/sw.js')`) {
		t.Error("pwa.js 缺少根作用域注册调用")
	}
}

// TestPWATemplateInjection 验证 PWA 头注入到关键页面（首页与播放页代表公开/播放两类）。
func TestPWATemplateInjection(t *testing.T) {
	ts, _, vid := newSubtitleUploadServer(t)

	for _, path := range []string{"/", "/watch/" + vid} {
		code, html := getHTML(t, ts, path)
		if code != http.StatusOK {
			t.Fatalf("GET %s 状态码 = %d", path, code)
		}
		for _, want := range []string{
			`<link rel="manifest" href="/static/manifest.webmanifest">`,
			`<meta name="theme-color" content="#0f172a">`,
			`<link rel="apple-touch-icon" href="/static/icon.svg">`,
			`apple-mobile-web-app-capable`,
			`apple-mobile-web-app-title`,
			`<script src="/static/js/pwa.js" defer></script>`,
		} {
			if !strings.Contains(html, want) {
				t.Errorf("页面 %s 缺少 PWA 注入 %q", path, want)
			}
		}
	}
}
