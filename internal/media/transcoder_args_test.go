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

// TestSelectABRVariants 验证 ABR 变体按源高度过滤且不放大，异常输入有兜底。
func TestSelectABRVariants(t *testing.T) {
	cases := []struct {
		name        string
		source      int
		wantCount   int
		wantHeights []int
	}{
		{name: "1080源三档全开", source: 1080, wantCount: 3, wantHeights: []int{1080, 720, 480}},
		{name: "720源无1080档", source: 720, wantCount: 2, wantHeights: []int{720, 480}},
		{name: "360源兜底最低档", source: 360, wantCount: 1, wantHeights: []int{480}},
		{name: "零高度兜底", source: 0, wantCount: 1, wantHeights: []int{480}},
		{name: "负高度兜底", source: -100, wantCount: 1, wantHeights: []int{480}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectABRVariants(tc.source, nil) // nil 走默认三档
			if len(got) != tc.wantCount {
				t.Fatalf("变体数 = %d，期望 %d（%+v）", len(got), tc.wantCount, got)
			}
			for i, wantH := range tc.wantHeights {
				if got[i].Height != wantH {
					t.Errorf("第 %d 个变体高度 = %d，期望 %d", i, got[i].Height, wantH)
				}
			}
		})
	}
}

// TestParseABRQualities 验证 TRANSCODE_ABR_QUALITIES 的白名单过滤、去重、排序与回退。
func TestParseABRQualities(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "空串回退默认", raw: "", want: []string{Quality1080p, Quality720p, Quality480p}},
		{name: "弱NAS两档", raw: "720p,480p", want: []string{Quality720p, Quality480p}},
		{name: "单值退化单变体", raw: "480p", want: []string{Quality480p}},
		{name: "乱序仍按高到低", raw: "480p,1080p", want: []string{Quality1080p, Quality480p}},
		{name: "去重", raw: "720p,720p,480p", want: []string{Quality720p, Quality480p}},
		{name: "含非法值被忽略", raw: "4k,720p,garbage", want: []string{Quality720p}},
		{name: "全非法回退默认", raw: "4k,8k", want: []string{Quality1080p, Quality720p, Quality480p}},
		{name: "带空格容错", raw: " 720p , 480p ", want: []string{Quality720p, Quality480p}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseABRQualities(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("解析 %q 得 %v，期望 %v", tc.raw, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("第 %d 档 = %q，期望 %q（整体 %v）", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestSelectABRVariantsConfigured 验证自定义变体档位下的源高度过滤与不放大。
func TestSelectABRVariantsConfigured(t *testing.T) {
	// 配置仅 720p/480p：1080 源也只产两档
	got := selectABRVariants(1080, []string{Quality720p, Quality480p})
	if len(got) != 2 || got[0].Height != 720 || got[1].Height != 480 {
		t.Fatalf("自定义两档过滤异常: %+v", got)
	}
	// 配置档位全部高于源（仅 1080p，源 720）：兜底 480p 单变体
	got = selectABRVariants(720, []string{Quality1080p})
	if len(got) != 1 || got[0].Height != 480 {
		t.Fatalf("全高于源应兜底 480p，实际 %+v", got)
	}
}

// abrProfileCases 覆盖软转与 VAAPI 两套 ABR 编码路径。
func abrProfileCases() []struct {
	name    string
	profile encodeProfile
} {
	return []struct {
		name    string
		profile encodeProfile
	}{
		{"软转", encodeProfile{}},
		{"VAAPI", encodeProfile{hwaccel: "vaapi", vaapiDevice: "/dev/dri/renderD128"}},
	}
}

// TestBuildABRFFmpegArgs 验证自适应会话的多变体参数布局（软转/VAAPI）。
func TestBuildABRFFmpegArgs(t *testing.T) {
	outDir := t.TempDir()
	variants := selectABRVariants(1080, nil) // 1080p/720p/480p 三变体
	if len(variants) != 3 {
		t.Fatalf("1080 源应得 3 个变体，实际 %d", len(variants))
	}

	for _, p := range abrProfileCases() {
		t.Run(p.name, func(t *testing.T) {
			args := buildABRFFmpegArgs("in.mkv", outDir, nil, variants, p.profile)
			joined := strings.Join(args, " ")

			// split 成三路且逐路缩放，三个目标高度都在滤镜里
			if !strings.Contains(joined, "split=3") {
				t.Errorf("filter_complex 缺少 split=3: %s", joined)
			}
			for _, h := range []int{1080, 720, 480} {
				if !strings.Contains(joined, "min(ih\\,"+strconv.Itoa(h)+")") {
					t.Errorf("滤镜缺少高度 %d 的缩放: %s", h, joined)
				}
			}

			// 三路视频 map 标签
			containsAll(t, args, "-map", "[v0]", "-map", "[v1]", "-map", "[v2]")

			// 逐路质量参数
			if p.profile.hwaccel == "vaapi" {
				containsAll(t, args, "-c:v", "h264_vaapi",
					"-b:v:0", "5000k", "-b:v:1", "2800k", "-b:v:2", "1400k")
				// 硬件 flag 必须在 -i 之前
				inputIdx := indexOf(args, "-i")
				for _, flag := range []string{"-hwaccel", "-init_hw_device", "-hwaccel_device", "-hwaccel_output_format"} {
					idx := indexOf(args, flag)
					if idx < 0 || idx > inputIdx {
						t.Fatalf("%s 必须存在且位于 -i 之前，参数: %v", flag, args)
					}
				}
			} else {
				containsAll(t, args, "-c:v", "libx264",
					"-crf:0", "20", "-crf:1", "23", "-crf:2", "26")
			}

			// ABR 专属：master、多变体 var_stream_map、out-%v 命名
			containsAll(t, args, "-master_pl_name", hlsMasterName, "-var_stream_map",
				"v:0,agroup:aud v:1,agroup:aud v:2,agroup:aud a:0,agroup:aud,name:a0",
				"-hls_segment_filename", filepath.Join(outDir, "out-%v-%05d.ts"),
				filepath.Join(outDir, "out-%v.m3u8"))

			// 公共约束
			containsAll(t, args, "-sn", "-c:a", "aac", "-b:a", "128k", "-ac", "2")
			// 不得混入单档布局
			containsNone(t, args, "seg-%05d.ts", hlsPlaylistName)
		})
	}
}

// TestBuildABRFFmpegArgsMultiAudio 验证自适应会话多音轨映射与 var_stream_map 下标。
func TestBuildABRFFmpegArgsMultiAudio(t *testing.T) {
	outDir := t.TempDir()
	variants := selectABRVariants(720, nil) // 两变体：720p/480p
	tracks := []AudioTrack{
		{Index: 1, Language: "chi", Default: true},
		{Index: 2, Language: "eng"},
	}
	args := buildABRFFmpegArgs("in.mkv", outDir, tracks, variants, encodeProfile{})
	// 两路视频 + 两路音频 map
	containsAll(t, args, "-map", "[v0]", "-map", "[v1]", "-map", "0:1?", "-map", "0:2?")
	// var_stream_map 的 a:N 按音频类型内序号编号，两路音频即 a:0 / a:1
	containsAll(t, args, "-var_stream_map",
		"v:0,agroup:aud v:1,agroup:aud a:0,agroup:aud,name:a0,default:yes a:1,agroup:aud,name:a1")
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
