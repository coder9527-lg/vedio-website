package media

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// atomicTime 用 atomic.Value 保存最近访问时间，避免每个请求都抢会话锁。
type atomicTime struct {
	v atomic.Value
}

func (a *atomicTime) load() time.Time {
	if t, ok := a.v.Load().(time.Time); ok {
		return t
	}
	return time.Time{}
}

func (a *atomicTime) store(t time.Time) { a.v.Store(t) }

// HLS 按需转码器：为不支持浏览器直解的视频（MKV / HEVC / FLV 等）
// 用 ffmpeg 实时转码为 H.264 + AAC 的 HLS 分片，并把产物缓存到磁盘。
//
// 设计要点：
//   - 每个视频一个 session，ffmpeg 持续向后产出 mpegts 分片与动态 m3u8；
//   - m3u8 采用默认 event 语义实时追加（不能用 vod/temp_file，二者都会让播放列表
//     到进程结束才落盘），转码完成后 ffmpeg 自动补 EXT-X-ENDLIST；
//   - 信号量限制同时运行的转码任务数，新任务抢不到槽位立即 busy 并入队等待；
//   - 每次拉取播放列表/分片都会刷新最近访问时间，空闲超过 idleTimeout 自动停止
//     ffmpeg，已产出分片保留可复用；
//   - 转码完成的会话直接走磁盘文件服务，不再重启 ffmpeg；
//   - 失败/被停止的会话再次被请求时全量重转（清空旧目录，分片重新编号）。

const (
	hlsPlaylistName = "index.m3u8"
	hlsMasterName   = "master.m3u8" // 多音轨主播放列表（前端入口）
	hlsSegmentTime  = 4             // 单个 HLS 分片时长（秒）
	hlsAudioGroup   = "aud"         // 多音轨变体在 master 中的分组 ID

	// 内嵌字幕提取产物的统一前缀：sub-<序号>.vtt，受 AssetPath 白名单保护。
	hlsSubPrefix = "sub-"

	// 共享内嵌字幕的目录名（视频级，各清晰度档位共用，避免重复提取/OCR）。
	hlsSubDir = "subs"
)

// 清晰度档位标识（同时作为会话复合键与产物目录名的一部分）。
const (
	Quality1080p = "1080p"
	Quality720p  = "720p"
	Quality480p  = "480p"
)

// QualitySpec 描述一个清晰度档位的编码参数。
//   - Height：目标高度（像素），宽度按比例自动取偶数；源高度不足时不放大；
//   - CRF：软转 libx264 质量参数（越小越清晰）；
//   - VideoBitrate：VAAPI 硬转目标码率（硬编不支持 CRF）。
type QualitySpec struct {
	Height       int
	CRF          int
	VideoBitrate string
}

// qualitySpecs 是三档清晰度的参数表，键为档位常量。
var qualitySpecs = map[string]QualitySpec{
	Quality1080p: {Height: 1080, CRF: 20, VideoBitrate: "5000k"},
	Quality720p:  {Height: 720, CRF: 23, VideoBitrate: "2800k"},
	Quality480p:  {Height: 480, CRF: 26, VideoBitrate: "1400k"},
}

// NormalizeQuality 校验清晰度档位，非法值（含空串）回退默认 720p。
// 注意：自适应档位 QualityAuto 是独立会话类型，不在此白名单内，由上层单独识别。
func NormalizeQuality(q string) string {
	if _, ok := qualitySpecs[q]; ok {
		return q
	}
	return Quality720p
}

// QualityAuto 是自适应（ABR）会话标识：单进程同时产出多个视频变体与 master 播放列表，
// 由 hls.js / Safari 按带宽自动选档，与单档手动转码走独立会话目录，互不影响。
const QualityAuto = "auto"

// defaultABRQualities 是自适应会话默认产出的变体档位（高→低有序）。
var defaultABRQualities = []string{Quality1080p, Quality720p, Quality480p}

// selectABRVariants 按源视频高度与配置的变体档位过滤 ABR 变体，遵循「不放大源」：
// 仅保留目标高度不超过源高度的档位；qualities 为空时用默认三档；源高度异常（≤0）
// 或全部档位都高于源时，兜底返回最低 480p 单变体（实际缩放仍由 min(ih,h) 约束，不会放大）。
func selectABRVariants(sourceHeight int, qualities []string) []QualitySpec {
	if len(qualities) == 0 {
		qualities = defaultABRQualities
	}
	if sourceHeight <= 0 {
		return []QualitySpec{qualitySpecs[Quality480p]}
	}
	var variants []QualitySpec
	for _, name := range qualities {
		spec, ok := qualitySpecs[name]
		if ok && spec.Height <= sourceHeight {
			variants = append(variants, spec)
		}
	}
	if len(variants) == 0 {
		return []QualitySpec{qualitySpecs[Quality480p]}
	}
	return variants
}

// abrVariantSpecs 返回当前转码器配置下、给定源高度实际启用的 ABR 变体。
func (t *Transcoder) abrVariantSpecs(sourceHeight int) []QualitySpec {
	return selectABRVariants(sourceHeight, t.abrQualities)
}

// sessionKey 生成会话复合键：同一视频的不同档位各自独立转码与缓存。
func sessionKey(videoID, quality string) string { return videoID + ":" + quality }

// subtitleExtractTimeout 限制单条内嵌字幕的提取时长，避免异常文件拖住转码启动。
const subtitleExtractTimeout = 60 * time.Second

// 转码相关的哨兵错误。
var (
	ErrTranscoderBusy     = errors.New("转码任务已满，请稍后重试")
	ErrTranscoderDisabled = errors.New("转码功能未启用或 ffmpeg 不可用")
	ErrSessionFailed      = errors.New("转码会话已失败")
)

