package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestWatchPageRotationInjection 锁死第二十轮阶段三画面旋转的前端契约：
// 纯浏览器行为、与持久化无关（store=nil 夹具），服务端只验证注入锚点。
func TestWatchPageRotationInjection(t *testing.T) {
	ts, _, vid := newSubtitleUploadServer(t)
	code, html := getHTML(t, ts, "/watch/"+vid)
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	for _, want := range []string{
		// 菜单四档 DOM 与分隔线
		`data-rot="0"`,
		`data-rot="90"`,
		`data-rot="180"`,
		`data-rot="270"`,
		`class="track-sep"`,
		// 旋转白名单与默认值
		`const ROTATIONS = [0, 90, 180, 270]`,
		`const ROT_DEFAULT = 0`,
		// transform 驱动与 90/270 宽高互换布局
		`rotate(${deg}deg)`,
		`function layoutRotated`,
		`player.videoWidth`,
		// 偏好并入同一 vw:prefs
		`window.PlayerPrefs.save({ videoRot: deg })`,
		`const ROTS = [0, 90, 180, 270]`,
		`prefs.videoRot = obj.videoRot`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("播放页缺少画面旋转注入内容 %q", want)
		}
	}

	// 旋转逻辑位于画面缩放 IIFE 段内：截取该段验证不引用持久化接口
	start := strings.Index(html, "const FITS = ['contain', 'cover', 'fill'];")
	if start < 0 {
		t.Fatal("未找到画面缩放脚本段")
	}
	end := strings.Index(html[start:], "</script>")
	seg := html[start : start+end]
	if strings.Contains(seg, "/api/v1/") {
		t.Error("画面旋转脚本段不应引用任何 /api/v1/ 持久化接口")
	}
}

// TestMobileCSSAndControlBar 验证窄屏适配样式经静态服务可访问且包含关键规则。
func TestMobileCSSAndControlBar(t *testing.T) {
	ts, _, _ := newSubtitleUploadServer(t)
	resp, err := http.Get(ts.URL + "/static/css/style.css")
	if err != nil {
		t.Fatalf("GET style.css 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("style.css 状态码 = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	css := string(body)
	for _, want := range []string{
		// 窄屏控制栏横向滑动收纳
		`@media (max-width: 640px)`,
		`.player-controls {`,
		`flex-wrap: nowrap`,
		`overflow-x: auto`,
		`.player-controls::-webkit-scrollbar { display: none; }`,
		// 弹窗全屏化
		`.scrape-dialog {`,
		`max-height: 100vh`,
		`height: 100vh`,
		`border-radius: 0`,
		// 旋转分隔线与过渡
		`.track-sep {`,
		`#player { transition: transform .2s ease; }`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("样式表缺少窄屏/旋转规则 %q", want)
		}
	}
}
