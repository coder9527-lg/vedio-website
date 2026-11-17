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

# ffmpeg 用于缩略图，ca-certificates 用于 HTTPS，tzdata 用于正确时区
RUN apk add --no-cache ffmpeg ca-certificates tzdata su-exec

ENV TZ=Asia/Shanghai \
    PORT=8080 \
    MEDIA_DIR=/media \
    THUMB_DIR=/data/thumbnails \
    DB_PATH=/data/vedio.db

WORKDIR /app
COPY --from=builder /out/vedio-server /app/vedio-server
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh && \
    mkdir -p /media /data/thumbnails

EXPOSE 8080
VOLUME ["/media", "/data"]

ENTRYPOINT ["/entrypoint.sh"]
