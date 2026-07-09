#!/usr/bin/env bash
#
# WebVid 一键安装 / 管理脚本（Linux）—— 同时支持 systemd 与 Docker 两种部署
# 项目：https://github.com/J606y/webvid
#
# 用法：
#   bash install.sh [子命令] [参数]        # root 下运行（无 sudo 也可）
#   webvid                                 # 安装后直接输入 webvid 进入交互菜单
#
# 子命令：
#   install [systemd|docker] [版本]  安装并启动（省略后端则交互选择，省略版本装最新）
#   update  [版本]                   升级到指定/最新版本（保留 data/ 与 files/）
#   uninstall                        卸载（会询问是否一并删除数据）
#   start | stop | restart | status | log      服务生命周期管理
#   reset-password [新密码]          重新生成管理员密码（省略则随机，不动存储配置）
#   password                         打印首启时生成的初始管理员密码
#   version                          显示已安装版本与最新可用版本
#   (无参数)                         进入交互菜单
#
# 后端自动识别：安装后记录在 ${INSTALL_DIR}/.backend，后续管理命令自动分派。
#
# 常用环境变量：
#   WEBVID_BACKEND=docker|systemd    非交互安装时指定后端
#   WEBVID_DIR=/opt/webvid           安装目录
#   WEBVID_PORT=5243                 监听/映射端口
#   WEBVID_ADMIN_PASSWORD=xxx        首启管理员密码（不设则随机）
#
# 一行远程安装：
#   curl -fsSL https://raw.githubusercontent.com/J606y/webvid/main/install.sh -o install.sh \
#     && bash install.sh install
#
set -euo pipefail

# ------------------------------ 可配置项 ------------------------------
REPO="J606y/webvid"                           # GitHub 仓库
APP="webvid"                                   # 二进制 / 服务名 / 管理命令名
INSTALL_DIR="${WEBVID_DIR:-/opt/webvid}"       # 安装目录（可用环境变量覆盖）
DATA_DIR="${INSTALL_DIR}/data"                 # 数据库/缩略图/转码缓存（含机密 newlist.db）
FILES_DIR="${INSTALL_DIR}/files"               # 首启自动挂载的本地存储
PORT="${WEBVID_PORT:-5243}"                    # 监听端口（NL_PORT）/ Docker 映射端口
SERVICE_FILE="/etc/systemd/system/${APP}.service"
BIN="${INSTALL_DIR}/${APP}"                    # systemd 后端的 app 二进制
SELF_BIN="/usr/local/bin/${APP}"               # 管理命令安装位置（输入 webvid 即本脚本）

BUILD_DIR="${INSTALL_DIR}/.build"              # Docker 后端的镜像构建上下文（仅放二进制+Dockerfile）
COMPOSE_FILE="${INSTALL_DIR}/docker-compose.yml"
IMAGE="webvid:local"                           # Docker 后端本地镜像名
BACKEND_FILE="${INSTALL_DIR}/.backend"         # 记录当前后端：systemd | docker
VERSION_FILE="${INSTALL_DIR}/.version"         # 记录已安装版本

DC=""   # docker compose 调用前缀（pick_compose 填充）
DL=""   # 下载工具（pick_downloader 填充）

# ------------------------------ 颜色输出 ------------------------------
if [ -t 1 ]; then
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'
  C_BLUE=$'\033[36m'; C_BOLD=$'\033[1m'; C_RST=$'\033[0m'
else
  C_RED=; C_GREEN=; C_YELLOW=; C_BLUE=; C_BOLD=; C_RST=
fi
info()    { echo "${C_BLUE}[i]${C_RST} $*"; }
ok()      { echo "${C_GREEN}[✓]${C_RST} $*"; }
warn()    { echo "${C_YELLOW}[!]${C_RST} $*" >&2; }
err()     { echo "${C_RED}[✗]${C_RST} $*" >&2; }
die()     { err "$*"; exit 1; }

# ------------------------------ 基础检查 ------------------------------
need_root() {
  if [ "$(id -u)" -ne 0 ]; then
    die "请用 root 运行（已是 root 则无需 sudo；否则：sudo bash install.sh $*）"
  fi
}

need_systemd() {
  command -v systemctl >/dev/null 2>&1 || \
    die "未检测到 systemd（systemctl）。请改用 Docker 后端：install docker"
}

