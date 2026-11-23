package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Thumbnailer 负责用 ffmpeg 按需生成并缓存视频缩略图。
// 缩略图按视频 ID 命名，已生成的直接命中缓存，不重复调用 ffmpeg。
type Thumbnailer struct {
	dir    string
	ffmpeg string

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex // 每个视频一把锁，避免并发生成同一张图
}

func NewThumbnailer(dir, ffmpeg string) (*Thumbnailer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	t := &Thumbnailer{
		dir:    dir,
		ffmpeg: ffmpeg,
		locks:  make(map[string]*sync.Mutex),
	}
	return t, nil
}

// Available 返回当前环境 ffmpeg 是否可用。
func (t *Thumbnailer) Available() bool {
	_, err := exec.LookPath(t.ffmpeg)
	return err == nil
}

// Dir 返回缩略图缓存根目录。
func (t *Thumbnailer) Dir() string { return t.dir }

// Path 返回某个视频缩略图的缓存路径。
func (t *Thumbnailer) Path(videoID string) string {
	return filepath.Join(t.dir, videoID+".jpg")
}

// Ensure 确保缩略图已生成，返回其文件路径；生成失败时返回错误。
func (t *Thumbnailer) Ensure(v *Video) (string, error) {
	out := t.Path(v.ID)
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}

	mu := t.lockFor(v.ID)
	mu.Lock()
	defer mu.Unlock()

	// 双重检查：可能在等锁期间已被其他请求生成
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 取第 10 秒的帧；短视频则退回起始帧
	cmd := exec.CommandContext(ctx, t.ffmpeg,
		"-y",
		"-ss", "10",
		"-i", v.AbsPath(),
		"-frames:v", "1",
		"-vf", "scale=480:-2",
		"-q:v", "4",
		out,
	)
	if err := cmd.Run(); err != nil {
		cmd = exec.CommandContext(ctx, t.ffmpeg,
			"-y",
			"-i", v.AbsPath(),
			"-frames:v", "1",
			"-vf", "scale=480:-2",
			"-q:v", "4",
			out,
		)
		if err := cmd.Run(); err != nil {
			return "", err
		}
	}
	return out, nil
}

func (t *Thumbnailer) lockFor(id string) *sync.Mutex {
	t.locksMu.Lock()
	defer t.locksMu.Unlock()
	mu, ok := t.locks[id]
	if !ok {
		mu = &sync.Mutex{}
		t.locks[id] = mu
	}
	return mu
}
