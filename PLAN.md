# NewList — 网盘挂载 + 液态玻璃媒体库 + 转码播放 + 多线程加速（Docker，自用，轻量）

## Context

在空目录 `E:\桌面\newlist` 从零构建一个类 AList/OpenList 的文件列表程序，带 Apple TV 式媒体库、液态玻璃界面。**自用定位：必须登录才能访问任何内容（无游客、无自助注册），管理员可在后台添加子用户；全程注重轻量占用。** 用户确认的范围：

- **网盘挂载**（AList 式驱动框架）：v1 实现 **本地磁盘、OneDrive、OneDrive APP、PikPak**，框架可扩展
- **跨存储转存**：服务器端把文件从 PikPak 直接移动/复制到 OneDrive（拉流→边下边传），后台任务带进度、可取消
- **多线程加速**：云盘直链单连接限速时，服务器用 **N 个并发 Range 分块 + 滑动窗口按序输出** 聚合带宽提升播放/转存网速（每存储可配线程数与分块大小）
- **界面双模式 + 液态玻璃主题**：首页 = Apple TV 式媒体库（海报墙、横向卡片架、**图片/视频分区展示**）；另有传统文件管理页
- **冷门视频格式播放**：mkv/avi/wmv/flv/rmvb/ts、DTS/AC3 音轨等用 **ffmpeg 转码/免转封装成 HLS**（Jellyfin 思路），能直播的直播
- **多用户**：admin 全权；子用户可设 根路径限制(base_path) + 是否允许写(can_write)、启用/停用
- **缩略图**：云盘自带（OneDrive/PikPak 都有）；本地图片缩放、本地视频 ffmpeg 截帧，缓存 data 目录
- **搜索**：全局文件名搜索（SQLite 索引），结果按用户 base_path 过滤
- Docker 部署，端口 5244

技术栈：Go (Gin) + Vue 3 (Vite + Element Plus 按需导入 + 自定义液态玻璃 CSS，JavaScript)，go:embed 单二进制；SQLite（`modernc.org/sqlite` 纯 Go，CGO=0）；ffmpeg/ffprobe 外部进程。

### 轻量占用设计（贯穿实现）

- 空闲内存目标 <50MB：无常驻扫描线程，索引/缩略图/转码全部按需触发
- 一切流式：io.Copy/ServeContent，禁 io.ReadAll；上传/转存/代理内存恒定
- 加速窗口有界：默认 4 线程 × 4MB，护栏 ≤64MB/会话；转码并发 ≤2、空闲 5min 回收；缩略图生成并发 ≤2
- 播放能 302 直链就 302（服务器零 CPU 零流量），能 remux 就不转码
- 前端：路由级懒加载 + Element Plus unplugin 按需自动导入；gin release 模式

## 后端架构

依赖：gin、golang-jwt/v5、x/crypto(bcrypt)、modernc.org/sqlite、disintegration/imaging + x/image、google/uuid。**Go ≥ 1.25**（`os.Root` 防穿越）。

