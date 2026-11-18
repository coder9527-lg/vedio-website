package config

import (
	"log"
	"os"
	"strconv"
	"time"
)

// Config 存放服务运行配置，全部通过环境变量注入，方便容器化部署。
type Config struct {
	Port     string // HTTP 监听端口
	MediaDir string // 视频媒体目录（NAS 挂载点）
	ThumbDir string // 缩略图缓存目录
	FFmpeg   string // ffmpeg 可执行文件路径
	FFProbe  string // ffprobe 可执行文件路径（多音轨探测）
	DBPath   string // SQLite 数据库路径（播放进度/收藏），留空则禁用持久化

	// HLS 在线转码：把 MKV/HEVC 等浏览器不支持的格式按需转成 H.264/AAC
	TranscodeEnabled     bool
	TranscodeDir         string        // HLS 分片缓存目录
	TranscodeMax         int           // 同时运行的 ffmpeg 转码任务上限
	TranscodeIdleTimeout time.Duration // 无客户端后多久停止转码进程（分片保留）

	WatchEnabled   bool          // 是否启用文件监听自动扫描
	WatchDebounce  time.Duration // 文件事件防抖窗口
	RescanInterval time.Duration // 定时全量兜底扫描间隔，0 表示关闭

	// TMDB 在线刮削：APIKey 留空则禁用，仅保留本地 NFO/海报
	TMDBAPIKey   string // v3 API Key（Bearer token 不支持，只用 api_key 方式）
	TMDBLang     string // 元信息语言，如 zh-CN / en-US
	TMDBBaseURL  string // API 接入点，留空用官方地址（测试/内网代理可覆写）
	TMDBImageURL string // 图片源前缀，留空用官方 image.tmdb.org
	TMDBImageDir string // 刮削下载的海报/背景图缓存目录

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
		FFProbe:  getenv("FFPROBE_BIN", "ffprobe"),
		DBPath:   os.Getenv("DB_PATH"),

		TranscodeEnabled:     getenv("TRANSCODE_ENABLED", "true") != "false",
		TranscodeDir:         getenv("TRANSCODE_DIR", "/data/transcode"),
		TranscodeMax:         parseInt("TRANSCODE_MAX", 2),
		TranscodeIdleTimeout: parseDuration("TRANSCODE_IDLE_TIMEOUT", 30*time.Minute),

		WatchEnabled:   getenv("WATCH_ENABLED", "true") != "false",
		WatchDebounce:  parseDuration("WATCH_DEBOUNCE", 2*time.Second),
		RescanInterval: parseDuration("RESCAN_INTERVAL", 0),

		TMDBAPIKey:   os.Getenv("TMDB_API_KEY"),
		TMDBLang:     getenv("TMDB_LANG", "zh-CN"),
		TMDBBaseURL:  os.Getenv("TMDB_BASE_URL"),
		TMDBImageURL: os.Getenv("TMDB_IMAGE_BASE_URL"),
		TMDBImageDir: getenv("TMDB_IMAGE_DIR", "/data/tmdb"),

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

func parseInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		log.Printf("警告: %s=%q 不是合法正整数，使用默认值 %d", key, raw, fallback)
		return fallback
	}
	return n
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
