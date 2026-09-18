# WebVid

自建的网盘与媒体库。把本地目录和各家网盘挂成同一棵目录树，用视频库的方式浏览和播放。

单个可执行文件，一个 SQLite 数据库，装完即用。

## 功能

**多个存储，一棵目录树**
本地目录、OneDrive、Google 云端硬盘、PikPak、Telegram 收藏夹挂载在同一个路径下浏览。
跨存储复制和移动由服务器直接完成，不经过你的设备，有进度、可取消、可重试，中断后按文件续传。

**媒体库**
视频和照片自动成库，有推荐位、最近添加和继续观看。
每个存储可以单独决定要不要进视频库、照片墙和搜索。

**随点随播**
mp4 一类直接播放。mkv、avi、DTS 音轨等在服务端实时转换后播放，能换封装就不重新编码，
进度条全片可拖。云盘上的文件也可以边下边播。

**离线下载**
给一个链接就下载到指定目录，m3u8 会自动合并成 mp4。需要防盗链的站点，填一个 Referer 即可。

**全局搜索**
按名称搜索整棵目录树，后台可随时重建索引。

**多用户**
必须登录才能访问。子账号可以限定可见目录和只读权限。

空闲内存不到 50MB。没有常驻扫描，索引、封面和转码都按需触发。

## 安装

### Linux（推荐）

```bash
curl -fsSL https://raw.githubusercontent.com/J606y/webvid/main/install.sh -o install.sh \
  && sudo bash install.sh install
```

脚本会检测 CPU 架构、安装 ffmpeg、下载对应版本到 `/opt/webvid`，注册服务并开机自启，
最后打印初始管理员账号和访问地址。支持 systemd 与 Docker 两种后端，安装时可选。

装完后直接输入 `webvid` 进入管理菜单，也可以用子命令：

```bash
webvid update            # 升级到最新版（保留数据与文件）
webvid status            # 查看运行状态
webvid log               # 跟随日志
webvid password          # 打印初始管理员密码
webvid reset-password    # 重设管理员密码（省略参数则随机生成）
webvid uninstall         # 卸载（会询问是否删除数据）
```

默认监听 `5243`，数据在 `/opt/webvid/data`，`/opt/webvid/files` 是留给本地存储的空目录。
可用 `WEBVID_PORT`、`WEBVID_DIR` 覆盖。

装好后存储列表是空的，到后台「存储」里添加你要挂的盘 —— 本地目录、OneDrive、Google Drive、
PikPak、Telegram 都在那里。

### Docker

```bash
docker compose up -d --build
docker logs webvid        # 首次启动会在日志里打印随机管理员密码
```

打开 `http://localhost:5243` 登录，到后台「存储」里添加要挂的盘；`./files` 是留给
本地存储的空目录，数据库、封面和转码缓存都在 `./data`。
想固定首启密码，在 `docker-compose.yml` 里取消 `NL_ADMIN_PASSWORD` 的注释（仅建库时生效）。

### 从源码构建

```bash
cd frontend && npm ci && npm run build && cd ..   # 前端产物会嵌入二进制
go build -o webvid .
NL_ADMIN_PASSWORD=admin123 ./webvid
```

需要 ffmpeg 和 ffprobe 在 PATH 上，或用 `NL_FFMPEG` / `NL_FFPROBE` 指定路径。
缺少时程序照常运行，只是视频封面和转码播放不可用。

## 挂载存储

后台管理 → 存储 → 添加，选择驱动后填写表单。

