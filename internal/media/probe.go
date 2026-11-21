package media

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProbeTimeout 限制 ffprobe 单次执行时长，损坏文件不应长时间拖住转码会话。
const ProbeTimeout = 30 * time.Second

// AudioTrack 描述一路音频流，供多音轨 HLS 转码与前端音轨选择使用。
type AudioTrack struct {
	Index    int    `json:"index"` // 在输入文件中的流序号（ffmpeg -map 0:<index>）
	Codec    string `json:"codec"`
	Language string `json:"lang"`  // 语言标签（chi/eng/...），缺失为 "und"
	Title    string `json:"title"` // 容器内自带的音轨标题，可能为空
	Label    string `json:"label"` // 供前端展示的名称
	Default  bool   `json:"default"`
}

// SubtitleTrack 描述一路内嵌字幕流。文本轨（subrip/ass/webvtt/mov_text 等）可提取为
// WebVTT sidecar；位图轨（PGS/DVD/DVB/XSUB）只能在列表中标注，浏览器无法渲染。
type SubtitleTrack struct {
	Index    int    `json:"index"` // 在输入文件中的流序号（ffmpeg -map 0:<index>）
	Codec    string `json:"codec"` // subrip / ass / webvtt / hdmv_pgs_subtitle / ...
	Language string `json:"lang"`  // 语言标签，缺失为 "und"
	Title    string `json:"title"` // 容器内自带的字幕标题
	Label    string `json:"label"` // 供前端展示的名称
	Default  bool   `json:"default"`
	Text     bool   `json:"text"`   // 是否为可提取的文本字幕
	Bitmap   bool   `json:"bitmap"` // 是否为位图字幕（PGS/DVD 等，无法转 WebVTT）
}

// bitmapSubtitleCodecs 无法转成 WebVTT 的位图字幕编码。
var bitmapSubtitleCodecs = map[string]bool{
	"hdmv_pgs_subtitle": true, // Blu-ray PGS
	"dvd_subtitle":      true, // DVD-VOBSUB
	"dvb_subtitle":      true, // DVB
	"dvb_teletext":      true,
	"xsub":              true, // AVI DivX
	"dvdsub":            true,
}

// ProbeSubtitles 用 ffprobe 列出视频中的全部字幕流（按流序号升序）。
// 无字幕时返回 nil, nil；探测失败由调用方决定是否降级。
func ProbeSubtitles(ffprobeBin, path string) ([]SubtitleTrack, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobeBin,
		"-v", "error",
		"-show_entries", "stream=index,codec_type,codec_name:stream_disposition=default:stream_tags=language,title",
		"-of", "json",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var parsed ffprobeJSON
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}

	var tracks []SubtitleTrack
	seenDefault := false
	ordinal := 0
	for _, st := range parsed.Streams {
		if st.CodecType != "subtitle" {
			continue
		}
		ordinal++
		bitmap := bitmapSubtitleCodecs[st.CodecName]
		track := SubtitleTrack{
			Index:    st.Index,
			Codec:    st.CodecName,
			Language: normalizeProbeLang(st.Tags["language"]),
			Title:    st.Tags["title"],
			Default:  st.Disposition.Default == 1 && !seenDefault,
			Text:     !bitmap,
			Bitmap:   bitmap,
		}
		if track.Default {
			seenDefault = true
		}
		track.Label = subtitleTrackLabel(track, ordinal)
		tracks = append(tracks, track)
	}
	// 容器未标记默认轨时，第一条文本轨视为默认
	if len(tracks) > 0 && !seenDefault {
		for i := range tracks {
			if tracks[i].Text {
				tracks[i].Default = true
				break
			}
		}
	}
	return tracks, nil
}

// subtitleTrackLabel 生成内嵌字幕展示名：优先标题，其次语言名，最后用序号兜底。
func subtitleTrackLabel(t SubtitleTrack, ordinal int) string {
	if t.Title != "" {
		return t.Title
	}
	if name, ok := languageNames[probeLangKey(t.Language)]; ok {
		if t.Bitmap {
			return name + "（图形字幕）"
		}
		return name
	}
	return "字幕 " + strconv.Itoa(ordinal)
}

