package config

import (
	"log"
	"os"
)

// Config 存放服务运行配置，全部通过环境变量注入，方便容器化部署。
type Config struct {
	Port     string // HTTP 监听端口
	MediaDir string // 视频媒体目录（NAS 挂载点）
	ThumbDir string // 缩略图缓存目录
	FFmpeg   string // ffmpeg 可执行文件路径
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Load 从环境变量加载配置。
func Load() *Config {
	cfg := &Config{
		Port:     getenv("PORT", "8080"),
		MediaDir: getenv("MEDIA_DIR", "/media"),
		ThumbDir: getenv("THUMB_DIR", "/data/thumbnails"),
		FFmpeg:   getenv("FFMPEG_BIN", "ffmpeg"),
	}

	if info, err := os.Stat(cfg.MediaDir); err != nil {
		log.Printf("警告: 媒体目录 %s 不可访问: %v，服务将以空库启动", cfg.MediaDir, err)
	} else if !info.IsDir() {
		log.Printf("警告: %s 不是目录，服务将以空库启动", cfg.MediaDir)
	}
	return cfg
}