// 会话状态机：idle → starting → running → done / idle(被停止) / failed。
type sessState int

const (
	stateIdle sessState = iota
	stateStarting
	stateRunning
	stateDone
	stateFailed
)

// Transcoder 管理全部视频的 HLS 转码会话，线程安全。
type Transcoder struct {
	dir           string // 转码产物根目录（每个视频一个子目录）
	ffmpeg        string
	ffprobe       string
	maxConcurrent int
	idleTimeout   time.Duration
	ocr           *OCRer   // 可选：位图字幕（PGS/DVD 等）OCR 转 WebVTT，nil 则跳过位图轨
	hwaccel       string   // 硬件加速模式：空=CPU 软转（默认），"vaapi"=VAAPI
	vaapiDevice   string   // VAAPI 渲染节点路径（如 /dev/dri/renderD128）
	abrQualities  []string // ABR 自适应会话产出的变体档位（高→低有序），空则用 defaultABRQualities

	ctx    context.Context
	cancel context.CancelFunc

	sem      chan struct{} // 并发转码任务信号量
	mu       sync.Mutex
	sessions map[string]*session
	subOnce  map[string]*sync.Once // 每个视频的内嵌字幕只提取一次（视频级共享，跨档位复用）
	wg       sync.WaitGroup        // 等待全部 ffmpeg 进程退出
}

// SetOCRer 注入位图字幕 OCR 器。未注入或 OCR 不可用时，位图字幕轨保持
// 「仅识别不提取」的原有行为。必须在首个转码会话启动前调用。
func (t *Transcoder) SetOCRer(ocr *OCRer) {
	t.ocr = ocr
}

// ParseABRQualities 解析 TRANSCODE_ABR_QUALITIES 配置（逗号分隔的档位名）。
// 仅保留三档白名单内的合法值并去重，按默认高→低顺序输出；全部非法或为空时
// 回退默认三档，保证弱 NAS 配「720p,480p」、单值退化单变体等用法可控。
func ParseABRQualities(raw string) []string {
	picked := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if _, ok := qualitySpecs[name]; ok {
			picked[name] = true
		}
	}
	var out []string
	for _, name := range defaultABRQualities { // 复用默认顺序，避免配置顺序影响变体排列
		if picked[name] {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), defaultABRQualities...)
	}
	return out
}

// SetABRQualities 配置 ABR 自适应会话产出的变体档位。必须在首个转码会话启动前调用；
// 传 nil/空切片时沿用默认三档。
func (t *Transcoder) SetABRQualities(qualities []string) {
	if len(qualities) == 0 {
		t.abrQualities = append([]string(nil), defaultABRQualities...)
		return
	}
	t.abrQualities = append([]string(nil), qualities...)
}

// SetHWAccel 配置硬件加速模式。mode 为空表示 CPU 软转（默认）；
// 当前仅支持 "vaapi"（Intel/核显），未知模式告警并回退软转。
// VAAPI 设备节点在启动期预检，不可访问时同样回退软转，
// 避免容器未映射 /dev/dri 时每个转码会话都在运行期失败。
// 必须在首个转码会话启动前调用。
func (t *Transcoder) SetHWAccel(mode, device string) {
	mode = strings.TrimSpace(mode)
	switch mode {
	case "":
		t.hwaccel, t.vaapiDevice = "", ""
	case "vaapi":
		if _, err := os.Stat(device); err != nil {
			log.Printf("警告: VAAPI 设备 %s 不可访问（%v），回退 CPU 软转", device, err)
			t.hwaccel, t.vaapiDevice = "", ""
			return
		}
		t.hwaccel, t.vaapiDevice = "vaapi", device
	default:
		log.Printf("警告: 未知硬件加速模式 %q，回退 CPU 软转（当前支持: vaapi）", mode)
		t.hwaccel, t.vaapiDevice = "", ""
	}
}

// HWAccelMode 返回当前实际生效的硬件加速模式（供启动日志与诊断使用）。
func (t *Transcoder) HWAccelMode() string {
	if t.hwaccel == "" {
		return "软件转码(libx264)"
	}
	return t.hwaccel + "(" + t.vaapiDevice + ")"
}

// NewTranscoder 创建转码器并清空旧的转码产物（残留的临时目录不可信）。
func NewTranscoder(dir, ffmpeg, ffprobe string, maxConcurrent int, idleTimeout time.Duration) (*Transcoder, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// 启动时清理上一轮残留：未完成的 m3u8/分片可能不一致
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &Transcoder{
		dir:           dir,
		ffmpeg:        ffmpeg,
		ffprobe:       ffprobe,
		maxConcurrent: maxConcurrent,
		idleTimeout:   idleTimeout,
		ctx:           ctx,
		cancel:        cancel,
		sem:           make(chan struct{}, maxConcurrent),
		sessions:      map[string]*session{},
		subOnce:       map[string]*sync.Once{},
	}
	go t.reapLoop()
	return t, nil
}

// Available 返回转码功能是否可用（ffmpeg 在 PATH 中可找到）。
func (t *Transcoder) Available() bool {
	if t == nil {
		return false
	}
	_, err := exec.LookPath(t.ffmpeg)
	return err == nil
}

// sessionStatus 描述会话当前状态，供 API 轮询。
type sessionStatus struct {
	State        string          `json:"state"` // idle | starting | running | done | failed
	HasPlaylist  bool            `json:"hasPlaylist"`
	Segments     int             `json:"segments"`
	Quality      string          `json:"quality"`    // 会话档位（1080p/720p/480p 或 auto 自适应）
	MultiAudio   bool            `json:"multiAudio"` // 是否产出 master.m3u8 多音轨布局
	AudioTracks  []AudioTrack    `json:"audioTracks,omitempty"`
	EmbeddedSubs []SubtitleTrack `json:"embeddedSubtitles,omitempty"` // 内嵌字幕轨（含位图轨）
}