```
newlist/
├── main.go                          # env(NL_PORT/NL_DATA_DIR/NL_FILES_DIR/NL_ADMIN_*) → db → server
├── public/public.go                 # //go:embed all:dist（提交占位 index.html）
├── internal/
│   ├── db/db.go                     # SQLite：settings KV、users、storages、files 索引 建表
│   ├── conf/…                       # 设置读写：site_title/jwt_secret 等
│   ├── auth/…                       # bcrypt、JWT HS256(48h, claims={sub:uid,role})、登录限流(5次锁5分钟)
│   ├── user/user.go                 # 用户 CRUD：role(admin|user)、base_path、can_write、enabled
│   ├── driver/
│   │   ├── driver.go / registry.go  # 驱动接口+能力 / 注册表+配置 schema（前端动态表单，同 AList）
│   │   ├── local/                   # os.Root；Link 返回本地文件句柄
│   │   ├── onedrive/                # Graph：refresh_token+区域(global/cn)、list 分页、Link=downloadUrl(302,Range)、
│   │   │                            #   Thumb、uploadSession 分块上传、增删改移
│   │   ├── onedrive_app/            # 同上但 client_credentials(tenant_id+client_id+secret+目标用户email)
│   │   └── pikpak/                  # 非官方 API：signin+captcha(md5 salt 链)、list、Link、thumbnail_link、
│   │                                #   改名/移动/删除；上传 v1 不做（can_upload=false）
│   ├── fs/
│   │   ├── fs.go                    # 虚拟挂载树：mount_path→驱动；路径解析、根合并；**base_path 前缀强制**
│   │   └── transfer.go              # 跨存储转存：目录递归→逐文件任务；源拉流(可走加速)→io.Copy→目标 Put；
│   │                                #   move=copy 成功删源；CountingReader 报进度
│   ├── stream/accel.go              # **多线程加速读**：NewMultiReader(urlProvider,size,offset,{threads,chunk})→
│   │                                #   K 个 worker 并发取 Range 分块，滑动窗口按序输出 io.ReadCloser；
│   │                                #   直链 403/过期时经 urlProvider 重取；内存=threads×chunk 有界
│   ├── task/task.go                 # 任务管理器：worker pool(2)、状态机、进度/速度、取消、owner（v1 内存态）
│   ├── thumb/thumb.go               # 云盘 302 / 本地图片 imaging / 本地视频 ffmpeg 截帧；缓存 data/thumbs/
│   ├── media/probe.go               # ffprobe 探测(结果按 path+mtime 缓存 db)→ direct|remux|transcode 三档决策
│   ├── media/hls.go                 # 转码会话：ffmpeg 产 HLS(fMP4) 到 data/transcode/<id>/；seek 越界带 -ss 重启；
│   │                                #   空闲 5min 杀进程清目录；并发≤2；云盘源输入=本地回环 raw URL（复用加速与鉴权）
│   ├── index/index.go               # 索引：BFS 全存储写 files 表(ext_type)、进度、写操作后同步 upsert；
│   │                                #   搜索=LIKE lower(name)，结果过滤 base_path
│   └── server/                      # router / middleware(Authed,CanWrite,AdminOnly) / resp / static(SPA)
│       └── handler_{auth,fs,raw,thumb,video,search,media,storage,task,user,admin}.go
├── frontend/ …
├── Dockerfile  docker-compose.yml  .dockerignore  .gitignore  README.md
```

### API（统一 `{code,message,data}`；path=虚拟逻辑路径 `/onedrive/电影/a.mkv`）

**权限中间件**：`Authed`=有效 token（任何用户，附加 base_path 视野过滤）挂所有内容端点；`CanWrite`=Authed 且 (admin 或 can_write)；`AdminOnly`。**除 login/public/ping 外一律要登录**。

| 端点 | 权限 | 说明 |
|---|---|---|
| POST `/api/auth/login`、GET `/api/auth/me` | 公开/Authed | JWT；限流 429；me 返回角色/权限供前端渲染 |
| GET `/api/public/settings`、`/api/ping` | 公开 | 仅 site_title/version；健康检查 |
| GET `/api/fs/get`、`/api/fs/list` | Authed | list 含目录能力(can_write/can_upload)；根=挂载点合并且按 base_path 过滤 |
| GET/HEAD `/api/raw/*path` | Authed | 本地→ServeContent(Range)；云盘→302 直链；存储开**代理模式**时服务器中转，开**多线程加速**时走 stream/accel（透传客户端 Range，从 offset 起分块）；`?dl=1`；接受 `?token=` |
| GET `/api/thumb/*path?size=` | Authed | 云盘 302 / 本地生成缓存 / 404→前端占位 |
| GET `/api/video/info?path=` | Authed | ffprobe→ `{strategy, container, vcodec, acodec, duration}` |
| GET `/api/video/hls/*path/index.m3u8?start=&token=` | Authed | 创建/复用转码会话；分片同前缀 `seg_N.m4s` |
| PUT `/api/fs/upload`、POST `/api/fs/{mkdir,rename,remove}` | CanWrite | 流式上传；按驱动能力拒绝 |
| POST `/api/fs/{copy,move}` | CanWrite | 同存储→同步；**跨存储→后台转存任务**（如 PikPak→OneDrive）；目标不可写→明确报错 |
| GET `/api/tasks`、POST `…/{id}/cancel`、`…/retry`、DELETE `…/done` | Authed | 普通用户仅见自己的任务，admin 见全部 |
| GET `/api/fs/search?q=&type=` | Authed | 索引查询+base_path 过滤；type=video/image |
| GET `/api/media/list?kind=&sort=&limit=`、`/api/media/groups?kind=` | Authed | 媒体库数据（源=索引，base_path 过滤）：最近添加、目录聚合(数量+封面) |
| CRUD `/api/admin/users` | AdminOnly | **子用户管理**：建号/改密/启停/base_path/can_write；不可删最后一个 admin |
| CRUD `/api/admin/storages`、GET `/api/admin/drivers`、POST `…/{id}/reload` | AdminOnly | 存储管理（含 代理模式/加速线程数/分块大小 通用字段）；回显脱敏 token |
| POST `/api/admin/index/rebuild`、GET `…/index/progress` | AdminOnly | 重建索引/进度；存储增删自动触发 |
| GET/PUT `/api/admin/settings`、PUT `/api/user/password` | Admin/Authed | 站点设置；任何用户可改自己密码 |

