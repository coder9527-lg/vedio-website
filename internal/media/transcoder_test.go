package media

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ffmpegAvailable 判断测试环境是否具备 ffmpeg，缺失则跳过转码集成测试。
func ffmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// makeTestVideo 用 ffmpeg lavfi 源生成一个指定时长的测试视频（无需外部素材）。
func makeTestVideo(t *testing.T, path string, seconds int) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration="+itoa(seconds),
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+itoa(seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac",
		path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("生成测试视频失败: %v\n%s", err, out)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestSegmentPathValidation(t *testing.T) {
	dir := t.TempDir()
	sess := newSession(&Video{ID: "abc"}, dir)

	// 放一个合法分片
	valid := filepath.Join(dir, "seg-00001.ts")
	if err := os.WriteFile(valid, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := sess.SegmentPath("seg-00001.ts")
	if err != nil || got != valid {
		t.Fatalf("合法分片应返回路径，got=%q err=%v", got, err)
	}

	for _, bad := range []string{
		"../seg-00001.ts",  // 路径穿越
		"seg-1.ts",         // 编号位数不对
		"seg-00001.ts.tmp", // 临时文件
		"ffmpeg.log",       // 非分片
		"seg-00001.txt",    // 错误后缀
		"seg-.ts",          // 无编号
	} {
		if _, err := sess.SegmentPath(bad); err == nil {
			t.Errorf("非法分片名 %q 应被拒绝", bad)
		}
	}
	if _, err := sess.SegmentPath("seg-00099.ts"); err == nil {
		t.Error("不存在的分片应返回错误")
	}
}

func TestTranscoderEndToEnd(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("环境无 ffmpeg，跳过转码集成测试")
	}
	mediaDir := t.TempDir()
	videoPath := filepath.Join(mediaDir, "clip.mkv")
	makeTestVideo(t, videoPath, 6)

	lib := NewLibrary(mediaDir, DefaultExts)
	if n, err := lib.Scan(); err != nil || n != 1 {
		t.Fatalf("扫描失败 n=%d err=%v", n, err)
	}
	v := lib.List()[0]

	tc, err := NewTranscoder(t.TempDir(), "ffmpeg", "ffprobe", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()

	if !tc.Available() {
		t.Fatal("ffmpeg 可用时 Available 应为 true")
	}

	sess, err := tc.Ensure(v)
	if err != nil {
		t.Fatalf("首次 Ensure 失败: %v", err)
	}

	// 轮询直到完成（短视频应很快）
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		st := sess.Status()
		if st.State == "failed" {
			t.Fatal("转码会话进入 failed 状态")
		}
		if st.State == "done" && st.HasPlaylist && st.Segments > 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	st := sess.Status()
	if st.State != "done" || !st.HasPlaylist || st.Segments == 0 {
		t.Fatalf("转码未按预期完成: %+v", st)
	}

	// m3u8 与分片都能取到
	pl, err := sess.PlaylistPath()
	if err != nil {
		t.Fatalf("播放列表不存在: %v", err)
	}
	if _, err := os.Stat(pl); err != nil {
		t.Fatalf("播放列表文件缺失: %v", err)
	}
	first := "seg-00000.ts"
	sp, err := sess.SegmentPath(first)
	if err != nil {
		t.Fatalf("首个分片不可用: %v", err)
	}
	if info, err := os.Stat(sp); err != nil || info.Size() == 0 {
		t.Fatalf("首个分片异常: err=%v", err)
	}

	// 完成后再次 Ensure 应直接复用，不再启动新进程
	sess2, err := tc.Ensure(v)
	if err != nil || sess2 != sess {
		t.Fatalf("完成态会话应被复用，err=%v same=%v", err, sess2 == sess)
	}
	if got := sess2.Status().State; got != "done" {
		t.Fatalf("复用后状态应为 done，got=%s", got)
	}
}

func TestTranscoderBusyAndQueue(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("环境无 ffmpeg，跳过转码集成测试")
	}
	mediaDir := t.TempDir()
	// 两个稍长视频，保证第一个转码期间第二个会排队
	makeTestVideo(t, filepath.Join(mediaDir, "a.mkv"), 20)
	makeTestVideo(t, filepath.Join(mediaDir, "b.mkv"), 20)

	lib := NewLibrary(mediaDir, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	videos := lib.List()
	var a, b *Video
	for _, v := range videos {
		switch v.Name {
		case "a":
			a = v
		case "b":
			b = v
		}
	}

	tc, err := NewTranscoder(t.TempDir(), "ffmpeg", "ffprobe", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()

	if _, err := tc.Ensure(a); err != nil {
		t.Fatalf("启动第一个转码失败: %v", err)
	}
	// 等第一个真正跑起来
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && tc.Get(a.ID).Status().State != "running" {
		time.Sleep(100 * time.Millisecond)
	}
	if tc.Get(a.ID).Status().State != "running" {
		t.Fatal("第一个会话未进入 running")
	}

	// 第二个应立即 busy 并入队
	if _, err := tc.Ensure(b); err != ErrTranscoderBusy {
		t.Fatalf("槽位满应返回 ErrTranscoderBusy，got=%v", err)
	}
	// 入队后再请求不应再报 busy，而是返回 starting 会话
	sessB, err := tc.Ensure(b)
	if err != nil {
		t.Fatalf("排队会话再次请求应直接返回，got=%v", err)
	}
	if got := sessB.Status().State; got != "starting" {
		t.Fatalf("排队会话状态应为 starting，got=%s", got)
	}
}

func TestAssetPathValidation(t *testing.T) {
	dir := t.TempDir()
	sess := newSession(&Video{ID: "abc"}, dir)

	// 简单布局文件
	for _, name := range []string{"seg-00001.ts", "index.m3u8"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 多音轨变体布局文件
	for _, name := range []string{
		"master.m3u8", "out-0.m3u8", "out-a0.m3u8", "out-a1.m3u8",
		"out-0-00000.ts", "out-a0-00000.ts", "out-a1-00001.ts",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	valid := []string{
		"index.m3u8", "master.m3u8", "seg-00001.ts",
		"out-0.m3u8", "out-a0.m3u8",
		"out-0-00000.ts", "out-a1-00001.ts",
	}
	for _, name := range valid {
		if _, err := sess.AssetPath(name); err != nil {
			t.Errorf("合法资源 %q 应放行，got err=%v", name, err)
		}
	}

	for _, bad := range []string{
		"../master.m3u8",  // 路径穿越
		"ffmpeg.log",      // 日志不可读
		"seg-1.ts",        // 编号位数不对
		"out-a0.ts",       // 变体分片必须带 5 位编号
		"out-.m3u8",       // 变体名为空
		"out-a0/evil.ts",  // 子目录
		"out-a0-0001.vtt", // 错误后缀
	} {
		if _, err := sess.AssetPath(bad); err == nil {
			t.Errorf("非法资源名 %q 应被拒绝", bad)
		}
	}
}

func TestRewriteMasterNames(t *testing.T) {
	dir := t.TempDir()
	master := filepath.Join(dir, hlsMasterName)
	orig := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:6",
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="group_aud",NAME="audio_1",DEFAULT=YES,URI="out-a0.m3u8"`,
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="group_aud",NAME="audio_2",DEFAULT=NO,URI="out-a1.m3u8"`,
		`#EXT-X-STREAM-INF:BANDWIDTH=140800,AUDIO="group_aud"`,
		"out-0.m3u8",
		"",
	}, "\n")
	if err := os.WriteFile(master, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := &Transcoder{}
	tracks := []AudioTrack{
		{Label: "国语", Default: true},
		{Label: "English"},
	}
	tc.rewriteOnce(master, tracks)

	got, err := os.ReadFile(master)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, `NAME="国语"`) || !strings.Contains(text, `NAME="English"`) {
		t.Fatalf("NAME 未被替换为友好音轨名:\n%s", text)
	}
	// URI 与其余属性必须保持不变
	if !strings.Contains(text, `URI="out-a0.m3u8"`) || !strings.Contains(text, `DEFAULT=YES`) {
		t.Fatalf("URI/属性被意外修改:\n%s", text)
	}
	if strings.Contains(text, "audio_1") {
		t.Fatalf("旧名称 audio_N 应消失:\n%s", text)
	}
}

func TestTranscoderMultiAudio(t *testing.T) {
	if !ffmpegAvailable() || !ffprobeAvailable() {
		t.Skip("环境无 ffmpeg/ffprobe，跳过多音轨转码测试")
	}
	mediaDir := t.TempDir()
	videoPath := filepath.Join(mediaDir, "multi.mkv")
	makeMultiAudioVideo(t, videoPath, 8)

	lib := NewLibrary(mediaDir, DefaultExts)
	if _, err := lib.Scan(); err != nil {
		t.Fatal(err)
	}
	v := lib.List()[0]

	tc, err := NewTranscoder(t.TempDir(), "ffmpeg", "ffprobe", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()

	sess, err := tc.Ensure(v)
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		st := sess.Status()
		if st.State == "failed" {
			t.Fatal("转码会话进入 failed 状态")
		}
		if st.State == "done" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	st := sess.Status()
	if !st.MultiAudio {
		t.Fatalf("双音轨视频应进入多音轨布局，status=%+v", st)
	}
	if len(st.AudioTracks) != 2 {
		t.Fatalf("状态应返回 2 路音轨，got %d", len(st.AudioTracks))
	}

	// 入口播放列表应指向 master
	pl, err := sess.PlaylistPath()
	if err != nil {
		t.Fatalf("master 播放列表不存在: %v", err)
	}
	if filepath.Base(pl) != hlsMasterName {
		t.Fatalf("多音轨入口应为 %s，got %s", hlsMasterName, pl)
	}

	// master 引用的变体与分片都应能通过白名单取到
	for _, name := range []string{"out-0.m3u8", "out-a0.m3u8", "out-a1.m3u8",
		"out-0-00000.ts", "out-a0-00000.ts", "out-a1-00000.ts"} {
		if _, err := sess.AssetPath(name); err != nil {
			t.Errorf("多音轨资源 %q 应可访问: %v", name, err)
		}
	}
}

func TestTranscoderUnavailable(t *testing.T) {
	tc, err := NewTranscoder(t.TempDir(), "/nonexistent/ffmpeg-bin", "/nonexistent/ffprobe-bin", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	if tc.Available() {
		t.Fatal("不存在的 ffmpeg 路径应判定为不可用")
	}
	if _, err := tc.Ensure(&Video{ID: "x"}); err != ErrTranscoderDisabled {
		t.Fatalf("ffmpeg 不可用时应返回 ErrTranscoderDisabled，got=%v", err)
	}
}