// Ensure 返回（必要时启动）视频在指定清晰度档位下的转码会话。
// quality 非法时回退默认 720p；同一视频的不同档位拥有独立会话与产物目录。
// 槽位已满且这是新排队任务时立即返回 ErrTranscoderBusy（会话已入队，前端轮询即可）；
// 会话已在 starting/running/done 时直接返回会话与当前状态。
func (t *Transcoder) Ensure(v *Video, quality string) (*session, error) {
	if !t.Available() {
		return nil, ErrTranscoderDisabled
	}
	// auto 是独立的自适应会话类型，不进普通档位的归一化白名单；
	// 其余非法值仍由 NormalizeQuality 回退 720p。
	if quality != QualityAuto {
		quality = NormalizeQuality(quality)
	}
	key := sessionKey(v.ID, quality)

	t.mu.Lock()
	sess, ok := t.sessions[key]
	if ok && sess.getState() == stateFailed {
		// 失败会话丢弃，下一轮以全新状态重试
		delete(t.sessions, key)
		ok = false
	}
	if !ok {
		sess = newSession(v, filepath.Join(t.dir, v.ID, quality), quality)
		t.sessions[key] = sess
	}
	t.mu.Unlock()

	sess.Touch()
	// 已在启动/运行中，或已转码完成（走磁盘缓存），直接复用
	if !sess.tryBegin() {
		return sess, nil
	}

	// tryBegin 成功（idle → starting）后，必须保证最终有一次 run，
	// 否则会话会永远停在 starting。先非阻塞抢槽位：
	//   - 抢到：goroutine 立即开始转码；
	//   - 抢不到：goroutine 阻塞排队等槽位，本次请求立即返回 busy（前端轮询），
	//     后续请求看到会话处于 starting 会直接返回状态，不会重复排队。
	select {
	case t.sem <- struct{}{}:
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			defer func() { <-t.sem }()
			t.run(sess)
		}()
	default:
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			select {
			case t.sem <- struct{}{}:
				defer func() { <-t.sem }()
				t.run(sess)
			case <-t.ctx.Done():
				sess.backToIdle()
			}
		}()
		return nil, ErrTranscoderBusy
	}

	// 给进程启动一个短窗口：槽位空闲时 ffmpeg 很快能写出首批分片；
	// 启动中也直接返回，由前端轮询驱动。
	select {
	case <-sess.started:
	case <-time.After(2 * time.Second):
	}
	if sess.getState() == stateFailed {
		t.mu.Lock()
		if cur := t.sessions[key]; cur == sess {
			delete(t.sessions, key)
		}
		t.mu.Unlock()
		return nil, ErrSessionFailed
	}
	return sess, nil
}

// run 执行一次完整的 ffmpeg 转码进程，直到自然结束、失败或被停止。
func (t *Transcoder) run(sess *session) {
	// 重新启动时清理旧产物，保证 m3u8 从 1 号分片重新编号
	_ = os.RemoveAll(sess.dir)
	if err := os.MkdirAll(sess.dir, 0o755); err != nil {
		sess.fail(err)
		return
	}

	// 探测音轨：失败不致命，降级为「第一路音轨」的简单布局
	tracks, probeErr := ProbeAudio(t.ffprobe, sess.video.AbsPath())
	if probeErr != nil {
		log.Printf("ffprobe 探测音轨失败 %s，按单音轨转码: %v", sess.video.RelPath, probeErr)
	}
	sess.setAudioTracks(tracks)

	// 探测内嵌字幕：文本轨在主转码前同步提取为 WebVTT sidecar（sub-N.vtt），
	// 位图轨（PGS/DVD 等）仅记录、不提取。失败不阻断主转码。
	// 字幕产物为视频级共享（<dir>/<videoID>/subs/），各清晰度档位只提取一次，
	// 避免切档时重复转码/OCR。
	subTracks, subErr := ProbeSubtitles(t.ffprobe, sess.video.AbsPath())
	if subErr != nil {
		log.Printf("ffprobe 探测字幕失败 %s，跳过内嵌字幕: %v", sess.video.RelPath, subErr)
	}
	sess.setSubtitleTracks(subTracks)
	if len(subTracks) > 0 {
		t.mu.Lock()
		once, ok := t.subOnce[sess.video.ID]
		if !ok {
			once = &sync.Once{}
			t.subOnce[sess.video.ID] = once
		}
		t.mu.Unlock()
		once.Do(func() { t.extractSubtitles(sess, subTracks) })
	}

	ctx, cancel := context.WithCancel(t.ctx)
	sess.setCancel(cancel)
	defer cancel()

	args := t.buildArgs(sess, tracks)

	cmd := exec.CommandContext(ctx, t.ffmpeg, args...)
	// ffmpeg 进度/错误全在 stderr，落盘日志便于排障并防止管道阻塞
	logFile, err := os.Create(filepath.Join(sess.dir, "ffmpeg.log"))
	if err == nil {
		cmd.Stderr = logFile
		defer logFile.Close()
	}

	sess.setState(stateRunning)
	if err := cmd.Start(); err != nil {
		sess.fail(fmt.Errorf("启动 ffmpeg 失败: %w", err))
		return
	}
	sess.startOnce.Do(func() { close(sess.started) })

	// 多音轨：ffmpeg 6.x 不会把 var_stream_map 的 name 写入 master 的 EXT-X-MEDIA NAME
	//（始终是 audio_N），后台轮询重写为友好音轨名（国语/English/音轨 2）。
	// ABR 会话无论音轨数都要重写：除音轨名外还需剔除 master 末尾的纯音频 STREAM-INF。
	if len(tracks) > 1 || sess.quality == QualityAuto {
		stopRewrite := make(chan struct{})
		go t.rewriteMasterNames(sess, tracks, stopRewrite)
		defer close(stopRewrite)
	}

	err = cmd.Wait()
	if ctx.Err() != nil {
		// 被 idle 回收或服务关闭主动停止：回到 idle，保留已产出分片，下次请求可重转
		sess.backToIdle()
		return
	}
	if err != nil {
		sess.fail(fmt.Errorf("ffmpeg 转码失败: %w", err))
		return
	}
	sess.setState(stateDone)
}