**路径安全**：入口统一归一化（拒 `\`/NUL，POSIX `path.Clean`）→ **再拼 base_path 前缀校验** → 本地驱动全走 `os.Root`（内核级防穿越+符号链接逃逸）。
**转码策略**：direct = mp4/webm 且 h264/vp9/av1 + aac/mp3/opus/flac；remux = 编码可播容器不认（mkv 里的 h264+aac，`-c copy` 秒开零 CPU）；transcode = 其余（h265/wmv/rv40/vc1…或 DTS/AC3 音轨——视频尽量 copy 只转音频，否则 libx264 veryfast）。
**多线程加速**：仅对"代理模式+线程数>1"的云盘存储生效；本地文件与 302 直链不经过它。播放、转码输入、转存源拉流三处共用 stream/accel。
**转存**：源直链文件级重试 2 次；OneDrive uploadSession 10MB 块级重试；大小取自源 stat。

### 认证与首启

users 表 + settings 表。首启：创建 admin（用户名 `NL_ADMIN_USER` 默认 admin；密码 `NL_ADMIN_PASSWORD` 否则随机 12 位**打印 stdout**）；jwt_secret 随机生成。忘记密码删 `data/newlist.db` 重启（README 写明）。本地磁盘自动挂载 `/本地存储`（NL_FILES_DIR）。

## 前端设计（双模式 + 液态玻璃）

### 液态玻璃视觉（assets/glass.css 设计令牌，全站统一）

深色基底 `#0a0a12` + 大面积柔和极光渐变斑（blur(120px) 彩色 radial，fixed）；玻璃面板 = `rgba(255,255,255,.07)` + `backdrop-filter: blur(28px) saturate(1.7)` + 1px 内描边 `rgba(255,255,255,.14)` + 顶部镜面高光 + 大圆角（卡片 16px/面板 24px/胶囊按钮）+ 柔和投影；悬停 `scale(1.05)`+边缘辉光；悬浮玻璃 Dock 导航；对话框/抽屉玻璃化；Element Plus 用 CSS 变量覆写融入；`@supports` 降级半透明实色。

### 路由（未登录访问任何页 → 重定向 /login）

| 路径 | 页面 |
|---|---|
| `/login` | 登录（玻璃卡片）——唯一公开页 |
| `/` | **MediaHome**：Hero 轮播（最近视频封面）+「视频」区（最近添加横向架 + 目录合集架）+「图片」区（最近图片架 + 相册架），视频/图片分区展示 |
| `/library/video?dir=`、`/library/photos?dir=` | 视频海报墙 / 图片墙(justified)+PhotoSwipe 灯箱 |
| `/play/:path(.*)*` | 播放页：`/api/video/info` → direct 用 ArtPlayer 直连 raw；remux/transcode 用 ArtPlayer+**hls.js**；越界 seek 自动带 start 重开；玻璃控制栏 |
| `/files/:path(.*)*` | 文件管理：面包屑+列表/网格+排序过滤+上传(拖拽/队列/进度)+重命名/移动/复制/删除；移动/复制目标可跨存储→创建任务；TopBar 任务图标→TasksDrawer（轮询进度/速度/取消/重试）；文件按类型预览（图→灯箱、视频→/play、md/代码/pdf→预览组件）；按能力+can_write 显隐写按钮 |
| `/search?q=&type=` | 全局搜索：视频/图片/全部过滤 |
| `/@admin` | 管理后台（Element Plus）：站点设置 / 存储管理(驱动 schema 动态表单) / **用户管理(子用户 CRUD/权限)** / 索引管理 / 改密码 |

