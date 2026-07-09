# WebVid

自用轻量网盘挂载 + Infuse 风格媒体库：单二进制、SQLite、液态玻璃 UI。

- **多存储挂载**：本地目录 / OneDrive（refresh_token 或应用凭据）/ PikPak，统一逻辑路径树
- **媒体库**：Apple TV / Infuse 风格视频库与照片墙（随机推荐、最近添加、播放历史、滚动加载），
  每个存储可单独控制是否进视频库/照片墙/搜索
- **转码播放**：mp4/webm 直连秒开；mkv/avi/wmv/flv/rmvb、DTS/AC3 音轨等经 ffmpeg 转 HLS——
  能 remux 绝不重编码（`-c copy` 零 CPU 秒开），必要时才 libx264；全片进度条可拖
- **多线程加速**：云盘存储可开代理模式，服务器↔云盘并发 Range 分块拉流（播放/下载/转存共用）
- **任务系统**：跨存储转存（如 PikPak → OneDrive 服务器端搬运）带进度/取消/重试
- **权限**：必须登录；子用户可限定可见目录（base_path）与只读
- **全局搜索**：SQLite 索引，后台一键重建
- 空闲内存 < 50MB；无常驻扫描，索引/缩略图/转码全部按需触发

## 一键安装（Linux 服务器 / systemd）

```bash
curl -fsSL https://raw.githubusercontent.com/J606y/webvid/main/install.sh -o install.sh \
  && sudo bash install.sh install
```

脚本会自动检测 CPU 架构、安装 ffmpeg、从 GitHub Release 下载对应二进制到 `/opt/webvid`、
注册并启动 `webvid` systemd 服务（开机自启），最后打印初始管理员账号密码与访问地址。
装完后用 `webvid` 命令管理（脚本即 `install.sh`，可 `sudo cp install.sh /usr/local/bin/webvid`）：

```bash
sudo bash install.sh            # 无参数进入交互菜单
sudo bash install.sh update     # 升级到最新版（保留 data/、files/）
sudo bash install.sh status     # 查看运行状态
sudo bash install.sh log        # 跟随日志
sudo bash install.sh password   # 再次查看初始密码
sudo bash install.sh uninstall  # 卸载（询问是否删数据）
```

默认监听 `5243`，数据在 `/opt/webvid/data`、本地存储在 `/opt/webvid/files`；
可用环境变量覆盖：`WEBVID_PORT`、`WEBVID_DIR`（安装目录）。
公网访问务必置于 HTTPS 反向代理之后（本服务不内置 TLS，见下「部署与安全」）。

> 需先在仓库打过版本标签（`git tag v1.0.0 && git push --tags`）触发 GitHub Actions 构建出
> Release 产物，脚本才能下载安装。

## 快速开始（Docker）

```bash
docker compose up -d --build
docker logs webvid        # 首次启动在日志里打印随机管理员密码
```

打开 `http://localhost:5243`，用日志中的 `admin` 账号登录。
`./files` 会自动挂载为「/本地存储」；数据（数据库/缩略图/转码缓存）都在 `./data`。

想固定首启密码：在 `docker-compose.yml` 里取消 `NL_ADMIN_PASSWORD` 注释（仅数据库初建时生效）。

### 直接跑二进制（Windows/Linux）

```bash
cd frontend && npm ci && npm run build && cd ..   # 前端产物嵌入二进制
go build -o webvid .
NL_ADMIN_PASSWORD=admin123 ./webvid
```

需要 ffmpeg/ffprobe 在 PATH（或用 `NL_FFMPEG` / `NL_FFPROBE` 指定路径），
缺失时程序正常运行，仅视频缩略图与转码播放降级不可用。

## 挂载存储

后台管理 → 存储 → 添加，选择驱动填写动态表单：

| 驱动 | 必填 | 说明 |
|---|---|---|
| local | root_path | 宿主机绝对路径（容器内路径，如 `/files/media`） |
| onedrive | client_id、client_secret、refresh_token | 个人/企业账号 OAuth。refresh_token 可用任意 OneDrive 授权工具获取（权限需含 Files.ReadWrite.All + offline_access），token 轮换会自动回写配置 |
| onedrive_app | tenant_id、client_id、client_secret、drive_id | 应用专用凭据（client_credentials），Azure 门户注册应用并授 Sites/Files 应用权限 |
| pikpak | username、password（或 refresh_token） | 账密自动登录并维护 token；官方风控严时可能触发人机验证，稍后重试 |
| telegram | api_id、api_hash、phone | **只读**驱动：把 TG 消息转发到本人「收藏夹」，本站平铺读取；api_id/api_hash 在 my.telegram.org 申请，添加存储后在后台该行「🔑」按钮走手机验证码登录（session 自动保存，无需重复登录）；网络受限可填 socks5 代理；只读故无法从本站直接写入，常配合任务系统「转存」到其他可写存储 |

