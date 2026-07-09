#!/usr/bin/env bash
#
# WebVid 一键安装 / 管理脚本（Linux + systemd）
# 项目：https://github.com/J606y/webvid
#
# 用法：
#   sudo bash install.sh [子命令] [参数]
#
# 子命令：
#   install [版本]     安装并启动服务（省略版本则装最新 Release，如 v1.0.0）
#   update  [版本]     升级到指定/最新版本（保留 data/ 与 files/）
#   uninstall          卸载服务（会询问是否一并删除数据）
#   start | stop | restart | status | log     服务生命周期管理
#   password           打印首启时生成的初始管理员账号密码
#   version            显示已安装版本与最新可用版本
#   (无参数)           进入交互菜单
#
# 一行远程安装：
#   curl -fsSL https://raw.githubusercontent.com/J606y/webvid/main/install.sh -o install.sh \
#     && sudo bash install.sh install
#
set -euo pipefail

# ------------------------------ 可配置项 ------------------------------
REPO="J606y/webvid"                          # GitHub 仓库
APP="webvid"                                  # 二进制 / 服务名
INSTALL_DIR="${WEBVID_DIR:-/opt/webvid}"      # 安装目录（可用环境变量覆盖）
DATA_DIR="${INSTALL_DIR}/data"                # 数据库/缩略图/转码缓存（含机密 newlist.db）
FILES_DIR="${INSTALL_DIR}/files"              # 首启自动挂载的本地存储
PORT="${WEBVID_PORT:-5243}"                   # 监听端口（NL_PORT）
SERVICE_FILE="/etc/systemd/system/${APP}.service"
BIN="${INSTALL_DIR}/${APP}"

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
    die "请用 root 运行（sudo bash install.sh $*）"
  fi
}

need_systemd() {
  command -v systemctl >/dev/null 2>&1 || \
    die "未检测到 systemd（systemctl）。本脚本面向 systemd 发行版；容器/精简环境请直接用二进制或 Docker。"
}

# 下载工具：优先 curl，回退 wget
DL=""
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

# ------------------------------ 包管理器 / ffmpeg ------------------------------
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

# ------------------------------ Release 查询 / 下载 ------------------------------
# 取最新版本号（tag）；无 Release 时给出明确指引
latest_tag() {
  local api="https://api.github.com/repos/${REPO}/releases/latest" tag
  tag="$(fetch "$api" - 2>/dev/null | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/' || true)"
  echo "$tag"
}

# download_binary <tag> <arch> <dest_bin>
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
  install -m 0755 "${tmp}/${APP}" "$dest"
}

# ------------------------------ systemd 单元 ------------------------------
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

# ------------------------------ 首启密码 ------------------------------
print_password() {
  # 首启时初始管理员账号密码打印在服务日志里（仅建库当次）
  local out
  out="$(journalctl -u "$APP" --no-pager 2>/dev/null | grep -A4 '初始管理员账号' | tail -6 || true)"
  if [ -n "$out" ]; then
    echo
    echo "${C_BOLD}$out${C_RST}"
    echo
    info "如已登录并改过密码，上面是历史初值，仅供参考。"
  else
    warn "日志中未找到初始密码。可能：①非首次建库（数据库已存在）②日志已轮转。"
    warn "忘记密码可停服务后删除 ${DATA_DIR}/newlist.db 重置（会清空用户/存储配置）。"
  fi
}