// encodeProfile 描述一次转码的编码档位，使参数拼装成为不依赖实例的纯函数，
// 便于在无 ffmpeg/无 GPU 的环境下单测软转与硬转两套参数。
type encodeProfile struct {
	hwaccel     string      // 硬件加速模式：空=软转，"vaapi"=VAAPI
	vaapiDevice string      // VAAPI 渲染节点路径
	quality     QualitySpec // 清晰度档位（目标高度、CRF、硬转码率）
}

// buildFFmpegArgs 根据输入视频、输出目录、探测到的音轨与编码档位生成 ffmpeg 参数。
//   - 0~1 路音频：简单布局，单个 index.m3u8 + seg-xxxxx.ts（保持旧行为与测试不变）；
//   - 2 路及以上：变体布局，master.m3u8 为入口，视频与每路音频各自一个媒体播放列表，
//     前端通过 EXT-X-MEDIA 音频分组切换音轨，视频流只编码一份以节省 CPU。
//
// 缩放遵循「不放大源」原则：目标高度取 min(源高, 档高)，宽度 -2 自动保持比例与偶数像素。
func buildFFmpegArgs(videoPath, outDir string, tracks []AudioTrack, p encodeProfile) []string {
	h := p.quality.Height
	// 编解码与 HLS 公共选项（不含输入与 -map，二者随音轨数变化）。
	var opts []string
	if p.hwaccel == "vaapi" {
		// VAAPI 硬解硬编：输入前初始化硬件设备，输出帧保持 VAAPI 表面格式，
		// scale_vaapi 同时完成缩放与像素格式转换（nv12 为 h264_vaapi 通用输入格式）。
		// 逗号需转义以与滤镜链分隔符区分；min(ih\,h) 保证不向上放大。
		opts = []string{
			"-c:v", "h264_vaapi",
			"-b:v", p.quality.VideoBitrate, // VAAPI 不支持 crf/preset，使用目标码率控制
			"-vf", fmt.Sprintf("scale_vaapi=w=-2:h=min(ih\\,%d):format=nv12", h),
			"-c:a", "aac", "-b:a", "128k",
			"-ac", "2",
			"-sn", // 丢弃内嵌字幕轨：外挂字幕通过 <track> 提供，位图字幕无法转 WebVTT
			"-f", "hls",
			"-hls_time", fmt.Sprintf("%d", hlsSegmentTime),
			"-hls_flags", "append_list+independent_segments",
			"-hls_list_size", "0",
		}
	} else {
		opts = []string{
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-crf", strconv.Itoa(p.quality.CRF),
			// -2 让宽度按比例自动取偶数；min(ih\,h) 保证源高度不足时不放大
			"-vf", fmt.Sprintf("scale=-2:min(ih\\,%d)", h),
			"-c:a", "aac", "-b:a", "128k",
			"-ac", "2",
			"-sn",
			"-f", "hls",
			"-hls_time", fmt.Sprintf("%d", hlsSegmentTime),
			// 不设 hls_playlist_type=vod：vod 语义下 ffmpeg 到转码结束才写 m3u8，
			// 无法边转边播。默认 event 语义下列表实时追加，完成时自动补 ENDLIST。
			"-hls_flags", "append_list+independent_segments",
			"-hls_list_size", "0", // 保留全部分片，支持回看与断点续播
		}
	}

	args := []string{"-y"}
	if p.hwaccel == "vaapi" {
		// 硬件加速选项必须位于 -i 之前：初始化设备并让解码帧直接留在显存
		args = append(args,
			"-hwaccel", "vaapi",
			"-init_hw_device", "vaapi=va:"+p.vaapiDevice,
			"-hwaccel_device", "va",
			"-hwaccel_output_format", "vaapi",
		)
	}
	args = append(args, "-i", videoPath)

	if len(tracks) <= 1 {
		// 简单布局：单个 index.m3u8 + seg-xxxxx.ts，取第一路视频/音频
		args = append(args, "-map", "0:v:0", "-map", "0:a:0?") // 无音频轨也不报错
		args = append(args, opts...)
		args = append(args,
			"-hls_segment_filename", filepath.Join(outDir, "seg-%05d.ts"),
			filepath.Join(outDir, hlsPlaylistName),
		)
		return args
	}

	// 多音轨：视频变体 + 每路音频一个独立变体，master 引用同一视频流。
	// -map 必须在输出参数之前，ffmpeg 按输出流序号（v:0/a:0/a:1）映射，
	// 缺少 -map 会导致 var_stream_map 中的 a:N 找不到输出流。
	args = append(args, "-map", "0:v:0")
	for _, tr := range tracks {
		args = append(args, "-map", fmt.Sprintf("0:%d?", tr.Index)) // 单路损坏不拖垮整体
	}
	args = append(args, opts...)

	var streamMap []string
	streamMap = append(streamMap, "v:0,agroup:"+hlsAudioGroup)
	for i, tr := range tracks {
		entry := fmt.Sprintf("a:%d,agroup:%s,name:a%d", i, hlsAudioGroup, i)
		if tr.Default {
			entry += ",default:yes"
		}
		streamMap = append(streamMap, entry)
	}
	args = append(args,
		"-master_pl_name", hlsMasterName,
		"-var_stream_map", strings.Join(streamMap, " "),
		"-hls_segment_filename", filepath.Join(outDir, "out-%v-%05d.ts"),
		filepath.Join(outDir, "out-%v.m3u8"),
	)
	return args
}

