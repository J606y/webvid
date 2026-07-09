# ---- 1/3 前端构建 ----
FROM node:22-alpine AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
# vite 输出到 ../public/dist（见 frontend/vite.config.js）
RUN npm run build

# ---- 2/3 后端构建（modernc sqlite 纯 Go，CGO=0）----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 前端产物以 web 阶段为准（.dockerignore 已排除本机旧 dist）
COPY --from=web /src/public/dist ./public/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /webvid .

# ---- 3/3 运行时（ffmpeg 供缩略图与转码播放）----
FROM alpine:3.22
RUN apk add --no-cache ffmpeg ca-certificates tzdata
ENV NL_PORT=5243 NL_DATA_DIR=/data NL_FILES_DIR=/files
# 非 root 运行：固定 uid/gid=1000（常见发行版第一个非系统用户的 uid，便于和宿主机对齐）。
RUN addgroup -g 1000 webvid && adduser -D -u 1000 -G webvid webvid \
    && mkdir -p /data /files && chown -R webvid:webvid /data /files
COPY --from=build /webvid /usr/local/bin/webvid
VOLUME ["/data", "/files"]
EXPOSE 5243
# 注意：docker-compose.yml 用 bind mount（./data:/data、./files:/files）而非具名卷，
# 上面的 chown 只影响镜像自带的目录内容，对 bind mount 挂载点本身不生效——容器内 uid=1000
# 能否读写，取决于宿主机 ./data、./files 目录自身的属主/权限。首次运行前请在宿主执行
# `chown -R 1000:1000 data files`（或 `chmod -R o+rwX data files` 图省事），
# 或者不改宿主权限，改用 compose 的 `user: "<宿主uid>:<宿主gid>"` 覆盖本镜像默认用户，
# 详见 docker-compose.yml 里的注释。
USER webvid
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -qO- "http://127.0.0.1:${NL_PORT:-5243}/api/ping" >/dev/null 2>&1 || exit 1
ENTRYPOINT ["webvid"]
