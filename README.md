# vedio-website

面向 NAS 的轻量个人视频网站：Go 后端 + 原生 Web 前端，单容器部署，零数据库依赖。

## 功能特性

- 📁 **自动扫描媒体目录**：递归索引挂载目录中的视频文件（支持 mp4/mkv/mov/avi/webm 等常见格式）
- 👀 **文件监听自动入库**：基于 fsnotify 递归监听，新增/删除/重命名即时增量更新；事件防抖避免拷入半截文件，NAS 晚挂载自动重试，另支持定时全量兜底（NFS/SMB 事件丢失场景）
- ⏯️ **在线播放 + 进度条拖拽**：基于 HTTP Range 的流式传输，浏览器原生播放器
- 🖼️ **ffmpeg 缩略图**：首次访问按需抽帧并缓存，不阻塞扫描；无 ffmpeg 时自动降级占位图
- 🔄 **HLS 在线转码（按需）**：MKV/HEVC/FLV 等浏览器无法直解的视频，点击「转码播放」即用 ffmpeg 实时转 H.264/AAC 的 HLS 流，边转边播；并发上限、排队、空闲自动停止与断点重转，hls.js 内嵌无外部 CDN 依赖
- 🔊 **多音轨支持**：ffprobe 自动探测视频中的多路音轨（语言/标题），转码时产出带 `EXT-X-MEDIA` 音频分组的 master 播放列表，播放页可在音轨菜单中切换；单音轨视频保持简单布局不变
- 💬 **外挂字幕自动挂载**：同目录同名（可带语言后缀，如 `Movie.zh-CN.srt`、`Movie.en.ass`）的 SRT/WebVTT/ASS/SSA 字幕自动关联，服务端统一转 WebVTT，通过 `<track>` 在原画与转码两种模式下均可切换
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
│   ├── media/             # 媒体库扫描、查询排序、ffmpeg 缩略图、HLS 按需转码
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
| `FFPROBE_BIN` | `ffprobe` | ffprobe 可执行文件路径（多音轨探测，随 ffmpeg 一起安装） |
| `TRANSCODE_ENABLED` | `true` | 是否启用 HLS 在线转码，设为 `false` 或 ffmpeg 缺失时自动隐藏入口 |
| `TRANSCODE_DIR` | `/data/transcode` | HLS 分片（m3u8/ts）缓存目录，位于持久化卷内；启动时清空残留 |
| `TRANSCODE_MAX` | `2` | 同时运行的 ffmpeg 转码任务上限，超出的任务排队，按 NAS CPU 性能调整 |
| `TRANSCODE_IDLE_TIMEOUT` | `30m` | 会话无人访问后多久停止转码进程（已产出分片保留，再次观看自动重转） |
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
| GET | `/api/v1/videos/{id}/transcode` | 启动/查询 HLS 转码会话状态（排队返回 429，前端轮询） |
| GET | `/hls/{id}/index.m3u8` | HLS 入口播放列表（单音轨返回 index，多音轨返回 master 内容，`no-store`） |
| GET | `/hls/{id}/{asset...}` | HLS 媒体播放列表与 mpegts 分片（变体 `out-*`，文件名白名单校验，防路径穿越） |
| GET | `/api/v1/videos/{id}/subtitles` | 视频关联的外挂字幕列表 |
| GET | `/subtitles/{id}/{sid}/track.vtt` | 单条字幕，统一转 WebVTT（SRT/ASS/SSA 服务端转换） |
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

## HLS 在线转码（可选）

浏览器通常只能直解 MP4(H.264/AAC) 与 WebM，MKV、HEVC、FLV、AVI 等格式会黑屏或只有声音。开启转码后，播放页提供「原画直链 / 转码播放」两种模式：