// buildArgs 是 buildFFmpegArgs 的实例薄封装，注入当前转码器的硬件加速与清晰度档位。
// auto 自适应会话改走多变体 ABR 参数：先探测源高度过滤变体；探测失败时回退到
// 现有 720p 单档参数，保证任何情况下都能出流。
func (t *Transcoder) buildArgs(sess *session, tracks []AudioTrack) []string {
	profile := encodeProfile{
		hwaccel:     t.hwaccel,
		vaapiDevice: t.vaapiDevice,
		quality:     qualitySpecs[sess.quality],
	}
	if sess.quality != QualityAuto {
		return buildFFmpegArgs(sess.video.AbsPath(), sess.dir, tracks, profile)
	}

	height, err := ProbeVideoHeight(t.ffprobe, sess.video.AbsPath())
	if err != nil {
		log.Printf("ABR 探测源高度失败 %s，回退 720p 单档: %v", sess.video.RelPath, err)
		profile.quality = qualitySpecs[Quality720p]
		return buildFFmpegArgs(sess.video.AbsPath(), sess.dir, tracks, profile)
	}
	variants := t.abrVariantSpecs(height)
	log.Printf("ABR 会话 %s 源高度 %d，启用 %d 个变体", sess.video.RelPath, height, len(variants))
	return buildABRFFmpegArgs(sess.video.AbsPath(), sess.dir, tracks, variants, profile)
}

// buildABRFFmpegArgs 为自适应（ABR）会话生成单进程多视频变体的 ffmpeg 参数：
// 一次编码产出 len(variants) 路视频流（各自缩放高度与 CRF/码率）加共享音频，
// 由 HLS master 播放列表聚合，播放器按带宽在变体间切换。
//   - 软转：filter_complex split 后逐路 scale，每路用 -crf:<i> 独立质量；
//   - VAAPI：逐路 scale_vaapi，每路用 -b:v:<i> 独立码率（硬编不支持 CRF）；
//   - 音频只编码一份（多音轨时每路各一份），各视频变体在 var_stream_map 引用同一音频组；
//   - 缩放同样遵循不放大源（min(ih\,h)）；始终 -sn，内嵌字幕走共享 sidecar。
func buildABRFFmpegArgs(videoPath, outDir string, tracks []AudioTrack, variants []QualitySpec, p encodeProfile) []string {
	n := len(variants)

	// filter_complex：把源视频 split 成 N 路，再逐路缩放并命名 [v0]..[vN-1]
	var filters []string
	splits := make([]string, n)
	for i := range variants {
		splits[i] = fmt.Sprintf("[s%d]", i)
	}
	filters = append(filters, "[0:v]split="+strconv.Itoa(n)+strings.Join(splits, ""))
	for i, spec := range variants {
		if p.hwaccel == "vaapi" {
			filters = append(filters, fmt.Sprintf("[s%d]scale_vaapi=w=-2:h=min(ih\\,%d):format=nv12[v%d]", i, spec.Height, i))
		} else {
			filters = append(filters, fmt.Sprintf("[s%d]scale=-2:min(ih\\,%d)[v%d]", i, spec.Height, i))
		}
	}

	args := []string{"-y"}
	if p.hwaccel == "vaapi" {
		// 硬件初始化参数必须位于 -i 之前
		args = append(args,
			"-hwaccel", "vaapi",
			"-init_hw_device", "vaapi=va:"+p.vaapiDevice,
			"-hwaccel_device", "va",
			"-hwaccel_output_format", "vaapi",
		)
	}
	args = append(args, "-i", videoPath)
	args = append(args, "-filter_complex", strings.Join(filters, ";"))

	// 视频输出流 v:0..v:N-1，逐路映射缩放后的标签
	for i := range variants {
		args = append(args, "-map", fmt.Sprintf("[v%d]", i))
	}
	// 音频输出流从 v:N 开始依次排列；无音轨时用 0:a:0? 占位且不致命
	if len(tracks) == 0 {
		args = append(args, "-map", "0:a:0?")
	} else {
		for _, tr := range tracks {
			args = append(args, "-map", fmt.Sprintf("0:%d?", tr.Index))
		}
	}

	// 编码器与逐路视频质量参数
	if p.hwaccel == "vaapi" {
		args = append(args, "-c:v", "h264_vaapi")
		for i, spec := range variants {
			args = append(args, fmt.Sprintf("-b:v:%d", i), spec.VideoBitrate)
		}
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast")
		for i, spec := range variants {
			args = append(args, fmt.Sprintf("-crf:%d", i), strconv.Itoa(spec.CRF))
		}
	}
	args = append(args,
		"-c:a", "aac", "-b:a", "128k", "-ac", "2",
		"-sn",
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", hlsSegmentTime),
		"-hls_flags", "append_list+independent_segments",
		"-hls_list_size", "0",
		"-master_pl_name", hlsMasterName,
	)

	// var_stream_map：N 个视频变体引用同一音频组，音频变体按输出流序号 a:N.. 排列
	var streamMap []string
	for i := range variants {
		streamMap = append(streamMap, fmt.Sprintf("v:%d,agroup:%s", i, hlsAudioGroup))
	}
	if len(tracks) <= 1 {
		// 单音轨（含无音轨占位）：var_stream_map 的 a:N 按音频流类型内序号编号，
		// 与前面视频流数量无关，第一路音频即 a:0。
		streamMap = append(streamMap, fmt.Sprintf("a:0,agroup:%s,name:a0", hlsAudioGroup))
	} else {
		for i, tr := range tracks {
			entry := fmt.Sprintf("a:%d,agroup:%s,name:a%d", i, hlsAudioGroup, i)
			if tr.Default {
				entry += ",default:yes"
			}
			streamMap = append(streamMap, entry)
		}
	}
	args = append(args, "-var_stream_map", strings.Join(streamMap, " "))
	args = append(args,
		"-hls_segment_filename", filepath.Join(outDir, "out-%v-%05d.ts"),
		filepath.Join(outDir, "out-%v.m3u8"),
	)
	return args
}