- 海报=`/api/thumb`，失败回退占位图；IntersectionObserver 懒加载；路由组件全部动态 import
- Pinia：auth（token/用户信息/权限）+ app（设置/视图偏好）；axios 注入 Bearer、401→/login
- 依赖：vue/router/pinia/axios/element-plus(+unplugin 按需)/artplayer/hls.js/photoswipe/marked/dompurify/highlight.js
- Vite proxy `/api`→5244；`build.outDir='../public/dist'`

## Docker

三阶段：node:22-alpine 前端 → golang:1.25-alpine（CGO=0，-s -w）→ **alpine + apk add ffmpeg** + ca-certificates + tzdata（约 120–160MB）。compose：`./data:/data`、`./files:/files`、5244:5244、healthcheck `/api/ping`、`restart: unless-stopped`；启动清理残留转码目录。README 注明内存/CPU 预期：空闲 <50MB，转码时看片源。

## 实施里程碑（每步可独立验证）

| # | 内容 | 验证 |
|---|---|---|
| **M0 环境** | 检查/安装 Go≥1.25、Node LTS、ffmpeg（winget）；Docker Desktop 留最后（Glob 显示常规路径均未装） | 三个 version 命令 |
| **M1 骨架+DB+多用户认证** | db 建表(users/settings/storages/files)、首启初始化、JWT、限流、login/me、用户 CRUD API、占位前端 | 首启打印随机密码；登录/401/429；建子用户(base_path=/onedrive/共享, can_write=false)能登录；无 token 访问任何内容接口 401 |
| **M2 驱动框架+本地驱动** | driver 接口/registry/schema、fs 挂载树+base_path 强制、local(os.Root)、fs/get/list、raw、storages CRUD | 中文文件名 list 正确；Range 206；穿越攻击全拒；子用户看不到 base_path 外的挂载点 |
| **M3 OneDrive 双驱动** | onedrive + onedrive_app：token 刷新、list 分页、Link 302、Thumb、uploadSession、增删改移 | 真实账号：浏览/下载/拖动/上传大文件/重命名；token 自动刷新 |
| **M4 PikPak 驱动** | signin+captcha 签名、list、Link、thumbnail_link、改名/移动/删除；参考 AList/OpenList **接口行为**自写（不复制 AGPL 代码） | 真实账号：浏览/直链播放/缩略图；PikPak 上传被拒且按钮隐藏 |
| **M5 任务+跨存储转存** | task 管理器、fs/transfer、copy/move 分流、tasks API | **PikPak→OneDrive 单文件+目录转存**：进度/速度实时、内存恒定、move 后源删；取消立即停；→PikPak 报"不支持写" |
| **M6 多线程加速** | stream/accel（K 线程 Range 分块+滑动窗口+直链重取+客户端 Range 透传）、存储通用字段(代理/线程/分块)、raw 接入、转存源可选走加速 | 同一 PikPak 大文件：单线程 vs 4 线程 curl 限时下载对比提速明显；拖动播放（中段 Range 起播）正确；观察进程内存稳定在窗口大小级别 |
| **M7 前端骨架+文件管理+玻璃主题** | glass.css、登录、路由守卫（未登录全拦）、/files 全功能（含跨存储移动复制+TasksDrawer、预览组件）、/@admin 四个 tab（含**用户管理**） | 液态玻璃观感（模糊/描边/高光/悬停）；全流程手测含子用户视角（只读、看不到管理入口、base_path 外 404）；无 token 一切页面跳登录 |
| **M8 缩略图** | 云盘 302 / 本地 imaging / 本地视频 ffmpeg 截帧、缓存+并发限 2；网格接缩略图 | 本地视频网格真实截帧；二次访问走缓存；云盘海报来自云端 |
| **M9 索引+搜索** | index 构建器（BFS/进度/自动触发/写后同步）、search API、/search 页、admin 索引管理 | 中英文子串跨存储命中；type 过滤；子用户搜索结果不越 base_path；转存后新位置可搜到 |
| **M10 转码播放** | media/probe(三档决策+缓存)、media/hls(会话/seek/回收/并发2/云盘输入走本地回环 raw)、video API、/play 页 | mkv(h265)/avi/wmv/flv/rmvb/DTS 样片逐个能播能拖；mkv(h264+aac) remux 秒开 CPU 低；mp4 direct 不起 ffmpeg；空闲 5min 回收；PikPak 上的 mkv 开加速后转码流畅 |
| **M11 Apple TV 媒体库** | media API(list/groups)、MediaHome(hero+视频架+图片架+合集)、/library 海报墙、/photos 灯箱 | 首页视频/图片分区、悬停放大、横滚流畅；点视频进播放页；灯箱切换 |
| **M12 单二进制+Docker** | embed、SPA fallback+缓存头、Dockerfile(ffmpeg)、compose、README（挂载教程/token 获取/忘记密码/转码与加速说明/许可证） | 仅跑 exe 全功能、深层 F5 不 404；`docker compose up --build` 全流程复测；**docker stats 空闲 <50MB**；restart 后配置保留 |

