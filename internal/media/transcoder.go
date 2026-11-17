package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	hlsSegmentTime  = 4 // 单个 HLS 分片时长（秒）
)

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
	maxConcurrent int
	idleTimeout   time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	sem      chan struct{} // 并发转码任务信号量
	mu       sync.Mutex
	sessions map[string]*session
	wg       sync.WaitGroup // 等待全部 ffmpeg 进程退出
}

// NewTranscoder 创建转码器并清空旧的转码产物（残留的临时目录不可信）。
func NewTranscoder(dir, ffmpeg string, maxConcurrent int, idleTimeout time.Duration) (*Transcoder, error) {
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
		maxConcurrent: maxConcurrent,
		idleTimeout:   idleTimeout,
		ctx:           ctx,
		cancel:        cancel,
		sem:           make(chan struct{}, maxConcurrent),
		sessions:      map[string]*session{},
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
	State       string `json:"state"` // idle | starting | running | done | failed
	HasPlaylist bool   `json:"hasPlaylist"`
	Segments    int    `json:"segments"`
}

// Ensure 返回（必要时启动）视频对应的转码会话。
// 槽位已满且这是新排队任务时立即返回 ErrTranscoderBusy（会话已入队，前端轮询即可）；
// 会话已在 starting/running/done 时直接返回会话与当前状态。
func (t *Transcoder) Ensure(v *Video) (*session, error) {
	if !t.Available() {
		return nil, ErrTranscoderDisabled
	}

	t.mu.Lock()
	sess, ok := t.sessions[v.ID]
	if ok && sess.getState() == stateFailed {
		// 失败会话丢弃，下一轮以全新状态重试
		delete(t.sessions, v.ID)
		ok = false
	}
	if !ok {
		sess = newSession(v, filepath.Join(t.dir, v.ID))
		t.sessions[v.ID] = sess
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
		if cur := t.sessions[v.ID]; cur == sess {
			delete(t.sessions, v.ID)
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

	ctx, cancel := context.WithCancel(t.ctx)
	sess.setCancel(cancel)
	defer cancel()

	args := []string{
		"-y",
		"-i", sess.video.AbsPath(),
		"-map", "0:v:0", "-map", "0:a:0?", // 取第一路视频；无音频轨也不报错
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "23",
		"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2", // H.264 要求偶数像素
		"-c:a", "aac", "-b:a", "128k",
		"-ac", "2",
		"-sn", // 丢弃字幕轨（字幕挂载是后续路线图项）
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", hlsSegmentTime),
		// 不设 hls_playlist_type=vod：vod 语义下 ffmpeg 到转码结束才写 m3u8，
		// 无法边转边播。默认 event 语义下列表实时追加，完成时自动补 ENDLIST。
		// 不使用 temp_file：该标志会让 m3u8 也走临时文件、进程结束才改名。
		"-hls_flags", "append_list+independent_segments",
		"-hls_list_size", "0", // 保留全部分片，支持回看与断点续播
		"-hls_segment_filename", filepath.Join(sess.dir, "seg-%05d.ts"),
		filepath.Join(sess.dir, hlsPlaylistName),
	}

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

// Get 按视频 ID 查找会话（不存在返回 nil）。
func (t *Transcoder) Get(videoID string) *session {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessions[videoID]
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
	video *Video
	dir   string

	started   chan struct{} // ffmpeg 进程尝试启动后关闭（成功或失败）
	startOnce sync.Once

	mu     sync.Mutex
	state  sessState
	cancel context.CancelFunc

	lastAccessV atomicTime // 最近一次被播放器/API 访问的时间
}

func newSession(v *Video, dir string) *session {
	s := &session{
		video:   v,
		dir:     dir,
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
	st := sessionStatus{State: s.state.String(), HasPlaylist: s.hasPlaylistLocked()}
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

// PlaylistPath 返回 m3u8 文件路径；文件尚不存在时返回错误（播放器稍后重试）。
func (s *session) PlaylistPath() (string, error) {
	p := filepath.Join(s.dir, hlsPlaylistName)
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// SegmentPath 校验分片名并返回路径。
// 只允许 seg-<5 位数字>.ts 形式，杜绝路径穿越；文件不存在时返回错误。
func (s *session) SegmentPath(name string) (string, error) {
	if name != filepath.Base(name) || !strings.HasPrefix(name, "seg-") ||
		!strings.HasSuffix(name, ".ts") {
		return "", os.ErrNotExist
	}
	num := strings.TrimSuffix(strings.TrimPrefix(name, "seg-"), ".ts")
	if len(num) != 5 {
		return "", os.ErrNotExist
	}
	if _, err := strconv.Atoi(num); err != nil {
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