# ------------------------------ 健康等待 ------------------------------
wait_ready() {
  local i
  for i in $(seq 1 30); do
    if fetch "http://127.0.0.1:${PORT}/api/ping" - >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

# ------------------------------ 访问信息 ------------------------------
show_access() {
  local ip
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')"; [ -z "$ip" ] && ip="<服务器IP>"
  echo
  echo "${C_GREEN}${C_BOLD}================ WebVid 就绪 ================${C_RST}"
  echo "  本机访问：  http://127.0.0.1:${PORT}"
  echo "  局域网：    http://${ip}:${PORT}"
  echo "  安装目录：  ${INSTALL_DIR}"
  echo "  数据目录：  ${DATA_DIR}   ${C_YELLOW}(含机密 newlist.db，已 chmod 700)${C_RST}"
  echo "  本地存储：  ${FILES_DIR}  (自动挂载为「/本地存储」)"
  echo "${C_GREEN}${C_BOLD}============================================${C_RST}"
  echo "  常用命令： webvid {start|stop|restart|status|log|update|password}"
  echo "  ${C_YELLOW}公网部署请置于 HTTPS 反向代理之后（本服务不内置 TLS）。${C_RST}"
  echo
}

# ============================== 子命令 ==============================
cmd_install() {
  need_root install; need_systemd; pick_downloader
  local arch tag="${1:-}"
  arch="$(detect_arch)"

  if systemctl list-unit-files 2>/dev/null | grep -q "^${APP}.service"; then
    warn "检测到已安装的 ${APP} 服务。若要升级请用：webvid update"
    read -r -p "覆盖重装？(会保留 data/files) [y/N] " a
    [[ "${a:-N}" =~ ^[Yy]$ ]] || { info "已取消。"; exit 0; }
    systemctl stop "$APP" 2>/dev/null || true
  fi

  [ -n "$tag" ] || tag="$(latest_tag)"
  [ -n "$tag" ] || die "未找到任何 Release。请先在仓库打标签发布（git tag v1.0.0 && git push --tags，等 Actions 构建完成），或用 'install v1.0.0' 指定版本。"
  info "目标版本：${tag}    架构：${arch}"

  install_ffmpeg
  mkdir -p "$INSTALL_DIR" "$DATA_DIR" "$FILES_DIR"
  download_binary "$tag" "$arch" "$BIN"
  chmod 700 "$DATA_DIR" || true    # 收紧机密目录权限（README 部署与安全建议）

  write_service
  systemctl enable "$APP" >/dev/null 2>&1 || true
  systemctl restart "$APP"

  info "等待服务就绪…"
  if wait_ready; then ok "服务已启动"; else
    warn "健康检查超时，请查看日志：webvid log"
  fi
  print_password
  show_access
}

cmd_update() {
  need_root update; need_systemd; pick_downloader
  [ -f "$BIN" ] || die "未安装。请先运行：webvid install"
  local arch tag="${1:-}"
  arch="$(detect_arch)"
  [ -n "$tag" ] || tag="$(latest_tag)"
  [ -n "$tag" ] || die "无法获取最新版本，请手动指定：webvid update v1.0.0"

  info "升级到 ${tag}（当前二进制：${BIN}）…"
  systemctl stop "$APP" 2>/dev/null || true
  cp -f "$BIN" "${BIN}.bak" 2>/dev/null || true    # 备份旧版，失败可回滚
  if download_binary "$tag" "$arch" "$BIN"; then
    systemctl start "$APP"
    if wait_ready; then
      ok "升级完成并已启动：${tag}"
      rm -f "${BIN}.bak"
    else
      warn "新版本启动异常，回滚到旧版本…"
      [ -f "${BIN}.bak" ] && mv -f "${BIN}.bak" "$BIN"
      systemctl restart "$APP" || true
      die "已回滚。请查看日志：webvid log"
    fi
  else
    [ -f "${BIN}.bak" ] && mv -f "${BIN}.bak" "$BIN"
    systemctl start "$APP" || true
    die "下载失败，已保持旧版本。"
  fi
}

cmd_uninstall() {
  need_root uninstall; need_systemd
  info "停止并卸载 ${APP} 服务…"
  systemctl stop "$APP" 2>/dev/null || true
  systemctl disable "$APP" 2>/dev/null || true
  rm -f "$SERVICE_FILE"
  systemctl daemon-reload
  ok "服务已移除。"

  echo
  warn "数据目录 ${DATA_DIR}（含 newlist.db：驱动凭据/用户密码哈希）与本地存储 ${FILES_DIR} 默认保留。"
  read -r -p "是否一并删除整个 ${INSTALL_DIR}（含所有数据、无法恢复）？[y/N] " a
  if [[ "${a:-N}" =~ ^[Yy]$ ]]; then
    rm -rf "$INSTALL_DIR"
    ok "已删除 ${INSTALL_DIR}"
  else
    info "已保留数据于 ${INSTALL_DIR}。彻底清理可自行 rm -rf 该目录。"
  fi
}

cmd_start()   { need_root; systemctl start   "$APP"; ok "已启动"; }
cmd_stop()    { need_root; systemctl stop    "$APP"; ok "已停止"; }
cmd_restart() { need_root; systemctl restart "$APP"; ok "已重启"; }
cmd_status()  { systemctl status "$APP" --no-pager || true; }
cmd_log()     { info "跟随日志（Ctrl+C 退出）"; journalctl -u "$APP" -f --no-pager; }
cmd_password(){ print_password; }

cmd_version() {
  pick_downloader
  local cur="未安装" latest
  if [ -x "$BIN" ]; then
    # 二进制无 --version 标志时退回读服务日志里的启动版本行
    cur="$(journalctl -u "$APP" --no-pager 2>/dev/null | grep -oE 'WebVid [0-9]+\.[0-9]+\.[0-9]+' | tail -1 || true)"
    [ -n "$cur" ] || cur="已安装（版本未知）"
  fi
  latest="$(latest_tag)"; [ -n "$latest" ] || latest="（无法获取，检查网络或仓库 Release）"
  echo "  已安装：$cur"
  echo "  最新版：$latest"
}

# ------------------------------ 交互菜单 ------------------------------
menu() {
  echo
  echo "${C_BOLD}  WebVid 管理脚本${C_RST}  —  https://github.com/${REPO}"
  echo "  ------------------------------------------"
  echo "   1) 安装 install"
  echo "   2) 升级 update"
  echo "   3) 卸载 uninstall"
  echo "   4) 启动 start      5) 停止 stop"
  echo "   6) 重启 restart    7) 状态 status"
  echo "   8) 日志 log        9) 查看初始密码"
  echo "  10) 版本 version    0) 退出"
  echo "  ------------------------------------------"
  read -r -p "  选择 [0-10]: " n
  case "${n:-}" in
    1) cmd_install ;;   2) cmd_update ;;   3) cmd_uninstall ;;
    4) cmd_start ;;     5) cmd_stop ;;     6) cmd_restart ;;
    7) cmd_status ;;    8) cmd_log ;;      9) cmd_password ;;
    10) cmd_version ;;  0|"") exit 0 ;;
    *) err "无效选择：$n" ;;
  esac
}

# ------------------------------ 入口 ------------------------------
main() {
  local sub="${1:-}"; shift || true
  case "$sub" in
    install)   cmd_install "$@" ;;
    update|upgrade) cmd_update "$@" ;;
    uninstall|remove) cmd_uninstall ;;
    start)     cmd_start ;;
    stop)      cmd_stop ;;
    restart)   cmd_restart ;;
    status)    cmd_status ;;
    log|logs)  cmd_log ;;
    password|passwd) cmd_password ;;
    version)   cmd_version ;;
    ""|menu)   menu ;;
    help|-h|--help)
      # 打印文件顶部注释头（跳过 shebang，遇首个非注释行即止）
      awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "$0" ;;
    *) err "未知子命令：$sub"; echo "运行 'bash install.sh help' 查看用法。"; exit 1 ;;
  esac
}
main "$@"
