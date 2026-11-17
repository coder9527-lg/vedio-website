package config

import (
	"log"
	"os"
	"time"
)

// Config 存放服务运行配置，全部通过环境变量注入，方便容器化部署。
type Config struct {
	Port     string // HTTP 监听端口
	MediaDir string // 视频媒体目录（NAS 挂载点）
	ThumbDir string // 缩略图缓存目录
	FFmpeg   string // ffmpeg 可执行文件路径
	DBPath   string // SQLite 数据库路径（播放进度/收藏），留空则禁用持久化

	WatchEnabled   bool          // 是否启用文件监听自动扫描
	WatchDebounce  time.Duration // 文件事件防抖窗口
	RescanInterval time.Duration // 定时全量兜底扫描间隔，0 表示关闭

	// 简单访问密码认证：AuthPassword 非空即开启，留空则全站公开
	AuthPassword     string
	AuthSecret       string        // Cookie 签名密钥，留空时每次启动随机生成（重启后全员需重新登录）
	AuthTTL          time.Duration // 登录态有效期
	AuthCookieSecure bool          // Cookie 是否仅经 HTTPS 传输（反代 HTTPS 时建议开启）
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
		DBPath:   os.Getenv("DB_PATH"),

		WatchEnabled:   getenv("WATCH_ENABLED", "true") != "false",
		WatchDebounce:  parseDuration("WATCH_DEBOUNCE", 2*time.Second),
		RescanInterval: parseDuration("RESCAN_INTERVAL", 0),

		AuthPassword:     os.Getenv("AUTH_PASSWORD"),
		AuthSecret:       os.Getenv("AUTH_SECRET"),
		AuthTTL:          parseDuration("AUTH_TTL", 30*24*time.Hour),
		AuthCookieSecure: os.Getenv("AUTH_COOKIE_SECURE") == "true",
	}

	if cfg.AuthPassword != "" {
		log.Printf("访问密码认证已开启，登录态有效期 %s", cfg.AuthTTL)
	} else {
		log.Println("未设置 AUTH_PASSWORD，站点公开访问（设置该环境变量可开启访问密码）")
	}

	if info, err := os.Stat(cfg.MediaDir); err != nil {
		log.Printf("警告: 媒体目录 %s 不可访问: %v，文件监听将等待目录出现", cfg.MediaDir, err)
	} else if !info.IsDir() {
		log.Printf("警告: %s 不是目录，服务将以空库启动", cfg.MediaDir)
	}
	return cfg
}

func parseDuration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("警告: %s=%q 不是合法时长，使用默认值 %s", key, raw, fallback)
		return fallback
	}
	return d
}
