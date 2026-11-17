package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/auth"
	"github.com/coder9527-lg/vedio-website/internal/config"
	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/server"
	"github.com/coder9527-lg/vedio-website/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg := config.Load()

	library := media.NewLibrary(cfg.MediaDir, media.DefaultExts)
	if n, err := library.Scan(); err != nil {
		log.Printf("初始扫描媒体目录失败: %v", err)
	} else {
		log.Printf("初始扫描完成，索引视频 %d 个（目录: %s）", n, cfg.MediaDir)
	}

	thumbs, err := media.NewThumbnailer(cfg.ThumbDir, cfg.FFmpeg)
	if err != nil {
		log.Fatalf("初始化缩略图目录失败: %v", err)
	}
	if !thumbs.Available() {
		log.Printf("警告: 未找到 ffmpeg(%s)，缩略图将使用占位图", cfg.FFmpeg)
	}

	// HLS 在线转码（可选）：ffmpeg 缺失或显式关闭时降级，不影响其他功能
	var transcoder *media.Transcoder
	if cfg.TranscodeEnabled {
		tc, err := media.NewTranscoder(cfg.TranscodeDir, cfg.FFmpeg, cfg.TranscodeMax, cfg.TranscodeIdleTimeout)
		if err != nil {
			log.Printf("警告: 初始化转码目录失败: %v，HLS 转码不可用", err)
		} else if !tc.Available() {
			log.Printf("警告: ffmpeg 不可用，HLS 在线转码功能关闭")
			tc = nil
		} else {
			transcoder = tc
			log.Printf("HLS 在线转码已开启（并发上限 %d，空闲超时 %s，缓存目录 %s）",
				cfg.TranscodeMax, cfg.TranscodeIdleTimeout, cfg.TranscodeDir)
		}
	} else {
		log.Println("HLS 在线转码已关闭（TRANSCODE_ENABLED=false）")
	}

	// 文件监听：媒体目录变更后自动增量更新索引
	var watcher *media.Watcher
	if cfg.WatchEnabled {
		watcher = media.NewWatcher(library, cfg.MediaDir, cfg.WatchDebounce)
		go watcher.Run()
	} else {
		log.Println("文件监听已关闭（WATCH_ENABLED=false）")
	}

	// 定时全量兜底：NFS/SMB 等网络盘 inotify 事件可能丢失，按需开启
	var ticker *time.Ticker
	var tickerDone chan struct{}
	if cfg.RescanInterval > 0 {
		ticker = time.NewTicker(cfg.RescanInterval)
		tickerDone = make(chan struct{})
		go func() {
			for {
				select {
				case <-ticker.C:
					if n, err := library.Scan(); err != nil {
						log.Printf("定时扫描失败: %v", err)
					} else {
						log.Printf("定时全量扫描完成，索引视频 %d 个", n)
					}
				case <-tickerDone:
					return
				}
			}
		}()
		log.Printf("定时全量兜底扫描已开启，间隔 %s", cfg.RescanInterval)
	}

	// SQLite 持久化（播放进度/收藏）：DB_PATH 留空则禁用，打开失败时降级运行而不是崩溃
	var db *store.Store
	if cfg.DBPath != "" {
		if dir := filepath.Dir(cfg.DBPath); dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				log.Printf("警告: 创建数据库目录 %s 失败: %v，播放进度/收藏将不可用", dir, err)
			}
		}
		if db, err = store.Open(cfg.DBPath); err != nil {
			log.Printf("警告: 打开数据库失败: %v，播放进度/收藏将不可用", err)
			db = nil
		} else {
			log.Printf("SQLite 持久化已开启: %s", cfg.DBPath)
		}
	} else {
		log.Println("未设置 DB_PATH，播放进度与收藏功能关闭（设置该环境变量可开启）")
	}

	var authenticator *auth.Authenticator
	if cfg.AuthPassword != "" {
		authenticator, err = auth.New(cfg.AuthPassword, cfg.AuthSecret, cfg.AuthTTL)
		if err != nil {
			log.Fatalf("初始化认证失败: %v", err)
		}
	}

	srv, err := server.New(library, thumbs, transcoder, db, authenticator, cfg.AuthCookieSecure)
	if err != nil {
		log.Fatalf("初始化服务失败: %v", err)
	}

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("视频网站启动: http://0.0.0.0:%s", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常: %v", err)
		}
	}()

	// 优雅关闭：收到 SIGINT/SIGTERM 后给在途请求 10 秒
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("正在关闭服务...")

	if watcher != nil {
		watcher.Stop()
	}
	if ticker != nil {
		ticker.Stop()
		close(tickerDone)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("强制关闭: %v", err)
	}
	if db != nil {
		if err := db.Close(); err != nil {
			log.Printf("关闭数据库失败: %v", err)
		}
	}
	if transcoder != nil {
		transcoder.Close()
		log.Println("转码器已关闭")
	}
	log.Println("服务已退出")
}