# 下载工具：优先 curl，回退 wget
pick_downloader() {
  if command -v curl >/dev/null 2>&1; then DL="curl";
  elif command -v wget >/dev/null 2>&1; then DL="wget";
  else die "需要 curl 或 wget，请先安装其一。"; fi
}
# fetch <url> <out>   下载到文件；out 为 - 时输出到 stdout
fetch() {
  local url="$1" out="$2"
  if [ "$DL" = "curl" ]; then
    if [ "$out" = "-" ]; then curl -fsSL "$url"; else curl -fsSL "$url" -o "$out"; fi
  else
    if [ "$out" = "-" ]; then wget -qO- "$url"; else wget -qO "$out" "$url"; fi
  fi
}

# ------------------------------ 架构检测 ------------------------------
detect_arch() {
  local m; m="$(uname -m)"
  case "$m" in
    x86_64|amd64)          echo "amd64" ;;
    aarch64|arm64)         echo "arm64" ;;
    armv7l|armv7|armhf)    echo "armv7" ;;
    *) die "不支持的 CPU 架构：$m（当前发布仅提供 amd64 / arm64 / armv7）" ;;
  esac
}

# ------------------------------ 后端识别 ------------------------------
# current_backend  输出 systemd|docker|空（未安装）。优先读标记文件，回退按痕迹推断。
current_backend() {
  if [ -f "$BACKEND_FILE" ]; then cat "$BACKEND_FILE"; return; fi
  if [ -f "$COMPOSE_FILE" ]; then echo "docker"; return; fi
  if [ -f "$SERVICE_FILE" ]; then echo "systemd"; return; fi
  echo ""
}

# ask_backend  交互选择后端；结果（systemd|docker）打到 stdout，提示打到 stderr。
ask_backend() {
  {
    echo
    echo "  选择部署方式："
    echo "   1) systemd  —— 下载预编译二进制，systemctl 托管（需要 systemd）"
    echo "   2) Docker   —— 构建精简镜像，docker compose 托管（需要 Docker）"
  } >&2
  local n; read -r -p "  选择 [1/2]（默认 1）: " n
  case "${n:-1}" in
    2) echo "docker" ;;
    *) echo "systemd" ;;
  esac
}

# ------------------------------ Release 查询 / 下载 ------------------------------
# 取最新版本号（tag）
latest_tag() {
  local api="https://api.github.com/repos/${REPO}/releases/latest" tag
  tag="$(fetch "$api" - 2>/dev/null | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/' || true)"
  echo "$tag"
}

# download_binary <tag> <arch> <dest_bin>   下载并校验，安装到 dest（0755）
download_binary() {
  local tag="$1" arch="$2" dest="$3"
  local asset="${APP}-linux-${arch}.tar.gz"
  local base="https://github.com/${REPO}/releases/download/${tag}"
  local tmp; tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  info "下载 ${asset}（${tag}）…"
  fetch "${base}/${asset}" "${tmp}/${asset}" \
    || die "下载失败：${base}/${asset}（版本或架构对应的产物不存在？）"

  # 校验 sha256（checksums.txt 存在才校验，缺失不阻断）
  if fetch "${base}/checksums.txt" "${tmp}/checksums.txt" 2>/dev/null; then
    local want got
    want="$(grep " ${asset}\$" "${tmp}/checksums.txt" | awk '{print $1}' | head -1 || true)"
    if [ -n "$want" ] && command -v sha256sum >/dev/null 2>&1; then
      got="$(sha256sum "${tmp}/${asset}" | awk '{print $1}')"
      [ "$want" = "$got" ] || die "校验和不匹配！期望 $want，实得 $got（下载可能损坏或被篡改）"
      ok "sha256 校验通过"
    fi
  fi

  tar -xzf "${tmp}/${asset}" -C "$tmp" \
    || die "解压失败：${tmp}/${asset}"
  [ -f "${tmp}/${APP}" ] || die "压缩包内未找到二进制 ${APP}"
  mkdir -p "$(dirname "$dest")"
  install -m 0755 "${tmp}/${APP}" "$dest"
}

