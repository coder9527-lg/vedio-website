package media

import (
	"testing"
	"time"
)

// StopSession 对不存在的会话应返回 false，且不 panic。
func TestStopSessionMissing(t *testing.T) {
	tc, err := NewTranscoder(t.TempDir(), "/nonexistent/ffmpeg-bin", "ffprobe", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()

	if tc.StopSession("no-such-video", Quality720p) {
		t.Error("不存在的会话 StopSession 应返回 false")
	}
	// 管理快照在转码器不可用时仍应可安全调用，返回空会话列表
	snap := tc.ManagementSnapshot()
	if snap.Available {
		t.Error("伪造 ffmpeg 路径时 Available 应为 false")
	}
	if len(snap.Sessions) != 0 {
		t.Errorf("无会话时期望 0 条，实际 %d", len(snap.Sessions))
	}
	if snap.MaxConcurrent != 1 {
		t.Errorf("MaxConcurrent 期望 1，实际 %d", snap.MaxConcurrent)
	}
}

// 真实 ffmpeg 环境下验证管理快照能采集到会话、产物占用，且 StopSession 能摘除会话。
func TestManagementSnapshotAndStop(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("环境无 ffmpeg，跳过转码管理集成测试")
	}
	mediaDir := t.TempDir()
	videoPath := mediaDir + "/clip.mkv"
	makeTestVideo(t, videoPath, 6)

	lib := NewLibrary(mediaDir, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	v := lib.List()[0]

	root := t.TempDir()
	tc, err := NewTranscoder(root, "ffmpeg", "ffprobe", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()

	sess, err := tc.Ensure(v, Quality720p)
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	// 等待转码完成，确保产物落盘、字节统计非零
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if st := sess.Status(); st.State == "done" && st.Segments > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	snap := tc.ManagementSnapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("期望 1 个会话快照，实际 %d", len(snap.Sessions))
	}
	view := snap.Sessions[0]
	if view.VideoID != v.ID || view.VideoName != v.Name {
		t.Errorf("会话视频信息不匹配: id=%s name=%s", view.VideoID, view.VideoName)
	}
	if view.Quality != Quality720p || view.State != "done" {
		t.Errorf("档位/状态不匹配: q=%s state=%s", view.Quality, view.State)
	}
	if view.Segments <= 0 {
		t.Error("完成态会话分片数应大于 0")
	}
	if view.Bytes <= 0 {
		t.Error("完成态会话产物占用应大于 0 字节")
	}
	if snap.TotalBytes < view.Bytes {
		t.Errorf("根目录总占用 %d 不应小于单会话 %d", snap.TotalBytes, view.Bytes)
	}
	if view.LastAccess == "" {
		t.Error("LastAccess 不应为空")
	}

	// 终止并清理后，会话应从 map 摘除，产物目录被删除
	if !tc.StopSession(v.ID, Quality720p) {
		t.Fatal("存在的会话 StopSession 应返回 true")
	}
	if tc.StopSession(v.ID, Quality720p) {
		t.Error("重复 StopSession 应返回 false")
	}
	if got := len(tc.ManagementSnapshot().Sessions); got != 0 {
		t.Errorf("终止后期望 0 个会话，实际 %d", got)
	}
}