## 关键风险与对策

1. **转码 CPU**：软转 1080p h265 约需 4 核——自用可接受；remux 优先、并发≤2、空闲回收；README 注明
2. **多线程加速被风控**：部分云盘对并发 Range 敏感——线程数每存储可调（默认 4，可关）；PikPak 直链 OSS 系一般支持；加速层失败自动降级单连接
3. **PikPak 非官方 API**：captcha salt 可能随版本失效——salt 集中常量、错误信息明确、README 注明
4. **OneDrive token 门槛**：README 分步教程；兼容 AList 已有 refresh_token 直接粘贴
5. **直链时效**：downloadUrl ~1h 过期——不缓存直链；加速层/转码中途 403 经 urlProvider 重取 Link
6. **大文件/长连接**：http.Server 只设 ReadHeaderTimeout+IdleTimeout；全链路流式禁 io.ReadAll
7. **Windows 文件名/路径**：本地驱动拒非法字符、NTFS 保留名、结尾点/空格；API 层 POSIX path
8. **XSS**：Markdown 过 DOMPurify；URL 不带长期 token 外泄（分享场景 v1 不做）
9. **索引一致性**：写操作/转存后同步 upsert/delete
10. **子用户越权**：base_path 在 fs 层单点强制（list/get/raw/thumb/video/search/media 全走同一解析函数）；写权限=CanWrite 中间件；管理端点 AdminOnly
11. **ffmpeg 缺失容错**：探测不到→缩略图/转码降级、进程不崩
12. **任务内存态**：重启丢失，可重试重建（README 注明）
13. **许可证**：不复制 AList/OpenList(AGPL) 代码，按公开 API 行为自行实现

## 端到端验证（最终验收）

`docker compose up --build` → logs 拿首启密码 → 登录（未登录任何页面/接口都进不去）→ 添加三个云盘存储（真实账号）→ 文件页浏览/下载/上传 → **PikPak 文件"移动到 OneDrive"任务进度→完成后两边状态正确** → 某 PikPak 存储开 4 线程加速后播放/下载明显提速 → 重建索引 → 搜索跨存储命中 → 建子用户（只读+base_path）用无痕窗口登录验证视野与权限 → 首页媒体库液态玻璃观感、视频/图片分区海报墙 → **mkv/avi/rmvb/DTS 样片容器内转码播放可拖动**、mp4 直播不耗 CPU → docker stats 空闲 <50MB → restart 后配置保留。安全回归：穿越攻击集全拒、无 token 全 401、子用户越权探测（访问 base_path 外路径/管理接口/写接口）全拒。