# install_self  把本脚本装成 /usr/local/bin/webvid，之后可直接 `webvid` 进菜单
install_self() {
  local src="${BASH_SOURCE[0]:-$0}"
  [ -f "$src" ] || return 0
  # 避免自己复制到自己
  [ "$(readlink -f "$src" 2>/dev/null || echo "$src")" = "$SELF_BIN" ] && return 0
  if install -m 0755 "$src" "$SELF_BIN" 2>/dev/null; then
    ok "管理命令已就绪：直接输入 ${C_BOLD}${APP}${C_RST} 即可进入菜单"
  else
    warn "无法写入 ${SELF_BIN}（PATH 里将没有 ${APP} 命令，可继续用 bash install.sh 管理）"
  fi
}

# ------------------------------ ffmpeg（仅 systemd 后端）------------------------------
install_ffmpeg() {
  if command -v ffmpeg >/dev/null 2>&1 && command -v ffprobe >/dev/null 2>&1; then
    ok "ffmpeg/ffprobe 已就绪：$(ffmpeg -version 2>/dev/null | head -1)"
    return 0
  fi
  info "安装 ffmpeg（缩略图与转码播放需要；缺失时程序仍可运行，仅相关功能降级）…"
  if   command -v apt-get >/dev/null 2>&1; then apt-get update -y && apt-get install -y ffmpeg
  elif command -v dnf     >/dev/null 2>&1; then dnf install -y ffmpeg || dnf install -y ffmpeg-free || true
  elif command -v yum     >/dev/null 2>&1; then yum install -y epel-release || true; yum install -y ffmpeg || true
  elif command -v zypper  >/dev/null 2>&1; then zypper install -y ffmpeg || true
  elif command -v pacman  >/dev/null 2>&1; then pacman -Sy --noconfirm ffmpeg || true
  elif command -v apk     >/dev/null 2>&1; then apk add --no-cache ffmpeg || true
  else warn "未识别包管理器，请自行安装 ffmpeg；或用 NL_FFMPEG/NL_FFPROBE 指定路径。"; return 0
  fi
  if command -v ffmpeg >/dev/null 2>&1; then ok "ffmpeg 安装完成"
  else warn "ffmpeg 未装上，视频缩略图/转码将不可用，其余功能正常。"; fi
}

# ------------------------------ systemd 后端 ------------------------------
write_service() {
  info "写入 systemd 单元：${SERVICE_FILE}"
  cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=WebVid — 自用网盘挂载 + Infuse 风格媒体库
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
ExecStart=${BIN}
Environment=NL_PORT=${PORT}
Environment=NL_DATA_DIR=${DATA_DIR}
Environment=NL_FILES_DIR=${FILES_DIR}
Restart=on-failure
RestartSec=5s
LimitNOFILE=65536
# 轻量加固：不影响对 ${INSTALL_DIR} 下 data/files 的读写
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
}

install_systemd() {
  need_systemd; pick_downloader
  local arch tag="${1:-}"
  arch="$(detect_arch)"

  if systemctl list-unit-files 2>/dev/null | grep -q "^${APP}.service"; then
    warn "检测到已安装的 ${APP}（systemd）。若要升级请用：${APP} update"
    read -r -p "覆盖重装？(会保留 data/files) [y/N] " a
    [[ "${a:-N}" =~ ^[Yy]$ ]] || { info "已取消。"; exit 0; }
    systemctl stop "$APP" 2>/dev/null || true
  fi

  [ -n "$tag" ] || tag="$(latest_tag)"
  [ -n "$tag" ] || die "未找到任何 Release。请先在仓库打标签发布，或用 'install systemd v1.0.1' 指定版本。"
  info "后端：systemd    版本：${tag}    架构：${arch}"

  install_ffmpeg
  mkdir -p "$INSTALL_DIR" "$DATA_DIR" "$FILES_DIR"
  download_binary "$tag" "$arch" "$BIN"
  chmod 700 "$DATA_DIR" || true

  write_service
  echo "systemd" > "$BACKEND_FILE"
  echo "$tag"    > "$VERSION_FILE"
  install_self
  systemctl enable "$APP" >/dev/null 2>&1 || true
  systemctl restart "$APP"

  info "等待服务就绪…"
  if wait_ready; then ok "服务已启动"; else warn "健康检查超时，请查看日志：${APP} log"; fi
  print_password
  show_access
}

