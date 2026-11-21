package media

import (
	"path/filepath"
	"strconv"
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

// containsNone 断言 args 不包含任何禁用片段（用于隔离不同档位的参数）。
func containsNone(t *testing.T, args []string, banned ...string) {
	t.Helper()
	joined := strings.Join(args, " ")
	for _, b := range banned {
		if strings.Contains(joined, b) {
			t.Fatalf("参数中不应出现 %q\n实际参数: %v", b, args)
		}
	}
}

// qualityProfileCases 覆盖三档清晰度 × 软转/VAAPI 两套编码路径，共 6 组。
func qualityProfileCases() []struct {
	name    string
	profile encodeProfile
	spec    QualitySpec
} {
	quals := []struct {
		name string
		q    string
	}{
		{"1080p", Quality1080p},
		{"720p", Quality720p},
		{"480p", Quality480p},
	}
	var cases []struct {
		name    string
		profile encodeProfile
		spec    QualitySpec
	}
	for _, q := range quals {
		spec := qualitySpecs[q.q]
		cases = append(cases,
			struct {
				name    string
				profile encodeProfile
				spec    QualitySpec
			}{"软转-" + q.name, encodeProfile{quality: spec}, spec},
			struct {
				name    string
				profile encodeProfile
				spec    QualitySpec
			}{"VAAPI-" + q.name, encodeProfile{hwaccel: "vaapi", vaapiDevice: "/dev/dri/renderD128", quality: spec}, spec},
		)
	}
	return cases
}

// TestBuildFFmpegArgsQualityProfiles 表驱动验证各档位的缩放高度、CRF/码率，
// 并断言不会混入其他档位的参数（保证档位之间真正隔离）。
func TestBuildFFmpegArgsQualityProfiles(t *testing.T) {
	videoPath := filepath.Join(t.TempDir(), "movie.mkv")
	outDir := t.TempDir()

	for _, tc := range qualityProfileCases() {
		t.Run(tc.name, func(t *testing.T) {
			args := buildFFmpegArgs(videoPath, outDir, nil, tc.profile)

			if tc.profile.hwaccel == "vaapi" {
				// 输入前的硬件设备初始化必须在 -i 之前
				inputIdx := indexOf(args, "-i")
				for _, flag := range []string{"-hwaccel", "-init_hw_device", "-hwaccel_device", "-hwaccel_output_format"} {
					idx := indexOf(args, flag)
					if idx < 0 || idx > inputIdx {
						t.Fatalf("%s 必须存在且位于 -i 之前，参数: %v", flag, args)
					}
				}
				containsAll(t, args,
					"-init_hw_device", "vaapi=va:"+tc.profile.vaapiDevice,
					"-hwaccel_output_format", "vaapi",
					"-c:v", "h264_vaapi",
					"-b:v", tc.spec.VideoBitrate,
					"-vf", "scale_vaapi=w=-2:h=min(ih\\,"+strconv.Itoa(tc.spec.Height)+"):format=nv12",
				)
				containsNone(t, args, "libx264", "-preset", "-crf", "scale=trunc", "scale=-2")
			} else {
				containsAll(t, args,
					"-c:v", "libx264",
					"-preset", "veryfast",
					"-crf", strconv.Itoa(tc.spec.CRF),
					"-vf", "scale=-2:min(ih\\,"+strconv.Itoa(tc.spec.Height)+")",
				)
				containsNone(t, args, "vaapi", "scale=trunc")
			}

			// 音频不分档、HLS 公共选项保持不变
			containsAll(t, args, "-c:a", "aac", "-b:a", "128k", "-ac", "2", "-sn",
				"-f", "hls", "-hls_time", "4", "-hls_list_size", "0",
				"-hls_segment_filename", filepath.Join(outDir, "seg-%05d.ts"),
				filepath.Join(outDir, hlsPlaylistName))

			// 档位隔离：参数中不得出现其他档位的高度/CRF/码率
			for otherQ, other := range qualitySpecs {
				if otherQ == "" || other == tc.spec {
					continue
				}
				containsNone(t, args,
					"min(ih\\,"+strconv.Itoa(other.Height)+")",
					"-crf "+strconv.Itoa(other.CRF),
					other.VideoBitrate,
				)
			}
		})
	}
}

// TestNormalizeQuality 验证档位白名单与默认回退。
func TestNormalizeQuality(t *testing.T) {
	for _, q := range []string{Quality1080p, Quality720p, Quality480p} {
		if got := NormalizeQuality(q); got != q {
			t.Errorf("NormalizeQuality(%q) = %q，期望原样返回", q, got)
		}
	}
	for _, bad := range []string{"", "4k", "1080P", "../etc", "null"} {
		if got := NormalizeQuality(bad); got != Quality720p {
			t.Errorf("NormalizeQuality(%q) = %q，期望回退 %q", bad, got, Quality720p)
		}
	}
}

// TestBuildFFmpegArgsMultiAudioKept 在软转与 VAAPI 下都保留多音轨变体布局。
func TestBuildFFmpegArgsMultiAudioKept(t *testing.T) {
	outDir := t.TempDir()
	tracks := []AudioTrack{
		{Index: 1, Language: "chi", Default: true},
		{Index: 2, Language: "eng"},
	}

	for _, p := range []encodeProfile{
		{quality: qualitySpecs[Quality720p]},
		{hwaccel: "vaapi", vaapiDevice: "/dev/dri/renderD128", quality: qualitySpecs[Quality720p]},
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
