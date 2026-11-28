package media

import (
	"os"
	"sort"
	"time"
)

// TranscodeSessionView 是单个转码会话的管理快照，供转码任务页展示与轮询。
// 字段全部为值类型/不可变切片，避免把内部 *session 与锁暴露给 server 层。
type TranscodeSessionView struct {
	VideoID    string `json:"videoId"`
	VideoName  string `json:"videoName"`
	Quality    string `json:"quality"`
	State      string `json:"state"` // idle | starting | running | done | failed
	Segments   int    `json:"segments"`
	Bytes      int64  `json:"bytes"`      // 该会话产物目录占用字节
	LastAccess string `json:"lastAccess"` // RFC3339，最近一次被播放器/API 访问时间
	MultiAudio bool   `json:"multiAudio"`
}

// TranscodeSummary 是转码器整体的管理概览。
type TranscodeSummary struct {
	Available     bool                   `json:"available"`     // ffmpeg 是否可用
	HWAccel       string                 `json:"hwAccel"`       // 硬件加速模式：空=软转，vaapi=VAAPI
	MaxConcurrent int                    `json:"maxConcurrent"` // 并发转码槽位上限
	TotalBytes    int64                  `json:"totalBytes"`    // 转码产物根目录总占用
	Sessions      []TranscodeSessionView `json:"sessions"`
}

// MaxConcurrent 返回并发转码槽位上限，供管理页展示容量。
func (t *Transcoder) MaxConcurrent() int { return t.maxConcurrent }

// ManagementSnapshot 汇总转码器配置、全部会话与磁盘占用，供管理页一次拉取。
// 转码器未启用（nil）时由 server 层直接返回不可用响应，不会调用本方法。
func (t *Transcoder) ManagementSnapshot() TranscodeSummary {
	t.mu.Lock()
	sessions := make([]*session, 0, len(t.sessions))
	for _, sess := range t.sessions {
		sessions = append(sessions, sess)
	}
	hwAccel := t.hwaccel
	maxConcurrent := t.maxConcurrent
	rootDir := t.dir
	t.mu.Unlock()

	// 会话顺序固定：按视频名、再按档位，保证轮询时表格不跳动
	sort.Slice(sessions, func(i, j int) bool {
		vi, vj := sessions[i].video.Name, sessions[j].video.Name
		if vi != vj {
			return vi < vj
		}
		return sessions[i].quality < sessions[j].quality
	})

	summary := TranscodeSummary{
		Available:     t.Available(),
		HWAccel:       hwAccel,
		MaxConcurrent: maxConcurrent,
		Sessions:      make([]TranscodeSessionView, 0, len(sessions)),
	}
	for _, sess := range sessions {
		summary.Sessions = append(summary.Sessions, sess.managementView())
	}
	// 根目录总占用包含已被终止但残留、尚未从 map 摘除的会话产物
	summary.TotalBytes = dirSize(rootDir)
	return summary
}

// StopSession 主动终止指定视频+档位的转码会话并摘除其产物与 map 条目。
// 返回 false 表示会话不存在（已被回收或从未创建）。终止后该档位下次播放会重新转码。
func (t *Transcoder) StopSession(videoID, quality string) bool {
	if quality != QualityAuto {
		quality = NormalizeQuality(quality)
	}
	key := sessionKey(videoID, quality)

	t.mu.Lock()
	sess, ok := t.sessions[key]
	if !ok {
		t.mu.Unlock()
		return false
	}
	delete(t.sessions, key)
	dir := sess.dir
	t.mu.Unlock()

	// stop 在锁外执行：cancel 会让 run goroutine 退出，避免持锁等待进程
	sess.stop()
	_ = os.RemoveAll(dir)
	return true
}

// managementView 采集单个会话的展示快照，磁盘大小通过遍历产物目录统计。
func (s *session) managementView() TranscodeSessionView {
	st := s.Status()
	return TranscodeSessionView{
		VideoID:    s.video.ID,
		VideoName:  s.video.Name,
		Quality:    s.quality,
		State:      st.State,
		Segments:   st.Segments,
		Bytes:      dirSize(s.dir),
		LastAccess: s.lastAccess().Format(time.RFC3339),
		MultiAudio: st.MultiAudio,
	}
}