# ------------------------------ Docker 后端 ------------------------------
need_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    warn "未检测到 docker。"
    if [ -t 0 ]; then
      read -r -p "现在用官方脚本安装 Docker？(curl https://get.docker.com | sh) [Y/n] " a
      [[ "${a:-Y}" =~ ^[Nn]$ ]] && die "已取消。请先安装 Docker 再重试。"
      pick_downloader
      fetch "https://get.docker.com" - | sh || die "Docker 安装失败，请手动安装后重试。"
    else
      die "未检测到 docker，请先安装：curl -fsSL https://get.docker.com | sh"
    fi
  fi
  if ! docker info >/dev/null 2>&1; then
    systemctl start docker 2>/dev/null || true
    docker info >/dev/null 2>&1 || die "Docker 守护进程未运行或无权限（root 下应正常）。"
  fi
}

# pick_compose  选择 docker compose 调用方式（插件优先，回退 docker-compose）
pick_compose() {
  if docker compose version >/dev/null 2>&1; then DC="docker compose";
  elif command -v docker-compose >/dev/null 2>&1; then DC="docker-compose";
  else die "需要 docker compose 插件或 docker-compose，请先安装。"; fi
}
# dc  在项目目录下调用 compose（相对路径 ./data ./files ./.build 均以 INSTALL_DIR 为基准）
dc() { $DC -f "$COMPOSE_FILE" --project-directory "$INSTALL_DIR" "$@"; }

write_dockerfile() {
  mkdir -p "$BUILD_DIR"
  cat > "${BUILD_DIR}/Dockerfile" <<'EOF'
# 精简运行时镜像：直接装入 release 预编译二进制（不在容器内编译），仅补 ffmpeg。
FROM alpine:3.22
RUN apk add --no-cache ffmpeg ca-certificates tzdata wget
ENV NL_PORT=5243 NL_DATA_DIR=/data NL_FILES_DIR=/files
# 非 root 运行：uid/gid=1000，便于与宿主 bind mount 对齐
RUN addgroup -g 1000 webvid && adduser -D -u 1000 -G webvid webvid \
    && mkdir -p /data /files && chown -R webvid:webvid /data /files
COPY webvid /usr/local/bin/webvid
USER webvid
EXPOSE 5243
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -qO- "http://127.0.0.1:5243/api/ping" >/dev/null 2>&1 || exit 1
ENTRYPOINT ["webvid"]
EOF
}

write_compose() {
  local pw_env=""
  [ -n "${WEBVID_ADMIN_PASSWORD:-}" ] && pw_env=$'\n      NL_ADMIN_PASSWORD: "'"${WEBVID_ADMIN_PASSWORD}"'"'
  cat > "$COMPOSE_FILE" <<EOF
name: webvid
services:
  webvid:
    build:
      context: ./.build
    image: ${IMAGE}
    container_name: ${APP}
    ports:
      - "${PORT}:5243"
    volumes:
      - ./data:/data     # 数据库/缩略图缓存/转码临时文件
      - ./files:/files   # 首启自动挂载为「/本地存储」
    environment:
      TZ: Asia/Shanghai${pw_env}
    restart: unless-stopped
EOF
}

install_docker() {
  need_docker; pick_compose; pick_downloader
  local arch tag="${1:-}"
  arch="$(detect_arch)"

  if [ -f "$COMPOSE_FILE" ] && dc ps 2>/dev/null | grep -q "$APP"; then
    warn "检测到已安装的 ${APP}（Docker）。若要升级请用：${APP} update"
    read -r -p "覆盖重装？(会保留 data/files) [y/N] " a
    [[ "${a:-N}" =~ ^[Yy]$ ]] || { info "已取消。"; exit 0; }
  fi

  [ -n "$tag" ] || tag="$(latest_tag)"
  [ -n "$tag" ] || die "未找到任何 Release。请先在仓库打标签发布，或用 'install docker v1.0.1' 指定版本。"
  info "后端：Docker    版本：${tag}    架构：${arch}"

  mkdir -p "$BUILD_DIR" "$DATA_DIR" "$FILES_DIR"
  download_binary "$tag" "$arch" "${BUILD_DIR}/${APP}"
  # bind mount 的可写性取决于宿主目录属主：交给容器内 uid=1000
  chown -R 1000:1000 "$DATA_DIR" "$FILES_DIR" 2>/dev/null \
    || warn "chown data/files 到 1000 失败，容器可能无写权限（可改 compose 的 user 字段）"
  write_dockerfile
  write_compose
  echo "docker" > "$BACKEND_FILE"
  echo "$tag"   > "$VERSION_FILE"
  install_self

  info "构建镜像并启动容器…"
  dc up -d --build

  info "等待服务就绪…"
  if wait_ready; then ok "服务已启动"; else warn "健康检查超时，请查看日志：${APP} log"; fi
  print_password
  show_access
}

