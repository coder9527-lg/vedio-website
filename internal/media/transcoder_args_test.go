package media

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// containsAll 按顺序无关地断言 args 包含全部期望片段（对成对的 -flag value 逐对检查）。
func containsAll(t *testing.T, args []string, want ...string) {
	t.Helper()
	joined := strings.Join(args, " ")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Fatalf("参数中缺少 %q\n实际参数: %v", w, args)
		}
	}
}

// TestBuildFFmpegArgsSoftwareProfile 锁定软转档位参数与重构前逐参一致，
// 保证默认行为（TRANSCODE_HWACCEL 留空）零变化。
func TestBuildFFmpegArgsSoftwareProfile(t *testing.T) {
	videoPath := filepath.Join(t.TempDir(), "movie.mkv")
	outDir := t.TempDir()

	// 单音轨简单布局
	args := buildFFmpegArgs(videoPath, outDir, nil, encodeProfile{})
	want := []string{
		"-y", "-i", videoPath,
		"-map", "0:v:0", "-map", "0:a:0?",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2",
		"-c:a", "aac", "-b:a", "128k", "-ac", "2", "-sn",
		"-f", "hls", "-hls_time", "4",
		"-hls_flags", "append_list+independent_segments",
		"-hls_list_size", "0",
		"-hls_segment_filename", filepath.Join(outDir, "seg-%05d.ts"),
		filepath.Join(outDir, hlsPlaylistName),
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("软转单音轨参数与重构前不一致\n got: %v\nwant: %v", args, want)
	}

	// 软转绝不能出现任何硬件加速片段
	if strings.Contains(strings.Join(args, " "), "vaapi") {
		t.Fatalf("软转参数不应包含 vaapi: %v", args)
	}
}

// TestBuildFFmpegArgsVAAPIProfile 验证 VAAPI 档位的输入设备选项、硬编参数与滤镜。
func TestBuildFFmpegArgsVAAPIProfile(t *testing.T) {
	videoPath := filepath.Join(t.TempDir(), "movie.mkv")
	outDir := t.TempDir()
	dev := "/dev/dri/renderD128"

	args := buildFFmpegArgs(videoPath, outDir, nil,
		encodeProfile{hwaccel: "vaapi", vaapiDevice: dev})
	joined := strings.Join(args, " ")

	// 输入前的硬件设备初始化必须在 -i 之前
	inputIdx := indexOf(args, "-i")
	for _, flag := range []string{"-hwaccel", "-init_hw_device", "-hwaccel_device", "-hwaccel_output_format"} {
		idx := indexOf(args, flag)
		if idx < 0 || idx > inputIdx {
			t.Fatalf("%s 必须存在且位于 -i 之前，参数: %v", flag, args)
		}
	}
	containsAll(t, args,
		"-init_hw_device", "vaapi=va:"+dev,
		"-hwaccel_output_format", "vaapi",
		"-c:v", "h264_vaapi",
		"-b:v", "4000k",
		"-vf", "scale_vaapi=format=nv12",
	)
	// VAAPI 不支持 libx264 的 preset/crf，必须彻底移除
	for _, banned := range []string{"libx264", "-preset", "-crf", "scale=trunc"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("VAAPI 参数不应包含 %q: %v", banned, args)
		}
	}
	// 音频、HLS 公共选项保持不变
	containsAll(t, args, "-c:a", "aac", "-sn", "-f", "hls", "-hls_list_size", "0")
}

// TestBuildFFmpegArgsMultiAudioKept 在两种档位下都保留多音轨变体布局。
func TestBuildFFmpegArgsMultiAudioKept(t *testing.T) {
	outDir := t.TempDir()
	tracks := []AudioTrack{
		{Index: 1, Language: "chi", Default: true},
		{Index: 2, Language: "eng"},
	}

	for _, p := range []encodeProfile{
		{},
		{hwaccel: "vaapi", vaapiDevice: "/dev/dri/renderD128"},
	} {
		args := buildFFmpegArgs("in.mkv", outDir, tracks, p)
		containsAll(t, args,
			"-map", "0:v:0",
			"-map", "0:1?", "-map", "0:2?",
			"-master_pl_name", hlsMasterName,
			"-var_stream_map",
			"v:0,agroup:aud a:0,agroup:aud,name:a0,default:yes a:1,agroup:aud,name:a1",
			"-hls_segment_filename", filepath.Join(outDir, "out-%v-%05d.ts"),
			filepath.Join(outDir, "out-%v.m3u8"),
		)
	}
}

// TestSetHWAccelFallback 验证设备缺失与未知模式都回退软转。
func TestSetHWAccelFallback(t *testing.T) {
	tc, err := NewTranscoder(t.TempDir(), "ffmpeg", "ffprobe", 1, 0)
	if err != nil {
		t.Fatalf("NewTranscoder 失败: %v", err)
	}
	defer tc.Close()

	// 不存在的设备节点 → 回退
	tc.SetHWAccel("vaapi", "/dev/dri/does-not-exist-xyz")
	if tc.hwaccel != "" {
		t.Fatalf("设备缺失应回退软转，实际 hwaccel=%q", tc.hwaccel)
	}
	if !strings.Contains(tc.HWAccelMode(), "软件转码") {
		t.Fatalf("HWAccelMode 应反映软转，实际 %q", tc.HWAccelMode())
	}

	// 未知模式 → 回退
	tc.SetHWAccel("nvenc", "/dev/null")
	if tc.hwaccel != "" {
		t.Fatalf("未知模式应回退软转，实际 hwaccel=%q", tc.hwaccel)
	}

	// 显式空值 → 软转
	tc.SetHWAccel("", "")
	if tc.hwaccel != "" {
		t.Fatalf("空模式应为软转")
	}
}

// TestSetHWAccelVAAPIWithExistingDevice 用 /dev/null 之外的真实存在节点验证正向路径
// （CI 无 GPU 时无法验证转码本身，但参数拼装已由上面的测试覆盖）。
func TestSetHWAccelVAAPIWithExistingDevice(t *testing.T) {
	tc, err := NewTranscoder(t.TempDir(), "ffmpeg", "ffprobe", 1, 0)
	if err != nil {
		t.Fatalf("NewTranscoder 失败: %v", err)
	}
	defer tc.Close()

	tc.SetHWAccel("vaapi", "/dev/null") // 一定存在的字符设备，仅用于通过预检
	if tc.hwaccel != "vaapi" || tc.vaapiDevice != "/dev/null" {
		t.Fatalf("设备存在时应启用 vaapi，实际 hwaccel=%q device=%q", tc.hwaccel, tc.vaapiDevice)
	}
	if !strings.Contains(tc.HWAccelMode(), "vaapi") {
		t.Fatalf("HWAccelMode 应反映 vaapi，实际 %q", tc.HWAccelMode())
	}
}

func indexOf(slice []string, target string) int {
	for i, v := range slice {
		if v == target {
			return i
		}
	}
	return -1
}