// masterAudioLineRE 匹配 master 播放列表中的 EXT-X-MEDIA 音频行，替换 NAME 属性。
var masterAudioLineRE = regexp.MustCompile(`(EXT-X-MEDIA:TYPE=AUDIO,.*?)NAME="[^"]*"`)

// rewriteMasterNames 轮询重写 master.m3u8，把 audio_N 名称替换为探测到的友好音轨名。
// ffmpeg 以 event 语义持续覆写该文件，因此不能只改一次；进程结束（stop 通道关闭）后退出。
func (t *Transcoder) rewriteMasterNames(sess *session, tracks []AudioTrack, stop <-chan struct{}) {
	master := filepath.Join(sess.dir, hlsMasterName)
	isAuto := sess.quality == QualityAuto
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			t.rewriteOnce(master, tracks, isAuto)
			return
		case <-ticker.C:
			t.rewriteOnce(master, tracks, isAuto)
		}
	}
}

// rewriteOnce 执行单次 master 重写；文件尚未生成时静默跳过。
//   - 多音轨：把 audio_N 的 NAME 替换为友好音轨名；
//   - ABR（isAuto）：ffmpeg 会额外生成一条无 RESOLUTION 的纯音频 EXT-X-STREAM-INF，
//     hls.js 不需要它（保留会出现一个「纯音频清晰度」），连同下一行 URI 一并剔除。
func (t *Transcoder) rewriteOnce(master string, tracks []AudioTrack, isAuto bool) {
	raw, err := os.ReadFile(master)
	if err != nil {
		return
	}
	content := string(raw)
	i := 0
	content = masterAudioLineRE.ReplaceAllStringFunc(content, func(line string) string {
		label := "音轨 " + strconv.Itoa(i+1)
		if i < len(tracks) && tracks[i].Label != "" {
			label = tracks[i].Label
		}
		i++
		return masterAudioLineRE.ReplaceAllString(line, `${1}NAME="`+label+`"`)
	})
	if isAuto {
		content = stripAudioOnlyStreamInf(content)
	}
	if content != string(raw) {
		_ = os.WriteFile(master, []byte(content), 0o644)
	}
}