# ------------------------------ 健康等待 ------------------------------
wait_ready() {
  [ -n "$DL" ] || pick_downloader
  local i
  for i in $(seq 1 30); do
    if fetch "http://127.0.0.1:${PORT}/api/ping" - >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

# ------------------------------ 首启密码 ------------------------------
print_password() {
  local backend out; backend="$(current_backend)"
  if [ "$backend" = "docker" ]; then
    [ -n "$DC" ] || { pick_compose; }
    out="$(dc logs 2>/dev/null | grep -A4 '初始管理员' | tail -6 || true)"
  else
    out="$(journalctl -u "$APP" --no-pager 2>/dev/null | grep -A4 '初始管理员' | tail -6 || true)"
  fi
  if [ -n "$out" ]; then
    echo
    echo "${C_BOLD}$out${C_RST}"
    echo
    info "如已登录并改过密码，上面是历史初值，仅供参考。"
  else
    warn "日志中未找到初始密码（可能非首次建库，或日志已轮转）。"
    warn "可用 ${APP} reset-password 重新生成管理员密码。"
  fi
}

# ------------------------------ 重置密码 ------------------------------
# 通过 app 的 reset-password 子命令重置（仅改管理员密码，保留存储/其他用户）。
# 需短暂停服务避免争 SQLite 写锁；完成后自动拉起。
cmd_reset_password() {
  need_root
  local backend newpw="${1:-}"; backend="$(current_backend)"
  case "$backend" in
    systemd)
      [ -x "$BIN" ] || die "未安装或二进制缺失：$BIN"
      info "停止服务以安全改密…"
      systemctl stop "$APP" 2>/dev/null || true
      NL_DATA_DIR="$DATA_DIR" "$BIN" reset-password "$newpw" || { systemctl start "$APP" || true; die "重置失败。"; }
      systemctl start "$APP"
      ;;
    docker)
      need_docker; pick_compose
      [ -f "$COMPOSE_FILE" ] || die "未检测到 Docker 安装。"
      info "停止容器以安全改密…"
      dc stop 2>/dev/null || true
      # 一次性容器执行子命令，与已停的主容器共用 /data 卷
      dc run --rm webvid reset-password "$newpw" || { dc up -d || true; die "重置失败。"; }
      dc up -d
      ;;
    *)
      die "未检测到已安装的 WebVid。请先 ${APP} install。"
      ;;
  esac
  info "服务已重新启动。请用上面的新密码登录。"
}

# ------------------------------ 访问信息 ------------------------------
show_access() {
  local ip
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')"; [ -z "$ip" ] && ip="<服务器IP>"
  echo
  echo "${C_GREEN}${C_BOLD}================ WebVid 就绪 ================${C_RST}"
  echo "  本机访问：  http://127.0.0.1:${PORT}"
  echo "  局域网：    http://${ip}:${PORT}"
  echo "  部署后端：  $(current_backend)"
  echo "  安装目录：  ${INSTALL_DIR}"
  echo "  数据目录：  ${DATA_DIR}"
  echo "  本地存储：  ${FILES_DIR}  (自动挂载为「/本地存储」)"
  echo "${C_GREEN}${C_BOLD}============================================${C_RST}"
  echo "  管理命令： ${APP} {start|stop|restart|status|log|update|reset-password}"
  echo "  或直接输入 ${C_BOLD}${APP}${C_RST} 进入交互菜单。"
  echo "  ${C_YELLOW}公网部署请置于 HTTPS 反向代理之后（本服务不内置 TLS）。${C_RST}"
  echo
}

# ============================== 子命令分派 ==============================
cmd_install() {
  need_root install
  # 解析：可选后端关键字 + 可选版本，顺序不限
  local backend="" tag=""
  local a
  for a in "$@"; do
    case "$a" in
      docker|systemd) backend="$a" ;;
      latest) tag="" ;;
      *) tag="$a" ;;
    esac
  done
  [ -n "$backend" ] || backend="${WEBVID_BACKEND:-}"
  if [ -z "$backend" ]; then
    if [ -t 0 ]; then backend="$(ask_backend)"; else backend="systemd"; fi
  fi
  local cur; cur="$(current_backend)"
  if [ -n "$cur" ] && [ "$cur" != "$backend" ]; then
    warn "已存在 ${cur} 部署，却要求安装 ${backend}。建议先 ${APP} uninstall 再换后端，避免端口冲突。"
  fi
  case "$backend" in
    docker)  install_docker "$tag" ;;
    systemd) install_systemd "$tag" ;;
    *) die "未知后端：$backend（可选 systemd | docker）" ;;
  esac
}

