package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	// 无条件登记缓存目录：手动元信息编辑只依赖该可写目录，与是否配置 TMDB_API_KEY 解耦；
	// 在线刮削器创建时会幂等重复登记同值。
	library.SetScraperImageDir(cfg.TMDBImageDir)

	// TMDB 在线刮削（可选）：必须在初始扫描前创建，扫描时才能从缓存目录恢复历史刮削结果。
	// 未配置 API Key 时整个功能关闭，不影响本地 NFO/海报。
	var scraper *media.Scraper
	if tmdbClient := media.NewTMDBClient(cfg.TMDBAPIKey, cfg.TMDBLang, cfg.TMDBBaseURL, cfg.TMDBImageURL); tmdbClient != nil {
		sc, err := media.NewScraper(tmdbClient, library, cfg.TMDBImageDir)
		if err != nil {
			log.Printf("警告: 初始化 TMDB 刮削器失败: %v，在线刮削不可用", err)
		} else {
			scraper = sc
			log.Printf("TMDB 在线刮削已开启（语言 %s，图片缓存 %s）", cfg.TMDBLang, cfg.TMDBImageDir)
		}
	} else {
		log.Println("未设置 TMDB_API_KEY，在线刮削关闭（本地 NFO/海报不受影响）")
	}

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
		tc, err := media.NewTranscoder(cfg.TranscodeDir, cfg.FFmpeg, cfg.FFProbe, cfg.TranscodeMax, cfg.TranscodeIdleTimeout)
		if err != nil {
			log.Printf("警告: 初始化转码目录失败: %v，HLS 转码不可用", err)
		} else if !tc.Available() {
			log.Printf("警告: ffmpeg 不可用，HLS 在线转码功能关闭")
			tc = nil
		} else {
			transcoder = tc
			// 硬件加速（可选）：默认空值走 CPU 软转；设备不可用或模式未知时内部自动回退
			transcoder.SetHWAccel(cfg.TranscodeHWAccel, cfg.TranscodeVAAPIDevice)
			// 自适应（ABR）变体档位：逗号分隔，白名单过滤，空/非法回退默认三档
			abrQualities := media.ParseABRQualities(cfg.TranscodeABRQualitiesRaw)
			transcoder.SetABRQualities(abrQualities)
			log.Printf("HLS 在线转码已开启（编码档位: %s，并发上限 %d，空闲超时 %s，ABR 变体: %s，缓存目录 %s）",
				transcoder.HWAccelMode(), cfg.TranscodeMax, cfg.TranscodeIdleTimeout,
				strings.Join(abrQualities, ","), cfg.TranscodeDir)
		}
		// 位图字幕 OCR（可选）：tesseract 缺失或显式关闭时降级，位图字幕仅识别不提取
		if transcoder != nil && cfg.OCREnabled {
			ocr := media.NewOCRer(cfg.TesseractBin, cfg.FFmpeg)
			if ocr.Available() {
				transcoder.SetOCRer(ocr)
				log.Println("位图字幕 OCR 已开启（tesseract 可用）")
			} else {
				log.Printf("警告: 未找到 tesseract(%s) 或语言包，位图字幕（PGS/DVD 等）将仅识别不提取",
					cfg.TesseractBin)
			}
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

	// 过期分享后台清理：惰性删除之外的定时兜底；间隔为 0 时关闭
	var shareTicker *time.Ticker
	var shareTickerDone chan struct{}
	if db != nil && cfg.ShareCleanupInterval > 0 {
		shareTicker = time.NewTicker(cfg.ShareCleanupInterval)
		shareTickerDone = make(chan struct{})
		go func() {
			for {
				select {
				case <-shareTicker.C:
					if n, err := db.DeleteExpiredShares(time.Now().UTC()); err != nil {
						log.Printf("清理过期分享失败: %v", err)
					} else if n > 0 {
						log.Printf("已清理 %d 条过期分享", n)
					}
				case <-shareTickerDone:
					return
				}
			}
		}()
		log.Printf("过期分享定时清理已开启，间隔 %s", cfg.ShareCleanupInterval)
	}

	// 认证器：访问密码或管理员引导任一配置即启用。
	// 单密码模式下 AUTH_PASSWORD 即登录口令；多用户模式（users 表有账号）下它不再作为登录入口，
	// 仅复用其 HMAC 密钥/TTL 签发与校验用户身份令牌，故未显式配置时用占位口令兜底构造认证器。
	var authenticator *auth.Authenticator
	authPassword := cfg.AuthPassword
	if authPassword == "" && db != nil && cfg.AdminUsername != "" && cfg.AdminPassword != "" {
		authPassword = "bootstrap-disabled-single-password"
	}
	if authPassword != "" {
		authenticator, err = auth.New(authPassword, cfg.AuthSecret, cfg.AuthTTL)
		if err != nil {
			log.Fatalf("初始化认证失败: %v", err)
		}
	}

	// 首个管理员引导：仅在 DB 可用、users 表为空、ADMIN_USERNAME/PASSWORD 齐全时创建。
	// 已存在任意用户（含历史引导）一律忽略，不覆盖、不改密。
	if db != nil && cfg.AdminUsername != "" && cfg.AdminPassword != "" {
		if n, err := db.CountUsers(); err != nil {
			log.Printf("警告: 查询用户数失败，跳过管理员引导: %v", err)
		} else if n == 0 {
			if _, err := db.CreateUser(cfg.AdminUsername, "管理员", cfg.AdminPassword, store.RoleAdmin); err != nil {
				log.Printf("警告: 引导首个管理员失败: %v", err)
			} else {
				log.Printf("已引导首个管理员账号: %s（多用户模式启用，登录请使用用户名+密码）", cfg.AdminUsername)
			}
		}
	}

	srv, err := server.New(library, thumbs, transcoder, db, authenticator, scraper, cfg.AuthCookieSecure)
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
	if shareTicker != nil {
		shareTicker.Stop()
		close(shareTickerDone)
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
