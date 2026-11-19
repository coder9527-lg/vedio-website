# vedio-website

面向 NAS 的轻量个人视频网站：Go 后端 + 原生 Web 前端，单容器部署，零数据库依赖。

## 功能特性

- 📁 **自动扫描媒体目录**：递归索引挂载目录中的视频文件（支持 mp4/mkv/mov/avi/webm 等常见格式）
- 👀 **文件监听自动入库**：基于 fsnotify 递归监听，新增/删除/重命名即时增量更新；事件防抖避免拷入半截文件，NAS 晚挂载自动重试，另支持定时全量兜底（NFS/SMB 事件丢失场景）
- ⏯️ **在线播放 + 进度条拖拽**：基于 HTTP Range 的流式传输，浏览器原生播放器；支持 0.5~2 倍倍速切换、键盘快捷键（空格/K 播放暂停、←→±5 秒、↑↓音量、F 全屏、M 静音）与字幕字号三档调节
- 🩺 **健康检查端点**：`/healthz` 固定返回 200（认证放行），Dockerfile 内置 `HEALTHCHECK`，群晖 Container Manager / 主流 NAS 容器管理可直接探活
- 🖼️ **ffmpeg 缩略图**：首次访问按需抽帧并缓存，不阻塞扫描；无 ffmpeg 时自动降级占位图
- 🔄 **HLS 在线转码（按需）**：MKV/HEVC/FLV 等浏览器无法直解的视频，点击「转码播放」即用 ffmpeg 实时转 H.264/AAC 的 HLS 流，边转边播；并发上限、排队、空闲自动停止与断点重转，hls.js 内嵌无外部 CDN 依赖；可选 Intel 核显 VAAPI 硬解硬编（`TRANSCODE_HWACCEL=vaapi`），设备缺失时启动期自动回退 CPU 软转
- 🔊 **多音轨支持**：ffprobe 自动探测视频中的多路音轨（语言/标题），转码时产出带 `EXT-X-MEDIA` 音频分组的 master 播放列表，播放页可在音轨菜单中切换；单音轨视频保持简单布局不变
- 💬 **外挂字幕自动挂载**：同目录或 `subs/`/`subtitles/`/`字幕/` 子目录中同名（可带语言/SDH/强制后缀，如 `Movie.zh-CN.srt`、`Movie.en.forced.ass`）的 SRT/WebVTT/ASS/SSA 字幕自动关联，服务端纯 Go 统一转 WebVTT（无需额外进程/磁盘缓存），通过 `<track>` 在原画与转码两种模式下均可切换
- 📝 **内嵌字幕提取**：转码时自动把视频内封装的文本字幕（SRT/ASS/WebVTT/MOV_TEXT）提取为 WebVTT 并挂进同一个字幕菜单（标注「[内嵌]」）；位图字幕（PGS/DVD-VOBSUB/DVB/XSUB）在安装 tesseract 后经 OCR 识别为 WebVTT，未安装时仅识别不提取、静默降级
- 🎞️ **NFO 元信息与本地海报**：自动识别 Kodi/Jellyfin 风格的 `movie.nfo` / 同名 `.nfo`（片名、年份、片长、评分、类型、导演、主演、剧情简介等），以及同目录命名约定的海报/背景图（`*-poster.jpg`、`folder.jpg`、`*.fanart.jpg` 等）；首页卡片优先展示海报与正式片名，播放页呈现完整影片资料，NFO/图片增删由文件监听自动刷新，无需重扫
- 🌐 **TMDB 在线刮削**（可选）：配置 `TMDB_API_KEY` 后在播放页一键搜索 TMDB 候选、人工确认后自动下载海报/背景图并补齐缺失元信息；本地 NFO 优先，结果持久化到缓存目录、重启自动恢复；未配置时功能静默关闭；首页另提供「批量刮削」，后台自动为全库缺失元信息的视频逐个匹配（300ms 节流、单任务、进度可轮询）
- 📺 **电视剧/剧集自动分组**：文件名含 `S01E02`、`1x02`、`第1季第2集`、`EP02` 等季集标记的视频自动聚合成剧集，首页折叠为剧集卡片（可按「全部/剧集/电影」筛选），剧集详情页按季列集并标记观看进度，播放页本集结束后 5 秒倒计时自动连播下一集（可取消）；识别不出季集标记的视频始终按电影处理，行为不变；聚合在内存索引中完成，无数据库依赖，文件增删改名即时增量更新
- 📺 **暗色响应式界面**：桌面/手机自适应，视频卡片墙
- 🔌 **JSON API**：便于后续接入客户端或自动化
- 🔒 **可选访问密码**：设置 `AUTH_PASSWORD` 即开启登录页，HMAC 签名 HttpOnly Cookie 无状态鉴权（无需数据库），含失败限流与开放重定向防护
- 💾 **SQLite 持久化（可选）**：设置 `DB_PATH` 即开启播放进度记忆（自动上报、断点续播）、收藏与播放列表/合集，纯 Go 驱动无需 CGO；不配置则自动降级为纯内存模式
- 🐳 **Docker 一键部署**：多阶段构建，小镜像；支持 `PUID/PGID` 适配 NAS 挂载卷权限
- 🛡️ 优雅关闭、请求日志、panic 恢复

