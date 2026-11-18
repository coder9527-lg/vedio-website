package media

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher 基于 fsnotify 监听媒体目录，把文件事件防抖后转为增量索引更新。
//
// 设计要点：
//   - 递归监听，运行中新建的子目录会被自动纳入监听；
//   - 事件防抖：拷贝大文件期间 Write 事件持续触发，定时器不断重置，
//     直到事件静默达到 debounce 窗口才处理，避免把未拷完的文件入库；
//   - 媒体目录晚挂载（NAS 常见）时按固定间隔重试，直到目录出现；
//   - 网络文件系统（NFS/SMB）可能不产生 inotify 事件，可配合定时全量扫描兜底。
type Watcher struct {
	library  *Library
	root     string
	debounce time.Duration

	watcher *fsnotify.Watcher
	stop    chan struct{}
	done    chan struct{}

	// pending 收集防抖窗口内需要处理的路径；true 表示可能已删除
	pending map[string]bool
}

// watchRetryInterval 是媒体目录不可用时的重试间隔，声明为变量便于测试覆写。
var watchRetryInterval = 5 * time.Second

// healthCheckInterval 是监听运行期间对媒体目录做存活检查的间隔，
// 用于检测 NAS 卸载/晚挂载后失效的 watch 并自动重建。
var healthCheckInterval = 30 * time.Second

func NewWatcher(library *Library, root string, debounce time.Duration) *Watcher {
	return &Watcher{
		library:  library,
		root:     root,
		debounce: debounce,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		pending:  make(map[string]bool),
	}
}

// Run 启动监听，阻塞直到 Stop 被调用。媒体目录暂不可用时会等待重试。
func (w *Watcher) Run() {
	defer close(w.done)

	for {
		if err := w.runOnce(); err != nil {
			log.Printf("文件监听退出: %v", err)
		}
		// 重试等待期间同时探测目录是否提前出现（晚挂载场景），避免死等整个间隔
		retry := time.NewTimer(watchRetryInterval)
		probe := time.NewTicker(100 * time.Millisecond)
	waitRetry:
		for {
			select {
			case <-w.stop:
				retry.Stop()
				probe.Stop()
				return
			case <-retry.C:
				break waitRetry
			case <-probe.C:
				if info, err := os.Stat(w.root); err == nil && info.IsDir() {
					retry.Stop()
					break waitRetry
				}
			}
		}
		probe.Stop()
		log.Printf("重试文件监听: %s", w.root)
	}
}

func (w *Watcher) runOnce() error {
	// fsnotify.Add 对不存在的路径会静默跳过而不报错，这里显式校验，
	// 否则晚挂载场景下会得到一个零 watch 的空转监听器。
	if info, err := os.Stat(w.root); err != nil || !info.IsDir() {
		return fmt.Errorf("媒体目录不可用: %s", w.root)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.watcher = fsw
	defer fsw.Close()

	if err := w.addWatchRecursive(w.root); err != nil {
		return err
	}
	// （重）连上目录后先全量索引一次：从目录出现到 watch 注册之间落地的文件
	// 不会产生事件，必须靠这次扫描接住，避免晚挂载/重连后漏库。
	if n, err := w.library.Scan(); err != nil {
		log.Printf("监听启动全量扫描失败: %v", err)
	} else {
		log.Printf("监听启动全量扫描完成，索引视频 %d 个", n)
	}
	log.Printf("文件监听已启动: %s（防抖 %s）", w.root, w.debounce)

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var timerCh <-chan time.Time // nil 通道在 select 中永远阻塞，借此启用/禁用定时分支
	health := time.NewTicker(healthCheckInterval)
	defer health.Stop()

	for {
		select {
		case <-w.stop:
			timer.Stop()
			w.flush() // 优雅关闭前处理防抖窗口内残留的最后一批变更
			return nil

		case <-health.C:
			// root 被卸载或删除后，旧 watch 不再产生事件；返回后由 Run 重新建立监听
			if info, err := os.Stat(w.root); err != nil || !info.IsDir() {
				timer.Stop()
				w.flush()
				return errors.New("媒体目录已不可用，等待重新挂载")
			}

		case ev, ok := <-fsw.Events:
			if !ok {
				timer.Stop()
				w.flush()
				return errors.New("fsnotify 事件通道已关闭")
			}
			w.handleEvent(ev)
			// 任意事件都重置防抖定时器，静默达到 debounce 窗口后才 flush
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(w.debounce)
			timerCh = timer.C

		case err, ok := <-fsw.Errors:
			if !ok {
				timer.Stop()
				return errors.New("fsnotify 错误通道已关闭")
			}
			log.Printf("文件监听错误: %v", err)

		case <-timerCh:
			w.flush()
			timerCh = nil
		}
	}
}

// handleEvent 把原始 fsnotify 事件归并到 pending 集合，并即时处理新建目录的监听注册。
func (w *Watcher) handleEvent(ev fsnotify.Event) {
	// 纯权限变更不影响索引，忽略以减少网络盘上的无效刷新
	if ev.Op == fsnotify.Chmod {
		return
	}
	path := ev.Name
	maybeRemoved := ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0

	// 新建/重命名产生的目录：立刻加入监听，并扫描其中已有的文件
	if !maybeRemoved {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if err := w.addWatchRecursive(path); err != nil {
				log.Printf("监听新目录失败 %s: %v", path, err)
			}
			w.enqueueDir(path)
			return
		}
	}

	w.pending[path] = w.pending[path] || maybeRemoved
}

