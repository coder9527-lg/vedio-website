package media

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// waitFor 轮询条件直到成立或超时，避免给文件事件处理写死 sleep。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待条件超时未满足")
}

func newTestWatcher(t *testing.T, debounce time.Duration) (*Library, *Watcher, string) {
	t.Helper()
	root := t.TempDir()
	lib := NewLibrary(root, DefaultExts)
	w := NewWatcher(lib, root, debounce)
	go w.Run()
	t.Cleanup(w.Stop)
	// 重试式就绪探测：watcher 完成 addWatchRecursive 前创建的文件会漏掉 CREATE 事件，
	// 因此每轮用全新文件名重试，直到事件能被稳定感知（入库 + 删除均生效）。
	deadline := time.Now().Add(3 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		probe := filepath.Join(root, ".probe"+strconv.Itoa(i)+".mp4")
		if err := os.WriteFile(probe, []byte("1"), 0o644); err != nil {
			t.Fatal(err)
		}
		ok := false
		for inner := time.Now().Add(300 * time.Millisecond); time.Now().Before(inner); {
			if lib.Len() == 1 {
				ok = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if ok {
			if err := os.Remove(probe); err != nil {
				t.Fatal(err)
			}
			waitFor(t, time.Second, func() bool { return lib.Len() == 0 })
			return lib, w, root
		}
		_ = os.Remove(probe)
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("文件监听就绪探测超时")
	return lib, w, root
}

func TestWatcherIncrementalAddAndDelete(t *testing.T) {
	lib, _, root := newTestWatcher(t, 30*time.Millisecond)

	f := filepath.Join(root, "a.mp4")
	if err := os.WriteFile(f, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return lib.Len() == 1 })

	if _, ok := lib.Get(makeID("a.mp4")); !ok {
		t.Fatal("新建文件未被增量索引")
	}

	// 非视频文件不应入库
	txt := filepath.Join(root, "note.txt")
	if err := os.WriteFile(txt, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if lib.Len() != 1 {
		t.Fatalf("非视频文件被入库，Len=%d", lib.Len())
	}

	// 删除文件后索引应同步移除
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return lib.Len() == 0 })
}

func TestWatcherNewSubdirectory(t *testing.T) {
	lib, _, root := newTestWatcher(t, 30*time.Millisecond)

	// 运行中新建子目录，并在其中放入视频（目录内已有文件也要被扫描到）
	sub := filepath.Join(root, "movies")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(sub, "b.mkv")
	if err := os.WriteFile(f, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return lib.Len() == 1 })

	got := lib.List()[0]
	if got.RelPath != "movies/b.mkv" {
		t.Fatalf("期望 movies/b.mkv，实际 %s", got.RelPath)
	}

	// 删除整个目录，其下条目应一并移除
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return lib.Len() == 0 })
}

func TestWatcherRename(t *testing.T) {
	lib, _, root := newTestWatcher(t, 30*time.Millisecond)

	old := filepath.Join(root, "old.mp4")
	if err := os.WriteFile(old, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return lib.Len() == 1 })

	// 重命名：旧 ID 消失，新路径入库
	new := filepath.Join(root, "new.mp4")
	if err := os.Rename(old, new); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, oldOK := lib.Get(makeID("old.mp4"))
		_, newOK := lib.Get(makeID("new.mp4"))
		return !oldOK && newOK
	})
}

func TestWatcherSubtitleSidecar(t *testing.T) {
	lib, _, root := newTestWatcher(t, 30*time.Millisecond)

	video := filepath.Join(root, "show.mkv")
	if err := os.WriteFile(video, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return lib.Len() == 1 })
	vid, _ := lib.Get(makeID("show.mkv"))
	if got := lib.Subtitles(vid.ID); len(got) != 0 {
		t.Fatalf("初始应无字幕，got %d", len(got))
	}

	// 视频落地后新增同名字幕，应被自动挂载
	sub := filepath.Join(root, "show.zh.srt")
	if err := os.WriteFile(sub, []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return len(lib.Subtitles(vid.ID)) == 1 })
	if got := lib.Subtitles(vid.ID); got[0].Lang != "zh-Hans" || !got[0].Default {
		t.Fatalf("字幕语言/默认标记异常: %+v", got)
	}

	// 删除字幕后索引应同步清空
	if err := os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return len(lib.Subtitles(vid.ID)) == 0 })
}

func TestWatcherSubtitleInSubdirectory(t *testing.T) {
	lib, _, root := newTestWatcher(t, 30*time.Millisecond)

	video := filepath.Join(root, "show.mkv")
	if err := os.WriteFile(video, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return lib.Len() == 1 })
	vid, _ := lib.Get(makeID("show.mkv"))

	// 字幕放在运行中新建的 subs 子目录（Jellyfin 风格）
	subDir := filepath.Join(root, "subs")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(subDir, "show.zh.srt")
	if err := os.WriteFile(sub, []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return len(lib.Subtitles(vid.ID)) == 1 })
	if got := lib.Subtitles(vid.ID)[0]; got.Lang != "zh-Hans" || filepath.Dir(got.RelPath) != "subs" {
		t.Fatalf("子目录字幕关联异常: %+v", got)
	}

	if err := os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return len(lib.Subtitles(vid.ID)) == 0 })
}

func TestWatcherLateMountedRoot(t *testing.T) {
	// 媒体目录先不存在，启动 watcher，随后再创建（模拟 NAS 晚挂载）
	root := filepath.Join(t.TempDir(), "media")
	lib := NewLibrary(root, DefaultExts)

	origRetry, origHealth := watchRetryInterval, healthCheckInterval
	watchRetryInterval = 20 * time.Millisecond
	healthCheckInterval = 30 * time.Millisecond
	t.Cleanup(func() {
		watchRetryInterval = origRetry
		healthCheckInterval = origHealth
	})

	w := NewWatcher(lib, root, 30*time.Millisecond)
	go w.Run()
	t.Cleanup(w.Stop)

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(root, "a.mp4")
	if err := os.WriteFile(f, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return lib.Len() == 1 })
}

func TestWatcherDebounceCoalescesWrites(t *testing.T) {
	lib, _, root := newTestWatcher(t, 100*time.Millisecond)

	f := filepath.Join(root, "big.mp4")
	// 模拟大文件拷贝：持续写入产生大量 Write 事件，期间不应提前入库
	if err := os.WriteFile(f, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		fp, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fp.WriteString("0123456789"); err != nil {
			t.Fatal(err)
		}
		fp.Close()
		time.Sleep(20 * time.Millisecond)
	}

	waitFor(t, 2*time.Second, func() bool {
		v, ok := lib.Get(makeID("big.mp4"))
		return ok && v.Size == 101
	})
	if lib.Len() != 1 {
		t.Fatalf("期望仅 1 个条目，实际 %d", lib.Len())
	}
}