## 目录结构

```
vedio-website/
├── cmd/server/            # 程序入口
├── internal/
│   ├── auth/              # 访问密码：HMAC 签名 Cookie、登录限流
│   ├── config/            # 环境变量配置
│   ├── media/             # 媒体库扫描、查询排序、季集解析与剧集聚合、ffmpeg 缩略图、HLS 按需转码
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
| `TRANSCODE_HWACCEL` | 空（软转） | 硬件加速模式，目前支持 `vaapi`（Intel 核显硬解硬编）；留空走 CPU 软转，未知模式或设备不可访问时启动期告警并回退软转 |
| `TRANSCODE_VAAPI_DEVICE` | `/dev/dri/renderD128` | VAAPI 渲染节点路径；启用硬转时还需在 compose 中映射 `/dev/dri` 并把容器用户加入 `render` 组 |
| `OCR_ENABLED` | `true` | 是否启用位图字幕 OCR；设为 `false` 关闭，或 tesseract 缺失/无语言包时自动降级（位图字幕仅识别不提取） |
| `TESSERACT_BIN` | `tesseract` | tesseract OCR 可执行文件路径；需另行安装 tesseract 及所需语言包（如 `chi_sim`、`eng`） |
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
| `TMDB_API_KEY` | 空（关闭） | TMDB v3 API Key；留空则在线刮削整体关闭（接口 503、页面隐藏入口），本地 NFO/海报不受影响 |
| `TMDB_LANG` | `zh-CN` | TMDB 元信息语言，如 `zh-CN`、`en-US` |
| `TMDB_BASE_URL` | 官方 API | API 接入点覆写，供内网代理/测试使用（默认 `https://api.themoviedb.org/3`） |
| `TMDB_IMAGE_BASE_URL` | 官方图床 | 图片源前缀覆写（默认 `https://image.tmdb.org/t/p`），国内可用镜像加速 |
| `TMDB_IMAGE_DIR` | `/data/tmdb` | 在线刮削下载的海报/背景图与 `metadata.json` 缓存目录，位于持久化卷内 |