// stripAudioOnlyStreamInf 剔除 master 中无 RESOLUTION 的纯音频 EXT-X-STREAM-INF
// 条目及其紧随的 URI 行；带 RESOLUTION 的视频变体条目保持不变。
func stripAudioOnlyStreamInf(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for j := 0; j < len(lines); j++ {
		line := lines[j]
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") && !strings.Contains(line, "RESOLUTION") {
			// 跳过该 STREAM-INF 行；若下一行是相对 URI（非标签、非空）也一并跳过
			if j+1 < len(lines) {
				next := lines[j+1]
				if next != "" && !strings.HasPrefix(next, "#") {
					j++
				}
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// extractSubtitles 把内嵌字幕逐路导出为转码目录下的 sub-<序号>.vtt。
// 文本轨（SRT/ASS/WebVTT 等）由 ffmpeg 直接转码；位图轨（PGS/DVD 等）在 OCR 器
// 可用时经 tesseract 识别为 WebVTT，OCR 不可用则跳过。序号与 ProbeSubtitles 返回
// 的列表下标一致；未产出文件的轨由处理器按文件是否存在返回 404。任一轨失败不影响
// 其余轨与主转码。
func (t *Transcoder) extractSubtitles(sess *session, tracks []SubtitleTrack) {
	// 视频级共享目录 <root>/<videoID>/subs/，与各档位会话目录平级，
	// 切档或会话重转（RemoveAll 会话目录）都不会清掉已提取字幕。
	subsDir := filepath.Join(t.dir, sess.video.ID, hlsSubDir)
	if err := os.MkdirAll(subsDir, 0o755); err != nil {
		log.Printf("创建共享字幕目录失败 %s: %v", subsDir, err)
		return
	}
	for i, tr := range tracks {
		out := filepath.Join(subsDir, fmt.Sprintf("%s%d.vtt", hlsSubPrefix, i))
		if tr.Bitmap {
			t.extractBitmapSubtitle(sess, tr, i, out)
			continue
		}
		if !tr.Text {
			continue
		}
		ctx, cancel := context.WithTimeout(t.ctx, subtitleExtractTimeout)
		cmd := exec.CommandContext(ctx, t.ffmpeg,
			"-y", "-v", "error",
			"-i", sess.video.AbsPath(),
			"-map", fmt.Sprintf("0:%d?", tr.Index), // 单路损坏不拖垮整体
			"-c:s", "webvtt",
			out,
		)
		if err := cmd.Run(); err != nil {
			log.Printf("提取内嵌字幕失败 %s 流#%d: %v", sess.video.RelPath, tr.Index, err)
			cancel()
			continue
		}
		cancel()
		// ffmpeg 对不足 1 小时的素材会输出 MM:SS.mmm（如 00:01.023），不符合
		// WebVTT 要求的 HH:MM:SS.mmm，部分浏览器会拒收，统一规整后再落盘。
		if raw, err := os.ReadFile(out); err == nil {
			_ = os.WriteFile(out, normalizeVTTTimestamps(raw), 0o644)
		}
		log.Printf("已提取内嵌字幕 %s 流#%d → %s.vtt", sess.video.RelPath, tr.Index, strconv.Itoa(i))
	}
}

// extractBitmapSubtitle 用 OCR 把单条位图字幕流转成 sub-<序号>.vtt。
// OCR 器未注入、tesseract/语言包不可用或识别失败时仅记录日志并跳过，不影响主转码。
func (t *Transcoder) extractBitmapSubtitle(sess *session, tr SubtitleTrack, ordinal int, out string) {
	if t.ocr == nil || !t.ocr.Available() {
		log.Printf("位图字幕 %s 流#%d 无 OCR 环境（tesseract/语言包缺失），跳过",
			sess.video.RelPath, tr.Index)
		return
	}
	ctx, cancel := context.WithTimeout(t.ctx, ocrTimeout)
	defer cancel()
	if err := t.ocr.ConvertBitmapSubtitle(ctx, sess.video.AbsPath(), tr, out); err != nil {
		log.Printf("位图字幕 OCR 失败 %s 流#%d: %v", sess.video.RelPath, tr.Index, err)
		_ = os.Remove(out) // 清理可能残留的半成品
		return
	}
	log.Printf("已 OCR 位图字幕 %s 流#%d → %s.vtt",
		sess.video.RelPath, tr.Index, strconv.Itoa(ordinal))
}

// vttShortTimeRE 匹配缺少小时段的 VTT 时间戳 MM:SS.mmm（可能出现在 cue 行的起止两侧）。
var vttShortTimeRE = regexp.MustCompile(`(^|[\s>])(\d{2}):(\d{2}[.,]\d{3})`)

// normalizeVTTTimestamps 把 ffmpeg 可能输出的 MM:SS.mmm 补齐为 HH:MM:SS.mmm。
// 已是 HH:MM:SS 形式（冒号两段以上）的时间戳因锚点不匹配而保持不变。
func normalizeVTTTimestamps(raw []byte) []byte {
	return vttShortTimeRE.ReplaceAll(raw, []byte(`${1}00:$2:$3`))
}

// Get 按视频 ID 与清晰度档位查找会话（不存在返回 nil）。
// quality 非法时按默认 720p 归一化，与 Ensure 的键规则保持一致；auto 独立识别。
func (t *Transcoder) Get(videoID, quality string) *session {
	if quality != QualityAuto {
		quality = NormalizeQuality(quality)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessions[sessionKey(videoID, quality)]
}

// SubAssetPath 返回视频级共享内嵌字幕（subs/sub-N.vtt）的磁盘路径。
// 文件名沿用 subVTTRE 白名单严格校验，杜绝路径穿越；文件不存在时返回错误。
func (t *Transcoder) SubAssetPath(videoID, name string) (string, error) {
	if name == "" || name != filepath.Base(name) || !subVTTRE.MatchString(name) {
		return "", os.ErrNotExist
	}
	p := filepath.Join(t.dir, videoID, hlsSubDir, name)
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// reapLoop 定期回收无客户端且空闲超时的会话，停止 ffmpeg 释放 CPU。
func (t *Transcoder) reapLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-ticker.C:
			t.mu.Lock()
			var idle []*session
			for _, sess := range t.sessions {
				// running（转码中）或 starting（排队等槽位）且长时间无人访问则回收
				st := sess.getState()
				if (st == stateRunning || st == stateStarting) &&
					time.Since(sess.lastAccess()) > t.idleTimeout {
					idle = append(idle, sess)
				}
			}
			t.mu.Unlock()
			for _, sess := range idle {
				sess.stop()
			}
		}
	}
}

// Close 停止全部转码进程并清理产物目录，服务优雅关闭时调用。
func (t *Transcoder) Close() {
	t.cancel()
	t.mu.Lock()
	for _, sess := range t.sessions {
		sess.stop()
	}
	t.mu.Unlock()
	t.wg.Wait()
	_ = os.RemoveAll(t.dir)
}

// ---------- 单个转码会话 ----------

type session struct {
	video   *Video
	dir     string
	quality string // 清晰度档位（Quality1080p/720p/480p）

	started   chan struct{} // ffmpeg 进程尝试启动后关闭（成功或失败）
	startOnce sync.Once

	mu             sync.Mutex
	state          sessState
	cancel         context.CancelFunc
	audioTracks    []AudioTrack    // ffprobe 探测到的音轨（单音轨时也填充）
	subtitleTracks []SubtitleTrack // ffprobe 探测到的内嵌字幕轨（含不可提取的位图轨）

	lastAccessV atomicTime // 最近一次被播放器/API 访问的时间
}

func newSession(v *Video, dir, quality string) *session {
	s := &session{
		video:   v,
		dir:     dir,
		quality: quality,
		started: make(chan struct{}),
	}
	s.Touch()
	return s
}

// tryBegin 尝试把会话从 idle 切到 starting；
// 返回 false 表示已有其他请求启动/完成转码，调用方无需再启动。
func (s *session) tryBegin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateIdle {
		return false
	}
	s.state = stateStarting
	return true
}

func (s *session) getState() sessState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *session) setState(st sessState) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
}

func (s *session) backToIdle() {
	s.mu.Lock()
	s.state = stateIdle
	s.mu.Unlock()
}

