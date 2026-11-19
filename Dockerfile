# ---- 构建阶段 ----
FROM golang:1.27-alpine AS builder

WORKDIR /build

# 先拉依赖（当前仅标准库，保留该层便于后续扩展）
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
# 静态编译：CGO 关闭，产出可在 scratch/alpine 直接运行的单一二进制
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/vedio-server ./cmd/server

# ---- 运行阶段 ----
FROM alpine:3.21

# ffmpeg 用于缩略图与 HLS 转码，ca-certificates 用于 HTTPS，tzdata 用于正确时区，su-exec 用于降权运行
# mesa-va-gallium/libva/intel-media-driver 为 Intel 核显 VAAPI 硬解硬编驱动；
# 默认仍走 CPU 软转，仅在设置 TRANSCODE_HWACCEL=vaapi 并映射 /dev/dri 后启用
RUN apk add --no-cache ffmpeg ca-certificates tzdata su-exec \
    mesa-va-gallium libva intel-media-driver

ENV TZ=Asia/Shanghai \
    PORT=8080 \
    MEDIA_DIR=/media \
    THUMB_DIR=/data/thumbnails \
    TRANSCODE_DIR=/data/transcode \
    DB_PATH=/data/vedio.db

WORKDIR /app
COPY --from=builder /out/vedio-server /app/vedio-server
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh && \
    mkdir -p /media /data/thumbnails

EXPOSE 8080
VOLUME ["/media", "/data"]

ENTRYPOINT ["/entrypoint.sh"]

# 容器健康探针：alpine 自带 busybox wget，无需额外安装
HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD wget -q -O - "http://127.0.0.1:${PORT:-8080}/healthz" || exit 1