## HTTP API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/` | 视频库首页（支持 `q`、`sort`、`view`、`type=all/movie/show` 筛选） |
| GET | `/watch/{id}` | 视频播放页（剧集成员自动注入下一集连播信息） |
| GET | `/shows/{id}` | 剧集详情页（按季列出全部集） |
| GET | `/api/v1/shows` | 剧集列表 JSON |
| GET | `/api/v1/shows/{id}` | 单部剧集详情（含季/集结构，不存在返回 404） |
| GET | `/api/v1/videos` | 视频列表 JSON（支持 `q` 关键字、`sort` 排序、`view=favorites/continue` 视图） |
| GET | `/api/v1/videos/{id}` | 单个视频元信息 |
| POST | `/api/v1/rescan` | 重新扫描媒体目录（自动清理已删除视频的孤儿进度/收藏） |
| GET | `/stream/{id}` | 视频流（支持 Range；加 `?download=1` 触发下载） |
| GET | `/thumb/{id}` | 缩略图（自动生成并缓存） |
| GET | `/api/v1/videos/{id}/metadata` | 刮削元信息（NFO/TMDB 合并结果 + 海报路径，无元信息时 404） |
| GET | `/api/v1/videos/{id}/scrape` | 用清洗后的文件名搜索 TMDB 候选影片（未配置 Key 时 503） |
| POST | `/api/v1/videos/{id}/scrape` | 请求体 `{"tmdbId": 555}`，拉取详情、下载图片并与本地 NFO 合并 |
| POST | `/api/v1/scrape/bulk` | 启动全库批量刮削（仅缺失元信息的视频）：202 已受理、409 已有任务运行（回传进度）、503 刮削未启用 |
| GET | `/api/v1/scrape/bulk` | 批量刮削进度快照（总数/成功/跳过/失败/当前视频/起止时间） |
| GET | `/artwork/{id}/poster` · `/artwork/{id}/backdrop` | 本地海报 / 背景图（仅限媒体根目录内的图片，白名单扩展名） |
| GET | `/api/v1/videos/{id}/transcode` | 启动/查询 HLS 转码会话状态（排队返回 429，前端轮询） |
| GET | `/hls/{id}/index.m3u8` | HLS 入口播放列表（单音轨返回 index，多音轨返回 master 内容，`no-store`） |
| GET | `/hls/{id}/{asset...}` | HLS 媒体播放列表与 mpegts 分片（变体 `out-*`，文件名白名单校验，防路径穿越） |
| GET | `/api/v1/videos/{id}/subtitles` | 视频关联的外挂字幕列表 |
| GET | `/subtitles/{id}/{sid}/track.vtt` | 单条字幕，统一转 WebVTT（SRT/ASS/SSA 服务端转换） |
| GET / PUT / DELETE | `/api/v1/videos/{id}/progress` | 查询 / 上报 / 清除播放进度（需 `DB_PATH`） |
| GET | `/api/v1/progress` | 全部播放进度（需 `DB_PATH`） |
| GET / POST | `/api/v1/videos/{id}/favorite` | 查询收藏状态 / 收藏·取消·切换（POST 空体即 toggle，需 `DB_PATH`） |
| GET | `/api/v1/favorites` | 全部收藏 ID（需 `DB_PATH`） |
| GET / POST | `/api/v1/playlists` | 播放列表摘要列表 / 新建列表（需 `DB_PATH`） |
| GET / PATCH / DELETE | `/api/v1/playlists/{id}` | 列表详情（含有序条目，悬空视频自动跳过）/ 重命名 / 删除 |
| POST / DELETE | `/api/v1/playlists/{id}/items` · `/items/{videoId}` | 加入单条（新增 201、重复 200）/ 移除条目（幂等） |
| POST | `/api/v1/playlists/{id}/items/{videoId}/move` | 上移/下移，请求体 `{"direction":"up"\|"down"}`，返回全序 |
| POST | `/api/v1/shows/{id}/playlist` | 整剧按季集顺序一键加入，请求体 `{"playlistId":"..."}`，自动去重 |
| GET | `/api/v1/videos/{id}/playlists` | 该视频所属的播放列表（加入弹窗勾选态） |
| GET / POST | `/login` | 登录页 / 提交密码（开启认证后） |
| POST | `/logout` | 退出登录并清除会话 |
| GET | `/healthz` | 健康检查，固定返回 `{"status":"ok"}`（无需登录，供容器探活/负载均衡） |

