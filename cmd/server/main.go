package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coder9527-lg/vedio-website/internal/config"
	"github.com/coder9527-lg/vedio-website/internal/media"
	"github.com/coder9527-lg/vedio-website/internal/server"
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

	srv, err := server.New(library, thumbs)
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("强制关闭: %v", err)
	}
	log.Println("服务已退出")
}
