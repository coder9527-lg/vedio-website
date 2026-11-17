package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("MEDIA_DIR", t.TempDir()) // 避免触发目录不可访问的警告分支
	t.Setenv("WATCH_ENABLED", "")
	t.Setenv("WATCH_DEBOUNCE", "")
	t.Setenv("RESCAN_INTERVAL", "")

	cfg := Load()
	if !cfg.WatchEnabled {
		t.Error("WatchEnabled 默认应为 true")
	}
	if cfg.WatchDebounce != 2*time.Second {
		t.Errorf("默认防抖期望 2s，实际 %s", cfg.WatchDebounce)
	}
	if cfg.RescanInterval != 0 {
		t.Errorf("定时扫描默认应关闭，实际 %s", cfg.RescanInterval)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("MEDIA_DIR", t.TempDir())
	t.Setenv("WATCH_ENABLED", "false")
	t.Setenv("WATCH_DEBOUNCE", "500ms")
	t.Setenv("RESCAN_INTERVAL", "10m")

	cfg := Load()
	if cfg.WatchEnabled {
		t.Error("WATCH_ENABLED=false 时应关闭监听")
	}
	if cfg.WatchDebounce != 500*time.Millisecond {
		t.Errorf("防抖期望 500ms，实际 %s", cfg.WatchDebounce)
	}
	if cfg.RescanInterval != 10*time.Minute {
		t.Errorf("定时扫描期望 10m，实际 %s", cfg.RescanInterval)
	}
}

func TestParseDurationInvalidFallsBack(t *testing.T) {
	t.Setenv("BAD_DURATION", "not-a-duration")
	got := parseDuration("BAD_DURATION", 7*time.Second)
	if got != 7*time.Second {
		t.Errorf("非法时长应回退默认值，实际 %s", got)
	}

	t.Setenv("EMPTY_DURATION", "")
	if got := parseDuration("EMPTY_DURATION", 3*time.Second); got != 3*time.Second {
		t.Errorf("空值应回退默认值，实际 %s", got)
	}

	t.Setenv("GOOD_DURATION", "90s")
	if got := parseDuration("GOOD_DURATION", time.Second); got != 90*time.Second {
		t.Errorf("合法时长解析错误，实际 %s", got)
	}
}
