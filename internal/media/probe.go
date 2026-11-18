package media

import (
	"context"
	"encoding/json"
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

// ffprobeJSON 仅解析我们关心的字段，忽略其余元信息。
type ffprobeJSON struct {
	Streams []struct {
		Index       int    `json:"index"`
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		Disposition struct {
			Default int `json:"default"`
		} `json:"disposition"`
		Tags map[string]string `json:"tags"`
	} `json:"streams"`
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
