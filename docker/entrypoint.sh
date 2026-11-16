#!/bin/sh
# 容器入口：支持 PUID/PGID（与 linuxserver.io 镜像习惯一致），
# 让进程以宿主机媒体文件属主的身份运行，避免 NAS 挂载卷权限问题。
set -eu

PUID="${PUID:-0}"
PGID="${PGID:-0}"

if [ "$PUID" = "0" ] && [ "$PGID" = "0" ]; then
    exec /app/vedio-server
fi

# 创建/复用指定 GID 的组
if getent group app >/dev/null 2>&1; then
    delgroup app >/dev/null 2>&1 || true
fi
addgroup -g "$PGID" app 2>/dev/null || true

# 创建/复用指定 UID 的用户
if id app >/dev/null 2>&1; then
    deluser app >/dev/null 2>&1 || true
fi
adduser -D -H -u "$PUID" -G "$(getent group "$PGID" | cut -d: -f1)" app 2>/dev/null || true

# 让运行用户拥有缩略图缓存目录（媒体目录通常是只读挂载，不改其属主）
mkdir -p "$THUMB_DIR"
chown -R "$PUID:$PGID" "$THUMB_DIR" 2>/dev/null || true

exec su-exec app /app/vedio-server