远端驱动通用字段：

- `proxy`：开启后下载/播放经服务器中转（配合多线程加速；关闭则 302 直链，服务器零流量）
- `threads` / `chunk_mb`：代理模式并发 Range 连接数（默认 4）与分块大小（默认 4MB）
- `show_video` / `show_photo` / `show_search`：该存储内容是否进视频库/照片墙/搜索

存储保存后会自动重建索引；重建完成后自动开始**媒体预载**——把勾选了视频库/照片墙展示的
文件封面下载/生成落盘（`data/thumbs/`），并对非原生格式视频预探测播放策略与时长写入
数据库，之后浏览媒体库即刻出图、打开视频详情/播放免现场探测。进度与手动重跑入口
见后台「索引管理」页签；预载幂等，已缓存内容跳过，云端单文件失败下轮自动重试。

## 转码与加速说明

- 播放策略由 `ffprobe` 探测自动决定：**direct**（mp4/webm 家族直连原文件，不起 ffmpeg）→
  **remux**（编码可播但容器不认，如 mkv 里的 h264+aac，`-c copy` 换封装，CPU 几乎为零）→
  **transcode**（h265/wmv/rv40 等重编码 libx264 veryfast；视频可播仅音轨不可播时只转音频）
- 转码分片缓存在 `data/transcode/`，会话空闲 5 分钟自动回收，并发限 2 路，重启自动清空
- 软转 1080p h265 大约需要 4 核 CPU；remux 与 direct 几乎不耗 CPU
- 云盘文件转码时输入走服务器回环拉流，直链过期自动换链；配合 `proxy + threads` 可解决慢源

## 常用环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| NL_PORT | 5243 | 监听端口 |
| NL_DATA_DIR | ./data（镜像内 /data） | 数据库与缓存目录 |
| NL_FILES_DIR | ./files（镜像内 /files） | 首启自动挂载的本地存储 |
| NL_ADMIN_USER / NL_ADMIN_PASSWORD | admin / 随机 | 仅首次建库生效 |
| NL_FFMPEG / NL_FFPROBE | 自动探测 | ffmpeg/ffprobe 路径 |
| NL_TRUSTED_PROXIES | 回环 + 内网网段 | 逗号分隔 CIDR，声明可信任的反向代理来源（见下「部署与安全」） |

## 部署与安全

公网部署前请务必确认以下几点：

1. **`data/newlist.db` 是最高机密文件**：里面存着云盘驱动的 client_secret/refresh_token、
   管理员与所有用户的密码哈希、自动生成并落库的 `jwt_secret`。**该文件与整个 `data/` 目录绝不能
   提交进代码仓库、上传到公开位置或以明文方式分享**（`.gitignore`/`.dockerignore` 已排除 `data/`，
   但这只防止误提交，不代表磁盘上是安全的）。建议额外收紧宿主机 `data/` 目录权限（如
   `chmod 700 data`，仅运行服务的账号可读写）；确需备份时，请对备份文件加密后再落盘或外传。
2. **本项目不内置 TLS**：只提供裸 HTTP 服务，不做证书申请/续期。公网访问必须放在支持 HTTPS
   的反向代理（Nginx / Caddy / Traefik 等）之后，由反代终止 TLS 并转发到 `NL_PORT`；不要把
   服务端口直接暴露给公网。
3. **反代之后要让服务拿到真实客户端 IP**：反代默认会让所有请求看起来都来自反代自身
   （如 `127.0.0.1`），登录失败限流与访问日志会因此把不同来源的用户误判为同一个人/同一次攻击。
   反代需设置 `X-Forwarded-For`/`X-Real-IP` 等头，服务端用环境变量 `NL_TRUSTED_PROXIES`
   （逗号分隔的 CIDR 列表，缺省仅信任回环地址与常见内网网段）声明"这些地址发来的连接可信、
   其转发头可以采信"——只有命中该列表的连接，其转发头里的客户端 IP 才会被采用；未在列表内的
   来源即使伪造该头也不会被信任，从而避免公网直接绕过登录限流。反代的出口 IP（或所在网段）
   必须包含在这个列表里，否则限流/日志会一直显示成反代自己的 IP。

## 忘记密码

停止服务后删除 `data/newlist.db`（索引/用户/存储配置会一并清空，存储需重新挂载），
重启后按首启流程重新生成管理员账号；或用有管理员权限的账号在后台重置他人密码。

## 许可证

AGPL-3.0。自用项目，闭源分发或提供网络服务须遵循 AGPL 开源义务。