cmd_update() {
  need_root update
  local backend tag="${1:-}"; backend="$(current_backend)"
  local arch; arch="$(detect_arch)"
  pick_downloader
  [ -n "$tag" ] || tag="$(latest_tag)"
  [ -n "$tag" ] || die "无法获取最新版本，请手动指定：${APP} update v1.0.1"

  case "$backend" in
    systemd)
      need_systemd
      [ -f "$BIN" ] || die "未安装。请先运行：${APP} install"
      info "升级到 ${tag}（systemd）…"
      systemctl stop "$APP" 2>/dev/null || true
      cp -f "$BIN" "${BIN}.bak" 2>/dev/null || true
      if download_binary "$tag" "$arch" "$BIN"; then
        echo "$tag" > "$VERSION_FILE"; install_self
        systemctl start "$APP"
        if wait_ready; then ok "升级完成并已启动：${tag}"; rm -f "${BIN}.bak"
        else warn "新版本启动异常，回滚…"; [ -f "${BIN}.bak" ] && mv -f "${BIN}.bak" "$BIN"; systemctl restart "$APP" || true; die "已回滚，见 ${APP} log"; fi
      else
        [ -f "${BIN}.bak" ] && mv -f "${BIN}.bak" "$BIN"; systemctl start "$APP" || true; die "下载失败，已保持旧版本。"
      fi
      ;;
    docker)
      need_docker; pick_compose
      [ -f "$COMPOSE_FILE" ] || die "未安装。请先运行：${APP} install"
      info "升级到 ${tag}（Docker）…"
      cp -f "${BUILD_DIR}/${APP}" "${BUILD_DIR}/${APP}.bak" 2>/dev/null || true
      if download_binary "$tag" "$arch" "${BUILD_DIR}/${APP}"; then
        echo "$tag" > "$VERSION_FILE"; install_self
        dc up -d --build
        if wait_ready; then ok "升级完成并已启动：${tag}"; rm -f "${BUILD_DIR}/${APP}.bak"
        else warn "新版本启动异常，回滚…"; [ -f "${BUILD_DIR}/${APP}.bak" ] && mv -f "${BUILD_DIR}/${APP}.bak" "${BUILD_DIR}/${APP}"; dc up -d --build || true; die "已回滚，见 ${APP} log"; fi
      else
        [ -f "${BUILD_DIR}/${APP}.bak" ] && mv -f "${BUILD_DIR}/${APP}.bak" "${BUILD_DIR}/${APP}"; die "下载失败，已保持旧版本。"
      fi
      ;;
    *) die "未检测到已安装的 WebVid。请先 ${APP} install。" ;;
  esac
}

cmd_uninstall() {
  need_root uninstall
  local backend; backend="$(current_backend)"
  case "$backend" in
    systemd)
      need_systemd
      info "停止并卸载 ${APP}（systemd）…"
      systemctl stop "$APP" 2>/dev/null || true
      systemctl disable "$APP" 2>/dev/null || true
      rm -f "$SERVICE_FILE"; systemctl daemon-reload
      ok "systemd 服务已移除。"
      ;;
    docker)
      need_docker; pick_compose
      info "停止并移除 ${APP}（Docker）…"
      dc down 2>/dev/null || true
      docker image rm "$IMAGE" 2>/dev/null || true
      ok "容器与镜像已移除。"
      ;;
    *) die "未检测到已安装的 WebVid。" ;;
  esac
  rm -f "$BACKEND_FILE" "$VERSION_FILE"

  echo
  warn "数据目录 ${DATA_DIR}（含 newlist.db：驱动凭据/用户密码哈希）与本地存储 ${FILES_DIR} 默认保留。"
  read -r -p "是否一并删除整个 ${INSTALL_DIR}（含所有数据、无法恢复）？[y/N] " a
  if [[ "${a:-N}" =~ ^[Yy]$ ]]; then
    rm -rf "$INSTALL_DIR"; ok "已删除 ${INSTALL_DIR}"
    rm -f "$SELF_BIN" 2>/dev/null || true
  else
    info "已保留数据于 ${INSTALL_DIR}。彻底清理可自行 rm -rf 该目录。"
  fi
}

