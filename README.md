# vedio-website

面向 NAS 的轻量个人视频网站：Go 后端 + 原生 Web 前端，单容器部署，零数据库依赖。

## 功能特性

- 📁 **自动扫描媒体目录**：递归索引挂载目录中的视频文件（支持 mp4/mkv/mov/avi/webm 等常见格式）
- 👀 **文件监听自动入库**：基于 fsnotify 递归监听，新增/删除/重命名即时增量更新；事件防抖避免拷入半截文件，NAS 晚挂载自动重试，另支持定时全量兜底（NFS/SMB 事件丢失场景）
- ⏯️ **在线播放 + 进度条拖拽**：基于 HTTP Range 的流式传输，浏览器原生播放器
- 🖼️ **ffmpeg 缩略图**：首次访问按需抽帧并缓存，不阻塞扫描；无 ffmpeg 时自动降级占位图
- 📺 **暗色响应式界面**：桌面/手机自适应，视频卡片墙
- 🔌 **JSON API**：便于后续接入客户端或自动化
- 🔒 **可选访问密码**：设置 `AUTH_PASSWORD` 即开启登录页，HMAC 签名 HttpOnly Cookie 无状态鉴权（无需数据库），含失败限流与开放重定向防护
- 💾 **SQLite 持久化（可选）**：设置 `DB_PATH` 即开启播放进度记忆（自动上报、断点续播）与收藏，纯 Go 驱动无需 CGO；不配置则自动降级为纯内存模式
- 🐳 **Docker 一键部署**：多阶段构建，小镜像；支持 `PUID/PGID` 适配 NAS 挂载卷权限
- 🛡️ 优雅关闭、请求日志、panic 恢复

## 目录结构

```
vedio-website/
├── cmd/server/            # 程序入口
├── internal/
│   ├── auth/              # 访问密码：HMAC 签名 Cookie、登录限流
│   ├── config/            # 环境变量配置
│   ├── media/             # 媒体库扫描、查询排序、ffmpeg 缩略图
│   ├── server/            # HTTP 路由、API、页面模板与静态资源（embed 内嵌）
│   └── store/             # SQLite 持久化：播放进度、收藏（modernc 纯 Go 驱动）
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
| `DB_PATH` | 空（关闭） | SQLite 数据库文件路径（如 `/data/vedio.db`），开启播放进度与收藏；留空禁用持久化，接口返回 503 且页面隐藏相关入口 |
| `PUID` / `PGID` | `0` / `0` | 容器内运行用户 UID/GID（NAS 权限适配） |
| `TZ` | `Asia/Shanghai` | 时区 |
| `WATCH_ENABLED` | `true` | 是否启用 fsnotify 文件监听自动增量扫描，设为 `false` 关闭 |
| `WATCH_DEBOUNCE` | `2s` | 文件事件防抖窗口；静默超过该时长才入库，防止拷入未完成的文件 |
| `RESCAN_INTERVAL` | 空（关闭） | 定时全量兜底扫描间隔（如 `10m`、`1h`），NFS/SMB 等网络盘 inotify 事件可能丢失时建议开启；留空或 `0` 关闭 |
| `AUTH_PASSWORD` | 空（公开） | 访问密码；非空即开启登录认证，留空则全站公开 |
| `AUTH_SECRET` | 空（随机） | 登录 Cookie 的 HMAC 签名密钥；设置固定值可避免容器重启后全员重新登录 |
| `AUTH_TTL` | `720h`（30 天） | 登录态有效期，如 `24h`、`7d` |
| `AUTH_COOKIE_SECURE` | `false` | 反代启用 HTTPS 时设为 `true`，Cookie 仅经 HTTPS 传输 |

## HTTP API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/` | 视频库首页 |
| GET | `/watch/{id}` | 视频播放页 |
| GET | `/api/v1/videos` | 视频列表 JSON（支持 `q` 关键字、`sort` 排序、`view=favorites/continue` 视图） |
| GET | `/api/v1/videos/{id}` | 单个视频元信息 |
| POST | `/api/v1/rescan` | 重新扫描媒体目录（自动清理已删除视频的孤儿进度/收藏） |
| GET | `/stream/{id}` | 视频流（支持 Range；加 `?download=1` 触发下载） |
| GET | `/thumb/{id}` | 缩略图（自动生成并缓存） |
| GET / PUT / DELETE | `/api/v1/videos/{id}/progress` | 查询 / 上报 / 清除播放进度（需 `DB_PATH`） |
| GET | `/api/v1/progress` | 全部播放进度（需 `DB_PATH`） |
| GET / POST | `/api/v1/videos/{id}/favorite` | 查询收藏状态 / 收藏·取消·切换（POST 空体即 toggle，需 `DB_PATH`） |
| GET | `/api/v1/favorites` | 全部收藏 ID（需 `DB_PATH`） |
| GET / POST | `/login` | 登录页 / 提交密码（开启认证后） |
| POST | `/logout` | 退出登录并清除会话 |