// ffprobeJSON 仅解析我们关心的字段，忽略其余元信息。
type ffprobeJSON struct {
	Streams []struct {
		Index       int    `json:"index"`
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		Width       int    `json:"width"`  // 视频流宽度（非视频流为 0）
		Height      int    `json:"height"` // 视频流高度（非视频流为 0）
		Disposition struct {
			Default int `json:"default"`
		} `json:"disposition"`
		Tags map[string]string `json:"tags"`
	} `json:"streams"`
}

// ProbeVideoHeight 用 ffprobe 返回第一个视频流的像素高度，供 ABR 变体过滤（不放大源）。
// 无视频流或探测失败时返回错误，由调用方决定降级策略。
func ProbeVideoHeight(ffprobeBin, path string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobeBin,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=height",
		"-of", "json",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	var parsed ffprobeJSON
	if err := json.Unmarshal(out, &parsed); err != nil {
		return 0, err
	}
	// 已用 -select_streams v:0 限定视频流，返回条目只含 height 字段（无 codec_type），
	// 因此直接取第一个正高度即可。
	for _, st := range parsed.Streams {
		if st.Height > 0 {
			return st.Height, nil
		}
	}
	return 0, errors.New("ffprobe 未探测到有效视频流高度")
}

// ProbeAudio 用 ffprobe 列出视频中的全部音频流（按流序号升序）。
// ffprobe 不可用或探测失败时返回错误，由调用方降级为单音轨转码。
func ProbeAudio(ffprobeBin, path string) ([]AudioTrack, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobeBin,
		"-v", "error",
		"-show_entries", "stream=index,codec_type,codec_name:stream_disposition=default:stream_tags=language,title",
		"-of", "json",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var parsed ffprobeJSON
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}

	var tracks []AudioTrack
	seenDefault := false
	for _, st := range parsed.Streams {
		if st.CodecType != "audio" {
			continue
		}
		lang := normalizeProbeLang(st.Tags["language"])
		track := AudioTrack{
			Index:    st.Index,
			Codec:    st.CodecName,
			Language: lang,
			Title:    st.Tags["title"],
			Default:  st.Disposition.Default == 1 && !seenDefault,
		}
		if track.Default {
			seenDefault = true
		}
		track.Label = audioTrackLabel(track, len(tracks)+1)
		tracks = append(tracks, track)
	}
	// 容器未标记默认音轨时，第一路视为默认
	if len(tracks) > 0 && !seenDefault {
		tracks[0].Default = true
		tracks[0].Label = audioTrackLabel(tracks[0], 1)
	}
	return tracks, nil
}

// audioTrackLabel 生成音轨展示名：优先容器标题，其次语言名，最后用序号兜底。
func audioTrackLabel(t AudioTrack, ordinal int) string {
	if t.Title != "" {
		return t.Title
	}
	if name, ok := languageNames[probeLangKey(t.Language)]; ok {
		return name
	}
	return "音轨 " + strconv.Itoa(ordinal)
}

// normalizeProbeLang 归一 ffprobe 返回的语言标签（ISO 639-2/3 或 BCP-47 都可能出现）。
func normalizeProbeLang(raw string) string {
	if raw == "" {
		return "und"
	}
	lang := strings.ToLower(strings.TrimSpace(raw))
	// 常见 ISO 639-2 三字母代码映射到两字母
	if two, ok := iso6392To6391[lang]; ok {
		return two
	}
	return lang
}

func probeLangKey(lang string) string {
	// languageNames 的键是小写两字母（或 zh-cn 这类），做一次适配
	if _, ok := languageNames[lang]; ok {
		return lang
	}
	return lang
}

// iso6392To6391 常见三字母语言代码 → 两字母代码。
var iso6392To6391 = map[string]string{
	"chi": "zh", "zho": "zh",
	"eng": "en", "jpn": "ja", "kor": "ko",
	"fre": "fr", "fra": "fr", "ger": "de", "deu": "de",
	"spa": "es", "rus": "ru", "ita": "it", "por": "pt",
	"tha": "th", "vie": "vi", "ara": "ar", "hin": "hi",
}