// enqueueDir 把目录下现存的视频与字幕文件全部放入待处理集合。
func (w *Watcher) enqueueDir(dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if w.library.IsVideoExt(path) || IsSubtitleExt(path) {
			w.pending[path] = false
		}
		return nil
	})
}

// flush 处理防抖窗口内累积的全部变更。
func (w *Watcher) flush() {
	if len(w.pending) == 0 {
		return
	}
	pending := w.pending
	w.pending = make(map[string]bool)

	added, removed := 0, 0
	for path, maybeRemoved := range pending {
		if _, err := os.Stat(path); err != nil {
			// 路径已不存在：可能是文件删除，也可能是整个目录被删
			removed += w.library.Remove(path)
			if IsSubtitleExt(path) {
				w.refreshSiblingSubtitles(path)
			} else if IsMetadataSidecar(path) {
				w.refreshSiblingMetadata(path)
			}
			continue
		}
		if maybeRemoved {
			// 重命名场景：旧路径已不存在（上面已删），新路径会以 Create 事件到达
			removed += w.library.Remove(path)
		}
		if IsSubtitleExt(path) {
			// 字幕文件增删：刷新同目录同名视频的字幕索引
			w.refreshSiblingSubtitles(path)
			continue
		}
		if IsMetadataSidecar(path) {
			// NFO/海报增删：刷新同目录视频的刮削元信息
			w.refreshSiblingMetadata(path)
			continue
		}
		if _, ok := w.library.Upsert(path); ok {
			added++
		}
	}
	if added > 0 || removed > 0 {
		log.Printf("文件监听增量更新完成: 新增/更新 %d 个，移除 %d 个，当前共 %d 个",
			added, removed, w.library.Len())
	}
}

// refreshSiblingSubtitles 在字幕文件变更后，刷新关联视频的字幕列表。
// 视频名可能含点号（Show.2024.mkv），因此不靠截断文件名，而是遍历库中视频，
// 复用与扫描一致的 subtitleStemMatch 规则匹配；字幕可能位于视频同目录或其
// subs/subtitles/字幕 子目录，两种位置都要覆盖。
func (w *Watcher) refreshSiblingSubtitles(subPath string) {
	dir := filepath.Dir(subPath)
	stem := strings.TrimSuffix(filepath.Base(subPath), filepath.Ext(subPath))
	for _, v := range w.library.List() {
		videoDir := filepath.Dir(v.AbsPath())
		inVideoDir := dir == videoDir
		inSubDir := false
		if !inVideoDir && filepath.Dir(dir) == videoDir {
			inSubDir = subtitleDirs[strings.ToLower(filepath.Base(dir))]
		}
		if !(inVideoDir || inSubDir) || !subtitleStemMatch(stem, v.Name) {
			continue
		}
		w.library.RefreshSubtitles(v.ID)
	}
}

// refreshSiblingMetadata 在 NFO/海报文件变更后，刷新同目录下视频的元信息。
// 目录级通用图（folder.jpg / poster.jpg / movie.nfo）会影响整个目录的视频，
// 因此同目录全部视频都刷新；带视频名前缀的附属文件只刷新匹配的那个。
func (w *Watcher) refreshSiblingMetadata(sidecarPath string) {
	dir := filepath.Dir(sidecarPath)
	stem := strings.TrimSuffix(filepath.Base(sidecarPath), filepath.Ext(sidecarPath))
	lowerStem := strings.ToLower(stem)
	for _, v := range w.library.List() {
		if filepath.Dir(v.AbsPath()) != dir {
			continue
		}
		// 目录级通用名（movie.nfo / poster.jpg / folder.jpg / fanart.png 等）刷新目录内全部视频；
		// 否则只刷新同名或「视频名+分隔符+标记」（如 movie-poster.jpg）所属的视频
		if lowerStem == "movie" || isGenericArtMarker(lowerStem) ||
			lowerStem == strings.ToLower(v.Name) || hasArtMarkerSuffix(lowerStem, strings.ToLower(v.Name)) {
			w.library.RefreshMetadata(v.ID)
		}
	}
}

// isGenericArtMarker 判断图片 stem 是否为不带视频名前缀的通用海报/背景图名。
func isGenericArtMarker(stem string) bool {
	for _, m := range posterFileMarkers {
		if stem == m {
			return true
		}
	}
	for _, m := range backdropMarkers {
		if stem == m {
			return true
		}
	}
	return false
}

// hasArtMarkerSuffix 判断图片/NFO 的 stem 是否为「视频名 + 分隔符 + 海报/背景标记」。
func hasArtMarkerSuffix(stem, videoStem string) bool {
	for _, markers := range [][]string{posterFileMarkers, backdropMarkers} {
		for _, marker := range markers {
			if artStemMatch(stem, videoStem, marker) {
				return true
			}
		}
	}
	return false
}

// addWatchRecursive 把目录及其全部子目录加入监听。
func (w *Watcher) addWatchRecursive(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过无权限的条目
		}
		if d.IsDir() {
			if err := w.watcher.Add(path); err != nil {
				log.Printf("添加监听失败 %s: %v", path, err)
			}
		}
		return nil
	})
}

// Stop 停止监听并等待后台 goroutine 退出。
func (w *Watcher) Stop() {
	close(w.stop)
	<-w.done
}