> 开启 `AUTH_PASSWORD` 后，除 `/login`、`/static/*`、占位图外的接口均需登录：页面请求 302 跳转登录页，`/api/*` 返回 `401`。

## 访问密码（可选）

默认不设密码、全站公开。给容器加一个环境变量即可开启：

```yaml
environment:
  AUTH_PASSWORD: "你的密码"
  # AUTH_SECRET: "一段随机长字符串"   # 建议设置，固定签名密钥，重启不掉登录
  # AUTH_TTL: "7d"                    # 登录有效期
  # AUTH_COOKIE_SECURE: "true"        # 经 HTTPS 反代访问时开启
```

- 认证基于 HMAC-SHA256 签名的无状态 Cookie，服务端不存储会话，仍保持零数据库依赖；
- 静态资源与登录页放行，其余页面未登录跳转登录页，API 返回 `401 JSON`；
- 同一 IP 连续登录失败 10 次后锁定 10 分钟；回跳地址仅允许站内路径，防止开放重定向；
- 未设置 `AUTH_SECRET` 时每次启动随机生成密钥，重启后所有登录态失效。

## 播放进度与收藏（可选）

设置 `DB_PATH` 指向一个持久化卷内的数据库文件即可开启（`docker-compose.yml` 已默认配置为 `/data/vedio.db`）：

```yaml
environment:
  DB_PATH: "/data/vedio.db"
```

- 采用 **纯 Go 驱动 `modernc.org/sqlite`**，CGO 关闭即可静态编译，交叉编译与 NAS 容器部署无负担；
- 播放页每 5 秒、暂停、切后台、关闭页面时自动上报进度，重新打开自动断点续播（看完 95% 以上视为看完，从头播放）；
- 首页提供「全部 / 继续观看 / ⭐ 收藏」视图，卡片展示播放进度条与续播提示；
- 数据库以 WAL 模式运行，重扫后自动清理指向已删除视频的孤儿记录；
- 不配置 `DB_PATH` 时功能整体降级：相关 API 返回 `503`，页面自动隐藏收藏按钮与视图 tab，其余功能不受影响。

## 设计说明

- **存储可选、默认无数据库**：视频索引保存在内存中，启动时全量扫描，运行期由 fsnotify 监听增量维护（可选定时全量兜底）；ID 由相对路径的 SHA-1 派生，重扫后稳定不变。只有显式设置 `DB_PATH` 才启用 SQLite，用于播放进度与收藏。
- **静态资源内嵌**：模板与 CSS 通过 `go:embed` 编译进二进制，镜像里只有一个可执行文件 + ffmpeg。
- **浏览器兼容**：MP4/WebM 可直接播放；MKV 等容器取决于浏览器解码能力，不支持时可下载原文件或在后续版本接入服务端转码（HLS）。

## 路线图（Roadmap）

- [x] 登录认证 / 简单访问密码（HMAC Cookie、失败限流，见 `AUTH_PASSWORD`）
- [x] SQLite 持久化：播放进度（自动上报 + 断点续播）、收藏、搜索/排序/视图（见 `DB_PATH`）
- [x] 文件监听（fsnotify）自动增量扫描（含防抖、晚挂载重试、定时全量兜底）
- [ ] ffmpeg HLS 在线转码，解决 MKV/小众编码播放问题
- [ ] 字幕自动挂载与多音轨支持
- [ ] 海报/元信息刮削（NFO、TMDB）