> 开启 `AUTH_PASSWORD` 后，除 `/login`、`/healthz`、`/static/*`、占位图外的接口均需登录：页面请求 302 跳转登录页，`/api/*` 返回 `401`。`/healthz` 始终放行以便容器探活。

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
- 首页提供「全部 / 继续观看 / ⭐ 收藏 / 📂 播放列表」视图，卡片展示播放进度条与续播提示；
- 数据库以 WAL 模式运行，重扫后自动清理指向已删除视频的孤儿记录（播放列表表头保留，仅清悬空条目）；
- 不配置 `DB_PATH` 时功能整体降级：相关 API 返回 `503`，页面自动隐藏收藏、播放列表按钮与视图 tab，其余功能不受影响。

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
Movies/Movie.srt              # 无名后缀，展示为「字幕」
Movies/Movie.zh-CN.ass        # 简体中文（默认开启）
Movies/Movie.en.sdh.srt       # English（SDH），展示为「English（SDH）」
Movies/Movie.zh-CN.forced.ass # 简体中文（强制），不会被选为默认轨道
Movies/Movie.default.vtt      # default 后缀同样视为默认字幕
Movies/subs/Movie.fr.srt      # 也可放在 subs/subtitles/字幕 子目录（Jellyfin 风格）
```

命名分隔符支持 `.`、空格、`-`、`_`（如 `Movie-en.srt`、`Movie_en.srt`）；
标记后缀 `sdh/cc/hi` 识别为听障字幕，`forced/force` 识别为强制字幕，排序时普通字幕优先于强制/SDH、中文优先于英文。

- 支持 `.srt` / `.vtt` / `.ass` / `.ssa` 文本字幕；服务端把 SRT 时间戳（逗号→点号、去序号）与 ASS/SSA 的 `Dialogue` 行（剥离样式覆盖块、`\N` 换行）统一转成浏览器可靠支持的 **WebVTT**，原生 `.vtt` 直接透传；
- 语言标签覆盖中英日韩法德西俄意葡泰越阿印等常见代码（含 `zh-CN/zh-Hant/chs/cht` 等别名），未识别的后缀原样展示、不丢弃；
- 字幕经标准 `<track>` 挂载，**原画直链与 HLS 转码两种播放模式通用**，播放页「字幕」菜单可开关；中文/`default` 字幕默认开启；
- 文件监听覆盖字幕增删：拷贝进来或删除字幕文件后（防抖窗口结束）自动刷新关联，无需重扫；
- **内嵌字幕**：视频内封装的文本字幕（subrip/ASS/WebVTT/MOV_TEXT 等）在转码启动时由 ffmpeg 逐路提取为转码目录下的 `sub-N.vtt`（自动把 `MM:SS.mmm` 规整为标准 `HH:MM:SS.mmm`），与外挂字幕共用播放页「字幕」菜单，名称带「[内嵌]」前缀。
- **位图字幕 OCR（可选）**：PGS/DVD-VOBSUB/DVB/XSUB 等位图字幕只有图像、没有文本，需要额外安装 [tesseract OCR](https://github.com/tesseract-ocr/tesseract) 及语言包（简体中文 `chi_sim`、英文 `eng` 等，Debian 系为 `tesseract-ocr` 与 `tesseract-ocr-chi-sim`，macOS 为 `brew install tesseract tesseract-lang`）。启用后转码时先用 ffmpeg 把位图字幕解码为带时间戳的 PNG 帧，再由 tesseract 逐帧识别，相邻重复帧自动合并为同一条 WebVTT cue，产出同样的 `sub-N.vtt` 并入字幕菜单；语言按字幕轨标签自动选择（中文优先 `chi_sim`/`chi_tra`，缺语言包回退英文）。未安装 tesseract、缺少语言包或设 `OCR_ENABLED=false` 时静默降级为「仅识别不提取」，转码主流程仍以 `-sn` 丢弃图像流，不影响其他功能。为保持镜像精简，默认镜像不含 tesseract；容器内需要时可基于本镜像自行 `apk add --no-cache tesseract-ocr tesseract-ocr-data-chi_sim`（语言包名随 Alpine 版本略有差异）。

### VAAPI 硬件加速（可选，Intel 核显）

软转在低配 NAS 上会占满 CPU，带 Intel 核显的机型（群晖 x86 Plus 系列、自组 N100/N305 小主机等）可启用 VAAPI 硬解硬编：

```yaml
environment:
  TRANSCODE_HWACCEL: "vaapi"
  TRANSCODE_VAAPI_DEVICE: "/dev/dri/renderD128"   # 可省略，默认即此值