func (s *session) fail(err error) {
	s.mu.Lock()
	s.state = stateFailed
	s.mu.Unlock()
	// started 可能在 cmd.Start 失败前/后关闭，用 Once 保证只关一次
	s.startOnce.Do(func() { close(s.started) })
}

func (s *session) setCancel(c context.CancelFunc) {
	s.mu.Lock()
	s.cancel = c
	s.mu.Unlock()
}

// Status 返回会话的快照状态。
func (s *session) Status() sessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	multi := s.hasMasterLocked()
	st := sessionStatus{
		State:        s.state.String(),
		HasPlaylist:  s.hasPlaylistLocked() || multi,
		Quality:      s.quality,
		MultiAudio:   multi,
		AudioTracks:  append([]AudioTrack(nil), s.audioTracks...),
		EmbeddedSubs: append([]SubtitleTrack(nil), s.subtitleTracks...),
	}
	if ents, err := os.ReadDir(s.dir); err == nil {
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".ts") {
				st.Segments++
			}
		}
	}
	return st
}

func (st sessState) String() string {
	switch st {
	case stateStarting:
		return "starting"
	case stateRunning:
		return "running"
	case stateDone:
		return "done"
	case stateFailed:
		return "failed"
	default:
		return "idle"
	}
}

// PlaylistPath 返回前端应拉取的播放列表路径：
// 多音轨布局返回 master.m3u8，简单布局返回 index.m3u8；文件尚不存在时返回错误。
func (s *session) PlaylistPath() (string, error) {
	s.mu.Lock()
	multi := s.hasMasterLocked()
	s.mu.Unlock()
	name := hlsPlaylistName
	if multi {
		name = hlsMasterName
	}
	p := filepath.Join(s.dir, name)
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// PlaylistContent 返回入口播放列表的字节内容。
// auto（ABR）会话的 master 由 ffmpeg 在转码过程中反复覆写，后台重写器的轮询间隙
// 可能短暂残留无 RESOLUTION 的纯音频 STREAM-INF，因此在读取出口再做一次确定性剔除，
// 保证播放器任何时刻拿到的 master 都不含纯音频清晰度；不落盘、不影响 ffmpeg 写文件。
// 其余会话直接返回磁盘原始内容。
func (s *session) PlaylistContent() ([]byte, error) {
	p, err := s.PlaylistPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if s.quality == QualityAuto {
		return []byte(stripAudioOnlyStreamInf(string(raw))), nil
	}
	return raw, nil
}

// hlsAssetName 白名单：
//   - 简单布局：index.m3u8、seg-<5 位数字>.ts
//   - 多音轨布局：master.m3u8、out-<变体>-<5 位数字>.ts、out-<变体>.m3u8
//
// 严格校验杜绝路径穿越与日志读取。
// 内嵌字幕 sub-N.vtt 已改为视频级共享，走 Transcoder.SubAssetPath，不在此列。
var (
	simpleSegRE  = regexp.MustCompile(`^seg-\d{5}\.ts$`)
	variantRE    = regexp.MustCompile(`^out-[0-9A-Za-z_-]+$`)
	variantSegRE = regexp.MustCompile(`^out-[0-9A-Za-z_-]+-\d{5}\.ts$`)
	subVTTRE     = regexp.MustCompile(`^sub-\d+\.vtt$`) // 共享内嵌字幕提取产物（SubAssetPath 白名单）
)

// AssetPath 校验 HLS 资源名并返回磁盘路径，非法或不存在时返回错误。
func (s *session) AssetPath(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", os.ErrNotExist
	}
	switch {
	case name == hlsPlaylistName || name == hlsMasterName:
	case simpleSegRE.MatchString(name):
	case strings.HasSuffix(name, ".m3u8") && variantRE.MatchString(strings.TrimSuffix(name, ".m3u8")):
	case variantSegRE.MatchString(name):
	default:
		return "", os.ErrNotExist
	}
	p := filepath.Join(s.dir, name)
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// SegmentPath 兼容旧调用：简单布局的分片名校验。
func (s *session) SegmentPath(name string) (string, error) {
	if !simpleSegRE.MatchString(name) {
		return "", os.ErrNotExist
	}
	p := filepath.Join(s.dir, name)
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

func (s *session) hasPlaylistLocked() bool {
	_, err := os.Stat(filepath.Join(s.dir, hlsPlaylistName))
	return err == nil
}

func (s *session) hasMasterLocked() bool {
	_, err := os.Stat(filepath.Join(s.dir, hlsMasterName))
	return err == nil
}

// setAudioTracks 记录 ffprobe 探测结果，供状态 API 返回音轨信息。
func (s *session) setAudioTracks(tracks []AudioTrack) {
	s.mu.Lock()
	s.audioTracks = append([]AudioTrack(nil), tracks...)
	s.mu.Unlock()
}

// setSubtitleTracks 记录 ffprobe 探测到的内嵌字幕轨。
func (s *session) setSubtitleTracks(tracks []SubtitleTrack) {
	s.mu.Lock()
	s.subtitleTracks = append([]SubtitleTrack(nil), tracks...)
	s.mu.Unlock()
}

// SubtitleTracks 返回内嵌字幕轨快照（含位图轨），供状态 API 渲染字幕菜单。
func (s *session) SubtitleTracks() []SubtitleTrack {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SubtitleTrack(nil), s.subtitleTracks...)
}

// stop 主动终止 ffmpeg 进程（保留磁盘分片）。
func (s *session) stop() {
	s.mu.Lock()
	c := s.cancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

// Touch 刷新最近访问时间（播放器轮询 m3u8、拉分片、查状态时调用）。
func (s *session) Touch() { s.lastAccessV.store(time.Now()) }

func (s *session) lastAccess() time.Time { return s.lastAccessV.load() }
