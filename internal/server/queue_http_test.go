package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestWatchQueuePanelRendered 播放页始终输出队列面板、脚本与当前视频元信息，
// 队列本身是纯前端 localStorage 数据，服务端只提供挂载点。
func TestWatchQueuePanelRendered(t *testing.T) {
	srv, ids := newRelatedTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/watch/"+ids["剧A/剧A.S01E01.mkv"])
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	containsAll(t, html,
		`data-queue-panel`,
		`data-queue-list`,
		`data-queue-count`,
		`data-queue-clear`,
		`id="queue-toast"`,
		`/static/js/queue.js`,
		`data-watch-id="`+ids["剧A/剧A.S01E01.mkv"]+`"`,
		`data-watch-name="`,
	)
}

// TestWatchQueueAddButtons 相关视频卡片带「＋ 队列」按钮，数量与相关卡片一致且带视频名。
func TestWatchQueueAddButtons(t *testing.T) {
	srv, ids := newRelatedTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/watch/"+ids["剧A/剧A.S01E01.mkv"])
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	// 同目录 2 个伙伴 → 2 个入队按钮，且各自携带目标视频 ID
	if got := strings.Count(html, "data-queue-add"); got != 2 {
		t.Errorf("入队按钮数 = %d，期望 2", got)
	}
	containsAll(t, html,
		`data-id="`+ids["剧A/剧A.S01E02.mkv"]+`"`,
		`data-id="`+ids["剧A/剧A.S01E03.mkv"]+`"`,
	)
}

// TestWatchQueueNoRelated 无同目录伙伴时仍有队列面板，但没有入队按钮。
func TestWatchQueueNoRelated(t *testing.T) {
	srv, ids := newRelatedTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	code, html := getHTML(t, ts, "/watch/"+ids["电影B/单片.mkv"])
	if code != http.StatusOK {
		t.Fatalf("播放页状态码 = %d", code)
	}
	if !strings.Contains(html, "data-queue-panel") {
		t.Error("无相关视频时队列面板仍应渲染（JS 按非空显示）")
	}
	if strings.Contains(html, "data-queue-add") {
		t.Error("无相关视频时不应出现入队按钮")
	}
}

// TestWatchQueueStaticServed 队列脚本作为静态资源可访问。
func TestWatchQueueStaticServed(t *testing.T) {
	srv, _ := newRelatedTestServer(t)
	ts := newHTTPTestServer(t, srv.Handler())

	resp := get(t, ts, "/static/js/queue.js")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("queue.js 状态码 = %d，期望 200", resp.StatusCode)
	}
}