cmd_start() {
  need_root
  case "$(current_backend)" in
    docker)  need_docker; pick_compose; dc up -d; ok "已启动" ;;
    systemd) systemctl start "$APP"; ok "已启动" ;;
    *) die "未检测到已安装的 WebVid。请先 ${APP} install。" ;;
  esac
}
cmd_stop() {
  need_root
  case "$(current_backend)" in
    docker)  need_docker; pick_compose; dc stop; ok "已停止" ;;
    systemd) systemctl stop "$APP"; ok "已停止" ;;
    *) die "未检测到已安装的 WebVid。" ;;
  esac
}
cmd_restart() {
  need_root
  case "$(current_backend)" in
    docker)  need_docker; pick_compose; dc restart; ok "已重启" ;;
    systemd) systemctl restart "$APP"; ok "已重启" ;;
    *) die "未检测到已安装的 WebVid。" ;;
  esac
}
cmd_status() {
  case "$(current_backend)" in
    docker)  need_docker; pick_compose; dc ps ;;
    systemd) systemctl status "$APP" --no-pager || true ;;
    *) die "未检测到已安装的 WebVid。" ;;
  esac
}
cmd_log() {
  case "$(current_backend)" in
    docker)  need_docker; pick_compose; info "跟随日志（Ctrl+C 退出）"; dc logs -f ;;
    systemd) info "跟随日志（Ctrl+C 退出）"; journalctl -u "$APP" -f --no-pager ;;
    *) die "未检测到已安装的 WebVid。" ;;
  esac
}
cmd_password() { print_password; }

cmd_version() {
  pick_downloader
  local cur latest
  cur="$(cat "$VERSION_FILE" 2>/dev/null || true)"; [ -n "$cur" ] || cur="未安装"
  latest="$(latest_tag)"; [ -n "$latest" ] || latest="（无法获取，检查网络或仓库 Release）"
  echo "  部署后端：$(current_backend)"
  echo "  已安装：  $cur"
  echo "  最新版：  $latest"
}

# ------------------------------ 交互菜单 ------------------------------
menu() {
  local b; b="$(current_backend)"; [ -n "$b" ] || b="未安装"
  echo
  echo "${C_BOLD}  WebVid 管理脚本${C_RST}  —  https://github.com/${REPO}"
  echo "  当前后端：${b}"
  echo "  ------------------------------------------"
  echo "   1) 安装 install      2) 升级 update"
  echo "   3) 卸载 uninstall    4) 重启 restart"
  echo "   5) 启动 start        6) 停止 stop"
  echo "   7) 状态 status       8) 日志 log"
  echo "   9) 重新生成密码      10) 查看初始密码"
  echo "  11) 版本 version       0) 退出"
  echo "  ------------------------------------------"
  read -r -p "  选择 [0-11]: " n
  case "${n:-}" in
    1) cmd_install ;;   2) cmd_update ;;   3) cmd_uninstall ;;
    4) cmd_restart ;;   5) cmd_start ;;    6) cmd_stop ;;
    7) cmd_status ;;    8) cmd_log ;;      9) cmd_reset_password ;;
    10) cmd_password ;; 11) cmd_version ;; 0|"") exit 0 ;;
    *) err "无效选择：$n" ;;
  esac
}

# ------------------------------ 入口 ------------------------------
main() {
  local sub="${1:-}"; shift || true
  case "$sub" in
    install)                 cmd_install "$@" ;;
    update|upgrade)          cmd_update "$@" ;;
    uninstall|remove)        cmd_uninstall ;;
    start)                   cmd_start ;;
    stop)                    cmd_stop ;;
    restart)                 cmd_restart ;;
    status)                  cmd_status ;;
    log|logs)                cmd_log ;;
    reset-password|resetpw|passwd-reset) cmd_reset_password "$@" ;;
    password|passwd)         cmd_password ;;
    version)                 cmd_version ;;
    ""|menu)                 menu ;;
    help|-h|--help)
      awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "${BASH_SOURCE[0]:-$0}" ;;
    *) err "未知子命令：$sub"; echo "运行 'bash install.sh help' 查看用法。"; exit 1 ;;
  esac
}
main "$@"
