# vedio-website

面向 NAS 的轻量个人视频网站：Go 后端 + 原生 Web 前端，单容器部署，零数据库依赖。

## 功能特性

- 📁 **自动扫描媒体目录**：递归索引挂载目录中的视频文件（支持 mp4/mkv/mov/avi/webm 等常见格式）
- 👀 **文件监听自动入库**：基于 fsnotify 递归监听，新增/删除/重命名即时增量更新；事件防抖避免拷入半截文件，NAS 晚挂载自动重试，另支持定时全量兜底（NFS/SMB 事件丢失场景）
- ⏯️ **在线播放 + 进度条拖拽**：基于 HTTP Range 的流式传输，浏览器原生播放器
- 🖼️ **ffmpeg 缩略图**：首次访问按需抽帧并缓存，不阻塞扫描；无 ffmpeg 时自动降级占位图
- 📺 **暗色响应式界面**：桌面/手机自适应，视频卡片墙
- 🔌 **JSON API**：便于后续接入客户端或自动化
- 🐳 **Docker 一键部署**：多阶段构建，小镜像；支持 `PUID/PGID` 适配 NAS 挂载卷权限
- 🛡️ 优雅关闭、请求日志、panic 恢复

## 目录结构

```
vedio-website/
├── cmd/server/            # 程序入口
├── internal/
│   ├── config/            # 环境变量配置
│   ├── media/             # 媒体库扫描、ffmpeg 缩略图
│   └── server/            # HTTP 路由、API、页面模板与静态资源（embed 内嵌）
├── docker/
│   └── entrypoint.sh      # PUID/PGID 权限处理
├── Dockerfile
├── docker-compose.yml
└── go.mod
```

## 快速开始

### Docker Compose（推荐 NAS 使用）

1. 修改 `docker-compose.yml` 中的卷挂载，把左侧路径换成你 NAS 上的视频目录：

   ```yaml
   volumes:
     - /volume1/videos:/media:ro   # 只读挂载更安全
     - ./data:/data                # 缩略图缓存
   ```

2. 把 `PUID`/`PGID` 改成媒体文件属主的 UID/GID（SSH 到 NAS 执行 `id 你的用户名` 查看）。

3. 启动：

   ```bash
   docker compose up -d --build
   ```

4. 浏览器打开 `http://<NAS-IP>:8080`。

### 本地直接运行

```bash
MEDIA_DIR=/path/to/videos THUMB_DIR=./data/thumbs go run ./cmd/server
```

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORT` | `8080` | HTTP 监听端口 |
| `MEDIA_DIR` | `/media` | 视频媒体目录 |
| `THUMB_DIR` | `/data/thumbnails` | 缩略图缓存目录 |
| `FFMPEG_BIN` | `ffmpeg` | ffmpeg 可执行文件路径 |
| `PUID` / `PGID` | `0` / `0` | 容器内运行用户 UID/GID（NAS 权限适配） |
| `TZ` | `Asia/Shanghai` | 时区 |
| `WATCH_ENABLED` | `true` | 是否启用 fsnotify 文件监听自动增量扫描，设为 `false` 关闭 |
| `WATCH_DEBOUNCE` | `2s` | 文件事件防抖窗口；静默超过该时长才入库，防止拷入未完成的文件 |
| `RESCAN_INTERVAL` | 空（关闭） | 定时全量兜底扫描间隔（如 `10m`、`1h`），NFS/SMB 等网络盘 inotify 事件可能丢失时建议开启；留空或 `0` 关闭 |

## HTTP API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/` | 视频库首页 |
| GET | `/watch/{id}` | 视频播放页 |
| GET | `/api/v1/videos` | 视频列表 JSON |
| GET | `/api/v1/videos/{id}` | 单个视频元信息 |
| POST | `/api/v1/rescan` | 重新扫描媒体目录 |
| GET | `/stream/{id}` | 视频流（支持 Range；加 `?download=1` 触发下载） |
| GET | `/thumb/{id}` | 缩略图（自动生成并缓存） |

## 设计说明

- **无数据库**：视频索引保存在内存中，启动时全量扫描，运行期由 fsnotify 监听增量维护（可选定时全量兜底）；ID 由相对路径的 SHA-1 派生，重扫后稳定不变。后续如需收藏、播放记录，可引入 SQLite。
- **静态资源内嵌**：模板与 CSS 通过 `go:embed` 编译进二进制，镜像里只有一个可执行文件 + ffmpeg。
- **浏览器兼容**：MP4/WebM 可直接播放；MKV 等容器取决于浏览器解码能力，不支持时可下载原文件或在后续版本接入服务端转码（HLS）。

## 路线图（Roadmap）

- [ ] 登录认证 / 简单访问密码
- [ ] SQLite 持久化：播放记录、收藏、搜索
- [x] 文件监听（fsnotify）自动增量扫描（含防抖、晚挂载重试、定时全量兜底）
- [ ] ffmpeg HLS 在线转码，解决 MKV/小众编码播放问题
- [ ] 字幕自动挂载与多音轨支持
- [ ] 海报/元信息刮削（NFO、TMDB）