devices:
  - /dev/dri:/dev/dri
group_add:
  - "render"   # render 组 GID 因发行版而异，宿主机执行 getent group render 查看
```

- 镜像已内置 `mesa-va-gallium`、`libva`、`intel-media-driver` 驱动，无需额外构建；
- 启动期对设备节点做一次预检：未映射 `/dev/dri`、节点不可访问或模式未知时打印警告并自动回退 CPU 软转，不会在每个转码会话上反复失败；
- 硬转使用 `h264_vaapi`（目标码率 4000k，VAAPI 不支持 crf/preset），软转维持 `libx264 -preset veryfast -crf 23`；多音轨、字幕提取等其余链路两种档位完全一致；
- 不设置 `TRANSCODE_HWACCEL` 时行为与旧版本零差异。NVIDIA NVENC、Intel QSV 本期未实现。

> 转码是 CPU 密集型任务，低配 NAS 建议把 `TRANSCODE_MAX` 设为 `1`，并优先在局域网内使用。

## 海报与元信息（NFO）

无需任何配置，把刮削器（tinyMediaManager、Kodi、Jellyfin 导出等）生成的 NFO 与图片放在视频同目录即可自动识别：

- **NFO 文件**：优先读取与视频同名的 `<片名>.nfo`，其次目录级 `movie.nfo`；兼容新版 `<ratings><rating><value>` 与旧版纯文本 `<rating>` 两种评分写法，年份缺失时从 `<premiered>` 提取，单个 `<genre>` 内的 `/`、`,`、`|` 分隔值会自动拆开
- **海报/背景图命名约定**（jpg/jpeg/png/webp）：
  - 与视频同名：`<片名>-poster.jpg`、`<片名>.fanart.jpg`（分隔符支持 `-`/`_`/`.`/空格）
  - 目录级通用图：`poster.jpg`、`cover.jpg`、`folder.jpg`（海报）；`fanart.jpg`、`backdrop.jpg`、`background.jpg`（背景图）
  - NFO 内 `<art><poster>`/`<art><fanart>` 指向的路径同样支持（绝对路径或相对 NFO 的相对路径）
- **展示规则**：有海报时首页卡片与播放页封面使用海报，否则回退 ffmpeg 缩略图；片名、年份、评分、片长、类型展示在卡片上，播放页另有剧情简介、导演、主演、出品方与背景图横幅
- **自动刷新**：NFO/图片的新增、替换、删除由 fsnotify 防抖后增量生效，不需要手动重新扫描；`/artwork/*` 只提供媒体根目录内、扩展名白名单内的登记图片，带路径穿越防护

## 在线刮削（TMDB）

配置 `TMDB_API_KEY` 后开启，无需任何外部 SDK 或 CGO（纯标准库 `net/http`）：

- **触发方式**：播放页点击「🔍 在线刮削」，后端先把文件名清洗为片名（自动剥离 `1080p`、`BluRay`、`x264`、发布组、方括号注释等噪声，保留年份），再搜索 TMDB 并弹出候选列表（含海报、年份、评分、简介）供人工确认，避免自动匹配错误
- **合并策略**：以本地 NFO 为权威基底，TMDB 只填充空缺字段；来源分别标记为 `nfo`、`tmdb`、`nfo+tmdb`，页面展示对应中文文案
- **图片下载**：海报（w500）与背景图（w1280）原子写入 `TMDB_IMAGE_DIR/<视频ID>/`，经 `/artwork/*` 提供（与 NFO 海报同一套扩展名白名单与路径穿越防护，允许根目录为媒体根目录 + 刮削缓存目录）；图片下载失败不阻断文字元信息
- **持久化**：刮削结果写入缓存目录的 `metadata.json`，重新扫描或进程重启后自动恢复，并与当前 NFO 重新合并；同一视频的刮削请求串行化，避免并发竞态
- **优雅降级**：未配置 Key、TMDB 不可达、返回错误码时接口分别返回 503/502，页面给出提示，不影响播放与本地刮削；`TMDB_BASE_URL`、`TMDB_IMAGE_BASE_URL` 可指向内网代理或图床镜像

### 批量刮削（全库自动补齐）

单视频人工刮削适合精修，新入库大量视频时可在首页点击「批量刮削」：

- 仅处理当前**没有任何元信息**的视频（已有 NFO 或已刮削的自动跳过），任务启动时快照视频列表，运行期间的增删不影响本次任务；
- 后端内存单任务模型：同一时刻只允许一个批量任务，重复启动返回 `409` 并回传当前进度；未配置 `TMDB_API_KEY` 时返回 `503`，页面不显示入口；
- 每个视频自动取 TMDB 搜索的首个候选应用（与手动刮削同一套合并/下载/持久化逻辑），相邻请求间隔 300ms 节流，避免触发 API 速率限制；
- 单个视频失败只记录错误并继续，无候选计入「跳过」，进度弹窗每秒轮询 `GET /api/v1/scrape/bulk`，展示总数/成功/跳过/失败/当前视频，刷新页面后自动接入进行中的任务；
- 任务状态仅存内存，进程重启即清空，不会有半截任务残留；任务不会在启动时自动运行，必须手动触发。

> 本地 NFO 与在线 TMDB 复用同一套 `Metadata` 结构、API 与页面展示链路，二者可独立或组合使用。

## 电视剧/剧集分组

无需任何配置，扫描时自动按文件名识别季集并聚合，识别失败的文件一律按电影展示：

- **支持的命名**（大小写不敏感，可被点/空格/下划线/括号包裹）：
  - `S01E02`、`S1E2`、多集 `S01E02E03`
  - `1x02`
  - 中文 `第1季第2集` / `第二季第十三集`（支持中文数字）
  - 仅集号 `EP02`、`第3集`（季号兜底为第 1 季）
- **聚合规则**：同一目录下规范化剧名相同的视频归入一部剧；放在 `Season 2`、`S02`、`第二季`、`Specials/特辑/番外` 等季目录中的视频自动上提到上一级剧名目录，特辑按第 0 季展示。剧名会剥离年份、分辨率（`1080p/4K`）、来源与编码词（`BluRay/WEB-DL/x265/HEVC` 等）和方括号组名
- **首页**：默认视图下每部剧折叠为一张卡片（角标显示总集数，缩略图取首集或首个带海报的成员），顶部「全部 / 剧集 / 电影」tab 可切换；`GET /?type=show` 与 `?type=movie` 也可直接作为链接/书签
- **剧集详情页** `/shows/{id}`：按季分组列出全部集，带 `S01E02` 角标；开启 `DB_PATH` 后已看集高亮并显示观看位置
- **自动连播**：播放页本集 `ended` 后弹出 5 秒倒计时浮层，自动跳转同剧下一集（跨季顺延），可「立即播放」或「取消连播」；末集与电影不显示浮层
- **稳定性**：剧集 ID 由「聚合目录 + 规范化剧名」的 SHA-1 派生，重扫稳定；新增/删除/改名经文件监听增量重建，最后一集移除后空剧自动清除，改名迁移不会残留旧剧；全部状态在内存中维护，不新增数据库表或环境变量

## 播放列表 / 合集（可选，需 `DB_PATH`）

把跨目录的任意电影与剧集单集聚合为命名列表，手动排序后按列表上下文连播：

- **入口**：首页「📂 播放列表」tab，弹窗式管理，不新增独立页面；未配置 `DB_PATH` 时 tab 与所有入口自动隐藏。
- **创建与加入**：列表页可新建；电影卡片、播放页、剧集详情页（单集「＋」与「整剧加入」）均可唤起加入弹窗，弹窗内支持直接新建并加入，重复加入自动去重。
- **整剧一键加入**：按季/集升序把整部剧的全部集写入列表，已存在的集自动跳过并返回 `added/skipped/total` 计数。
- **手动排序**：管理弹窗内上移/下移按钮，`sort_order` 始终是无间隙整数，移动在单个 SQLite 事务内重排，首项上移、末项下移为静默 no-op。
- **列表连播**：从列表进入播放页时 URL 带 `?playlist={id}` 并在跳转中贯穿；本集结束后优先按列表顺序连播（自动跳过已删除的悬空视频），列表末项停止，不回退到剧集连播；参数非法或列表已删时静默回退原有剧集连播，旧链接不会 404。
- **数据与清理**：两张新表 `playlists`、`playlist_item`（复合主键天然去重），无外键；重扫只清理悬空条目、保留空列表表头；删除列表在事务内级联清空条目。

## 设计说明

- **存储可选、默认无数据库**：视频索引保存在内存中，启动时全量扫描，运行期由 fsnotify 监听增量维护（可选定时全量兜底）；ID 由相对路径的 SHA-1 派生，重扫后稳定不变。只有显式设置 `DB_PATH` 才启用 SQLite，用于播放进度、收藏与播放列表。
- **静态资源内嵌**：模板与 CSS 通过 `go:embed` 编译进二进制，镜像里只有一个可执行文件 + ffmpeg。
- **浏览器兼容**：MP4/WebM 可直接播放；MKV 等容器取决于浏览器解码能力，不支持时可下载原文件或在后续版本接入服务端转码（HLS）。

## 路线图（Roadmap）

- [x] 登录认证 / 简单访问密码（HMAC Cookie、失败限流，见 `AUTH_PASSWORD`）
- [x] SQLite 持久化：播放进度（自动上报 + 断点续播）、收藏、播放列表/合集（整剧加入、手动排序、列表连播）、搜索/排序/视图（见 `DB_PATH`）
- [x] 文件监听（fsnotify）自动增量扫描（含防抖、晚挂载重试、定时全量兜底）
- [x] ffmpeg HLS 在线转码（按需转 H.264/AAC、并发排队、空闲回收，见 `TRANSCODE_*`）
- [x] 外挂字幕自动挂载（SRT/VTT/ASS/SSA → WebVTT，原画/转码通用，见「外挂字幕」）
- [x] 多音轨支持（ffprobe 探测 + HLS 音频分组，播放页切换）
- [x] 内嵌文本字幕提取为 WebVTT（转码启动时 ffmpeg 导出 `sub-N.vtt` 并复用字幕菜单）
- [x] 位图字幕 OCR（PGS/DVD/DVB/XSUB 经可选的 tesseract 识别为 WebVTT，缺失时静默降级，见 `OCR_ENABLED`/`TESSERACT_BIN`）
- [x] 海报/元信息刮削：本地 NFO（Kodi/Jellyfin 兼容）+ 海报/背景图命名约定，文件监听自动刷新（见「海报与元信息（NFO）」）
- [x] 在线刮削（TMDB v3，纯标准库实现，候选人工确认，与 NFO 合并并持久化，见「在线刮削（TMDB）」）
- [x] 全库批量刮削（内存单任务、300ms 节流、进度轮询，见「批量刮削」）
- [x] VAAPI 硬件加速转码（Intel 核显，设备缺失自动回退软转，见「VAAPI 硬件加速」）
- [x] 播放器增强（倍速、键盘快捷键、字幕字号）与 `/healthz` 容器健康检查
- [x] 电视剧/剧集分组（SxxEyy/1xx/中文季集/EP 命名解析、首页折叠、详情页按季列集、播放页自动连播，见「电视剧/剧集分组」）