| 驱动 | 必填 | 说明 |
|---|---|---|
| 本地目录 | 根目录路径 | 宿主机绝对路径；容器内填容器里的路径，如 `/files/media` |
| OneDrive | 客户端 ID、客户端密钥、刷新令牌 | 个人或企业账号。刷新令牌权限需含 Files.ReadWrite.All 与 offline_access，轮换后自动回写 |
| OneDrive（应用授权） | 租户 ID、客户端 ID、客户端密钥、用户邮箱 | 用应用专用凭据访问，需在 Azure 门户注册应用并授予 Sites/Files 应用权限 |
| Google 云端硬盘 | 客户端 ID、客户端密钥 | 填好后在该行点「授权」按钮，在 Google 页面同意即可，令牌自动写回。OAuth 应用需发布为「生产」，否则令牌约七天过期 |
| PikPak | 用户名、密码 | 自动登录并维护令牌。风控严格时可能要求人机验证，稍后重试 |
| Telegram | API ID、API Hash、手机号 | **只读**。把消息转发到本人收藏夹后在这里平铺读取。凭据在 my.telegram.org 申请，添加后点该行钥匙按钮用验证码登录，会话自动保存。常配合转存功能搬到可写的存储 |

远端存储的通用选项：

- **代理模式**：下载和播放经服务器中转。关闭时返回直链，服务器不消耗流量
- **并发连接数 / 分块大小**：代理模式下从云盘取数据的并发度，默认 4 线程、4MB 分块
- **展示开关**：该存储的内容是否进入视频库、照片墙和搜索

保存后会自动重建索引，随后在后台开始预载封面与视频信息，之后浏览媒体库即刻出图、
打开视频不必现场探测。进度和手动重跑入口在后台「索引管理」。

## 播放

播放方式由文件本身决定，无需设置：

- mp4、webm 一类直接播放原文件，不启动转码
- 编码能播但容器不认的（例如 mkv 里的 H.264 + AAC）只更换封装，几乎不占 CPU
- H.265、WMV 等需要重新编码；如果只有音轨不兼容，则仅转换音轨

转码分片缓存在 `data/transcode/`，会话闲置五分钟自动回收，同时最多两路，重启清空。
软解 1080p H.265 大约需要四核 CPU。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| NL_PORT | 5243 | 监听端口 |
| NL_DATA_DIR | ./data（镜像内 /data） | 数据库与缓存目录 |
| NL_FILES_DIR | ./files（镜像内 /files） | 留给本地存储的目录，需在后台自行添加挂载 |
| NL_ADMIN_USER / NL_ADMIN_PASSWORD | admin / 随机 | 仅首次建库时生效 |
| NL_FFMPEG / NL_FFPROBE | 自动探测 | ffmpeg 与 ffprobe 的路径 |
| NL_TRUSTED_PROXIES | 回环与内网网段 | 逗号分隔的 CIDR，声明可信的反向代理来源 |

## 部署与安全

公网部署前请确认以下三点。

**`data/newlist.db` 是最高机密文件。** 里面存着各存储的密钥与刷新令牌、所有用户的密码哈希，
以及自动生成的 JWT 密钥。该文件与整个 `data/` 目录不能提交进代码仓库、上传到公开位置或明文分享。
`.gitignore` 与 `.dockerignore` 已排除 `data/`，但这只防误提交，不代表磁盘上是安全的。
建议收紧目录权限（如 `chmod 700 data`），备份时先加密。

**本服务不内置 TLS。** 只提供 HTTP，不做证书申请与续期。公网访问必须放在 Nginx、Caddy、
Traefik 一类反向代理之后，由反代终止 TLS 后转发，不要把端口直接暴露到公网。

**反代之后要让服务拿到真实客户端 IP。** 否则所有请求看起来都来自反代自身，
登录限流会把不同来源误判成同一个人。反代需设置 `X-Forwarded-For` 或 `X-Real-IP`，
并把反代的出口地址或网段写进 `NL_TRUSTED_PROXIES`——只有来自该列表的连接，
其转发头才会被采信，其他来源即使伪造也不生效。

## 忘记密码

```bash
webvid reset-password            # 随机生成新密码并打印
webvid reset-password 你的新密码   # 指定新密码
```

只改密码，不动存储配置和其他用户。源码运行时用 `./webvid reset-password` 同样有效。

## 许可证

AGPL-3.0。自用项目；闭源分发或对外提供网络服务须遵循 AGPL 的开源义务。