- **按需启动、边转边播**：首次点击转码播放才拉起 ffmpeg，输出 HLS（`event` 语义播放列表实时增长，完成后补 `EXT-X-ENDLIST`），前端用内嵌的 hls.js（Safari 走原生 HLS）轮询状态并自动接管播放；
- **并发与排队**：`TRANSCODE_MAX` 限制同时运行的转码进程数，槽位满时新任务入队，API 立即返回 `429`，前端显示「排队中」并继续轮询，不会重复入队；
- **空闲回收与重转**：播放列表/分片/状态请求都会刷新会话的最近访问时间，超过 `TRANSCODE_IDLE_TIMEOUT` 无人观看即停止 ffmpeg 释放 CPU，已产出分片保留；再次观看时清空旧目录全量重转，分片从 0 重新编号，避免新旧列表混用；
- **安全与缓存**：分片名严格白名单（`seg-<5 位数字>.ts`），杜绝路径穿越与日志读取；m3u8 禁缓存（动态内容），分片长缓存（内容不变）；
- **多音轨切换**：转码前用 ffprobe 列出全部音频流（语言标签、容器标题、default 标记）。单路音频沿用 `index.m3u8 + seg-*.ts` 简单布局；两路及以上产出 `master.m3u8` + 视频变体 `out-0.*` 与每路音频变体 `out-aN.*`（视频流只编码一份以省 CPU），master 中的 `NAME` 由服务端重写为友好音轨名（ffmpeg 6.x 默认写 `audio_N`）。前端 hls.js 走 `audioTrack` 切换，Safari 原生 HLS 走 `audioTracks`
- **降级策略**：未安装 ffmpeg 或 `TRANSCODE_ENABLED=false` 时播放页自动隐藏转码入口，原画直链、下载等其他功能不受影响；ffprobe 缺失或探测失败时自动退回单音轨转码；转码失败可随时切回原画或下载原文件。

## 外挂字幕

把字幕文件与视频放在同一目录并保持同名（可带语言/`default` 后缀），扫描与文件监听会自动关联，无需手动导入：

```
Movies/Movie.mkv
Movies/Movie.srt            # 无名后缀，展示为「字幕」
Movies/Movie.zh-CN.ass      # 简体中文（默认开启）
Movies/Movie.en.srt         # English
Movies/Movie.default.vtt    # default 后缀同样视为默认字幕
```

- 支持 `.srt` / `.vtt` / `.ass` / `.ssa` 文本字幕；服务端把 SRT 时间戳（逗号→点号、去序号）与 ASS/SSA 的 `Dialogue` 行（剥离样式覆盖块、`\N` 换行）统一转成浏览器可靠支持的 **WebVTT**，原生 `.vtt` 直接透传；
- 语言标签覆盖中英日韩法德西俄意葡泰越阿印等常见代码（含 `zh-CN/zh-Hant/chs/cht` 等别名），未识别的后缀原样展示、不丢弃；
- 字幕经标准 `<track>` 挂载，**原画直链与 HLS 转码两种播放模式通用**，播放页「字幕」菜单可开关；中文/`default` 字幕默认开启；
- 文件监听覆盖字幕增删：拷贝进来或删除字幕文件后（防抖窗口结束）自动刷新关联，无需重扫；
- 位图字幕（PGS/DVD-VOBSUB 等）无法转 WebVTT，内嵌字幕轨在转码时仍按 `-sn` 丢弃（见路线图）。

> 转码是 CPU 密集型任务，低配 NAS 建议把 `TRANSCODE_MAX` 设为 `1`，并优先在局域网内使用。

## 设计说明

- **存储可选、默认无数据库**：视频索引保存在内存中，启动时全量扫描，运行期由 fsnotify 监听增量维护（可选定时全量兜底）；ID 由相对路径的 SHA-1 派生，重扫后稳定不变。只有显式设置 `DB_PATH` 才启用 SQLite，用于播放进度与收藏。
- **静态资源内嵌**：模板与 CSS 通过 `go:embed` 编译进二进制，镜像里只有一个可执行文件 + ffmpeg。
- **浏览器兼容**：MP4/WebM 可直接播放；MKV 等容器取决于浏览器解码能力，不支持时可下载原文件或在后续版本接入服务端转码（HLS）。

## 路线图（Roadmap）

- [x] 登录认证 / 简单访问密码（HMAC Cookie、失败限流，见 `AUTH_PASSWORD`）
- [x] SQLite 持久化：播放进度（自动上报 + 断点续播）、收藏、搜索/排序/视图（见 `DB_PATH`）
- [x] 文件监听（fsnotify）自动增量扫描（含防抖、晚挂载重试、定时全量兜底）
- [x] ffmpeg HLS 在线转码（按需转 H.264/AAC、并发排队、空闲回收，见 `TRANSCODE_*`）
- [x] 外挂字幕自动挂载（SRT/VTT/ASS/SSA → WebVTT，原画/转码通用，见「外挂字幕」）
- [x] 多音轨支持（ffprobe 探测 + HLS 音频分组，播放页切换）
- [ ] 内嵌字幕轨转 HLS WebVTT（当前转码 `-sn` 丢弃内嵌轨，位图字幕受 ffmpeg mpegts 限制）
- [ ] 海报/元信息刮削（NFO、TMDB）
