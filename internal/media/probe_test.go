package media

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func ffprobeAvailable() bool {
	_, err := exec.LookPath("ffprobe")
	return err == nil
}

// makeMultiAudioVideo 生成含两路音轨的测试视频（分别标记 chi/eng）。
func makeMultiAudioVideo(t *testing.T, path string, seconds int) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration="+itoa(seconds),
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+itoa(seconds),
		"-f", "lavfi", "-i", "sine=frequency=880:duration="+itoa(seconds),
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-metadata:s:a:0", "language=chi", "-metadata:s:a:0", "title=国语",
		"-metadata:s:a:1", "language=eng",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac",
		path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("生成多音轨测试视频失败: %v\n%s", err, out)
	}
}

func TestProbeAudio(t *testing.T) {
	if !ffprobeAvailable() {
		t.Skip("环境无 ffprobe，跳过探测测试")
	}
	mediaDir := t.TempDir()
	videoPath := filepath.Join(mediaDir, "multi.mkv")
	makeMultiAudioVideo(t, videoPath, 4)

	tracks, err := ProbeAudio("ffprobe", videoPath)
	if err != nil {
		t.Fatalf("ProbeAudio 失败: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("期望 2 路音轨，实际 %d", len(tracks))
	}
	if tracks[0].Language != "zh" || tracks[0].Title != "国语" {
		t.Errorf("第一音轨元信息异常: %+v", tracks[0])
	}
	if tracks[1].Language != "en" {
		t.Errorf("第二音轨语言应为 en，got %q", tracks[1].Language)
	}
	if !tracks[0].Default || tracks[1].Default {
		t.Errorf("仅第一音轨应标记默认: %+v", tracks)
	}
	if tracks[0].Label != "国语" {
		t.Errorf("有标题时 Label 应取标题，got %q", tracks[0].Label)
	}
}

func TestProbeSubtitles(t *testing.T) {
	if !ffprobeAvailable() || !ffmpegAvailable() {
		t.Skip("环境无 ffmpeg/ffprobe，跳过内嵌字幕探测测试")
	}
	mediaDir := t.TempDir()
	srtPath := filepath.Join(mediaDir, "in.srt")
	assPath := filepath.Join(mediaDir, "in.ass")
	videoPath := filepath.Join(mediaDir, "subbed.mkv")
	if err := os.WriteFile(srtPath, []byte("1\n00:00:01,000 --> 00:00:03,000\n内嵌中文字幕\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assPath, []byte("[Script Info]\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:02.00,0:00:04.00,Default,,0,0,0,,ASS English line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=6",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-i", srtPath, "-i", assPath,
		"-map", "0:v", "-map", "1:a", "-map", "2:s", "-map", "3:s",
		"-metadata:s:s:0", "language=chi", "-metadata:s:s:0", "title=中字",
		"-metadata:s:s:1", "language=eng",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-c:s", "srt",
		videoPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("生成内嵌字幕测试视频失败: %v\n%s", err, out)
	}

	tracks, err := ProbeSubtitles("ffprobe", videoPath)
	if err != nil {
		t.Fatalf("ProbeSubtitles 失败: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("期望 2 路内嵌字幕，实际 %d", len(tracks))
	}
	for _, tr := range tracks {
		if !tr.Text || tr.Bitmap {
			t.Errorf("SRT/ASS 应为可提取文本轨: %+v", tr)
		}
	}
	if tracks[0].Language != "zh" || tracks[0].Title != "中字" || !tracks[0].Default {
		t.Errorf("第一字幕轨元信息异常: %+v", tracks[0])
	}
	if tracks[1].Language != "en" {
		t.Errorf("第二字幕轨语言应为 en，got %q", tracks[1].Language)
	}
}

func TestProbeAudioMissingFile(t *testing.T) {
	if _, err := ProbeAudio("ffprobe", filepath.Join(t.TempDir(), "nope.mkv")); err == nil {
		t.Error("探测不存在的文件应返回错误")
	}
}
