package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// SubtitleConverter 负责把非 VTT 字幕按需转换为浏览器原生支持的 WebVTT。
// 转换结果按字幕 ID 缓存，原文件更新后自动失效重转。
type SubtitleConverter struct {
	dir    string
	ffmpeg string

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex // 每个字幕一把锁，避免并发转换同一文件
}

// NewSubtitleConverter 创建转换器，缓存目录通常位于缩略图目录下的 subs 子目录。
func NewSubtitleConverter(dir, ffmpeg string) (*SubtitleConverter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &SubtitleConverter{
		dir:    dir,
		ffmpeg: ffmpeg,
		locks:  make(map[string]*sync.Mutex),
	}, nil
}

// Available 返回当前环境 ffmpeg 是否可用。
func (c *SubtitleConverter) Available() bool {
	_, err := exec.LookPath(c.ffmpeg)
	return err == nil
}

// cachePath 返回某个字幕转换后的 VTT 缓存路径。
func (c *SubtitleConverter) cachePath(subID string) string {
	return filepath.Join(c.dir, subID+".vtt")
}

// Ensure 返回可供 <track> 使用的 VTT 文件路径。
// 原本就是 VTT 的字幕直接返回原文件；其余格式经 ffmpeg 转换并缓存。
func (c *SubtitleConverter) Ensure(s *Subtitle) (string, error) {
	if s.Format == "vtt" {
		return s.AbsPath(), nil
	}

	out := c.cachePath(s.ID)
	srcInfo, err := os.Stat(s.AbsPath())
	if err != nil {
		return "", err
	}
	// 缓存存在且不早于源文件修改时间，直接命中
	if info, err := os.Stat(out); err == nil && !info.ModTime().Before(srcInfo.ModTime()) {
		return out, nil
	}

	mu := c.lockFor(s.ID)
	mu.Lock()
	defer mu.Unlock()

	// 双重检查：可能在等锁期间已被其他请求转换
	if info, err := os.Stat(out); err == nil && !info.ModTime().Before(srcInfo.ModTime()) {
		return out, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.ffmpeg,
		"-y",
		"-i", s.AbsPath(),
		"-f", "webvtt",
		out,
	)
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out, nil
}

func (c *SubtitleConverter) lockFor(id string) *sync.Mutex {
	c.locksMu.Lock()
	defer c.locksMu.Unlock()
	mu, ok := c.locks[id]
	if !ok {
		mu = &sync.Mutex{}
		c.locks[id] = mu
	}
	return mu
}
