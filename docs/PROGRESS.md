# 进度存档（2026-07-08 第十七次更新）

## 当前阶段
**Demo 已验收 → 后续里程碑推进中。
M3 OneDrive + M4 PikPak 驱动完成；OneDrive 已有真实账号在库（用户自挂 /test，onedrive_app），
**M3 真实联调收尾✅（2026-07-08）**：小文件上传/mkdir/rename/copy/move/缩略图/302 校验/删除
19 项全过（frontend/od-tail-check.mjs 留库），并给 token 获取加了瞬时错误重试退避（见 M3 段）。
M5 任务+跨存储转存：✅ 完成。
M6 多线程加速：✅ 2026-07-06 完成。
M10 转码播放：✅ 2026-07-07 完成（单测+6 格式样片浏览器实测+真实 OneDrive 云端 HLS 实测全过，见 M10 段）。
M12 Docker：✅ 2026-07-07 完成（本机 Docker Desktop 实测：构建/转码/空闲 4.7MiB/重启持久化全过，见 M12 段）。
**全部计划里程碑完成**，剩 M3/M4 真实账号联调尾项（用户依赖）与按需迭代。**
用户反馈 #1~#29 全部完成并已部署，明细见「UI 迭代记录」
（#21 媒体预载：挂载勾选展示开关后自动后台下载封面+探测视频源信息持久化；
#22 移动端 UI 适配：底部 Tab 栏+全站响应式，回归 frontend/mobile-check.mjs 37 项；
#23 修 .ts 无法播放：mpegts 的 ADTS AAC copy 进 fMP4 须挂 aac_adtstoasc bsf，见 M10 踩坑；
#24 最近播放「查看更多」50 条视图；#25 断点续播：play_history 加 position/duration，
播放定时上报+重进续播+货架/详情卡进度条，回归 frontend/progress-check.mjs 15 项）。
当前线上：三开关全 true、索引完整（45982 条，含 6 个转码样片）。
规划见 PLAN.md；逐文件细则见 docs/TASKS.md。

## 存档索引

历史明细已归档到 `docs/progress/`（降低单文件并行编辑「标题行互吃」的事故面）：
- [里程碑实现存档](progress/milestones.md) — M3/M4/M5/M6/M10/M12 实现细节与实测。
- [UI 迭代 / 用户反馈存档](progress/ui-feedback.md) — 用户反馈 #1~#52 迭代与踩坑明细。

技术债务整改进度另见 `docs/REFACTOR-PROGRESS.md`。

**转存链路提速与限流可靠性（2026-07-25）见 `docs/TRANSFER-RELIABILITY.md`**：
断点续传探测从每文件一次 Stat 改为每目录一次 List、googledrive/pikpak 路径解析加负缓存、
限流退避改对（上传路径原本完全没有退避）+ 错误能定位到文件与源/目标侧。
内含实测结论：**Google Drive 每天 750GB 上传额度用尽时返回的是 `userRateLimitExceeded`，
与并发过高的瞬时限流同码**，官方文档未记载；两个并发设置是相乘的。

**上线后打磨（2026-07-25）另见 `docs/POLISH-PROGRESS.md`**：
A. 报错人话化 + Apple 风文案（`util.Humanize` + 三处异步任务 + 驱动前缀 +
`aadMessage`/`oauthMessage` + ffmpeg stderr 收敛）与 B. UX 清单 28 项（26 修 +
#19 不修 + #20 改后台开关）均已完成，**随 v2.0.0 发布**。
C. 预载「不是现在」（2026-07-26）：后台「封面与源信息预载」卡片新增推迟按钮，
点后停下预载、一天后自动继续，期间自动预载跳过、跨重启有效，「继续」可提前恢复
并接着剩余清单跑。前后端 + 单测 + e2e（`frontend/preload-snooze-check.mjs` 11/11）
均通过、二进制已重嵌，**随 v2.1.0 发布**。
D. 后台「传输任务」（2026-07-26）：后台新增传输任务 Tab（全站视野），比顶栏抽屉多一层——
点「查看文件」看文件夹内逐文件状态（等待/传输中/已完成/已跳过/失败 + 搜索 + 筛选 + 分页）。
任务层为此建了文件级进度模型，`fs.Progress` 接口改按下标上报。
e2e `frontend/admin-tasks-check.mjs` 15/15，**随 v2.1.0 发布**。
E. 离线下载可选 Referer（2026-07-27）：弹窗新增可选 Referer，直链请求与 ffmpeg 拉 HLS
分片两条路径都带上，防盗链站点（校验 Referer 非 IP）可下；403 且未填时明确提示填 Referer。
单测 `internal/server/offline_referer_test.go` + `offline-check.mjs` 18/0/3，**随 v2.1.2 发布**。
F. 索引管理两个删除动作（2026-07-28）：后台「文件索引」加「删除索引」（清空 files 表，
文件不动；重建进行中拒绝，409），「封面与源信息预载」加「删除缓存」（先停下预载，
再删 data/thumbs 全部封面文件 + 清空 media_info，进度归零、推迟状态保留）。
两者互不联动。顺带修了索引条数只存内存导致「重启后显示共 0 项」——`index.New` 启动回填真实行数；
并给预载计数加代际判定（`process(ctx, gen, …)`），免得删完缓存后在途几项把计数加回去。
单测 `internal/index/index_test.go`（新）+ `preload_test.go` 的 `TestClearRemovesCache`，
e2e `frontend/index-clear-check.mjs` 12/12、零控制台错误，二进制已重嵌。
e2e 里两个 clear 接口有意打桩——打真后端会把实例的封面缓存真删掉、重扫云盘代价太大；
路由是否注册改用未鉴权 POST 验（返 401 而非 404）。
G. 传输任务文件清单记住翻页（2026-07-28）：`openFiles()` 原本每次打开都把页码、搜索、
筛选清零，翻到第 3 页看完一个文件、关掉再打开又得从头翻。改为按任务 id 记住这三项，
重开同一任务回到离开时的样子，换任务从头开始，任务记录被删时一并清掉。
记住的页码若已越界（筛选换窄、任务重试后清单变短）按 total 回落到最后一页，不停在空白页上。
e2e `frontend/tasks-page-memory-check.mjs` 9/9——清单要 100 条以上才翻得动页，
跑真实转存造这么多太重，这里把任务与清单打桩（真实链路仍由 `admin-tasks-check.mjs` 覆盖）。
H. 锁屏与灵动岛显示片名封面（2026-07-28）：iPhone 上播放时灵动岛常驻一颗「正在播放」胶囊，
但里面是空的。查清了机制——WebKit 只要见到带音轨且在播的 video 就自建系统会话
（WebCore `MediaElementSession` 的 NowPlaying 判定），**网页没有任何开关能关掉它**，
连原生 App 都压不掉别人的灵动岛活动（Apple 开发者论坛 thread/804164）。
胶囊消不掉，能做的是让它显示得体：新增 `frontend/src/utils/mediaSession.js` 接管
Media Session API——片名（文件名去扩展名）、所在目录当专辑、480 档缩略图作封面、
播放态与进度同步，并接管播放/暂停/快退/快进/拖动五个系统控件；`teardown()` 里摘除，
否则离页后灵动岛挂着一个已经不在播的视频。封面刻意取 480 档：列表卡片用的就是这档、
多半已在缓存里命中，不必为锁屏再向云盘拉一次数据抽帧。
`setPositionState` 的入参先自校验（HLS event 列表边转边播时 duration 是 Infinity，直接交会抛）。
e2e `frontend/media-session-check.mjs` 18/18——这套 API 只有 setter 读不回来，
脚本在页面里挂探针记录真正交给系统的入参；跑在临时端口+临时数据目录的隔离实例上
（本仓库 `_e2e_*` 会被并行会话清掉）。真机呈现效果待用户在 iPhone 上确认。

## 如何启动预览
```
cd E:\桌面\newlist
NL_ADMIN_PASSWORD=admin123 ./webvid.exe     # 或 go run .
# 浏览器打开 http://localhost:5244
# 管理员 admin / admin123（仅本地演示密码；首启已写入 DB，之后 env 不再生效；公网部署务必换强密码）
# 演示子用户 guest / guest123（仅本地演示账号；视野限定 /本地存储/图片，只读）
```
改前端后重建：`cd frontend && npm run build`，再 `go build -o webvid.exe .`（嵌入 dist）。

## 已完成（全部编译通过 + curl/浏览器冒烟通过）
- 后端全部：model/db/conf/auth/user/driver/local/fs/thumb/index/server(13 个 handler)/public/main
- 前端全部：Vite+Vue3+JS、Element Plus 按需、液态玻璃 glass.css(极光背景+玻璃面板)、
  8 页面（Login/MediaHome/LibraryVideo/LibraryPhotos/Play/Files/Search/Admin）+
  组件（MediaShelf/UploadDrawer/MoveCopyDialog/NameDialog/TextDrawer/ChangePasswordDialog）
- 样例数据 ./files：电影 3 部/剧集 2 集(testsrc2 mp4)、风景 5+壁纸 3(渐变图)、说明.md/示例代码.py、测试音频.mp3
- 验证记录：
  - curl：登录/限流 429/me、list 中文路径、raw 206 Range、穿越集全拦(400/404)、
    mkdir 重复 409/CON 400、upload 冲突 409、rename/copy/move/remove、
    子用户视野隔离(list 空/404/403)、搜索(转义 %_、base_path 过滤)、media list/groups、
    thumb(图片 imaging/视频 ffmpeg 截帧/缓存头)、video/info 两档、SPA 深链 200、/api 404 JSON
  - 浏览器（playwright-core+本机 Chrome，frontend/e2e-check.mjs）：12 张截图在 _shots/，
    登录→首页 Hero/媒体架→视频库→照片墙+灯箱→播放(ArtPlayer 自动播放)→文件管理→
    md 抽屉渲染→搜索→后台 4 tab，控制台零错误

## 已知限制（见 TASKS.md 后续里程碑）
- 云盘真实账号联调（OneDrive/PikPak 代码已完成）、多线程加速（M6）、
  非常规格式转码（M10，video/info 返回 unsupported 档）、Docker（M12）

## 踩坑记录（供后续会话参考）
- git-bash 下 curl 测中文 JSON body 会被 argv 转码搞坏：body 一律写 UTF-8 临时文件 `-d @file`，
  且 `MSYS2_ARG_CONV_EXCL="*"` 时 @file 必须用 Windows 绝对路径
- git-bash 会把 `path=/` 参数改写成 Windows 路径（MSYS 路径转换），测试要么预编码 URL 要么禁转换
- `Bash 后台 &` 起的进程会脱管；要用 run_in_background 任务方式起 webvid.exe
- 前端打包：`import * as icons from '@element-plus/icons-vue'` 和全量 highlight.js 会把 chunk 撑到 1MB+，
  已改 utils/icons.js 按需映射 + highlight.js/lib/common
- `.glass` 的 backdrop-filter 会成为 fixed 定位的包含块：el-dialog/el-drawer 若写在 glass 容器内
  会被面板裁剪（后台弹窗曾只露一条）。修复=加 `append-to-body`；新增弹窗一律放 glass 容器外或加该属性。
  e2e-check.mjs 已加 09b/10b 两张弹窗回归截图
- **keep-alive 驻留列表页的三个连锁坑**（反馈#19，改动 App.vue keep-alive include 时必看）：
  ①驻留期路由切走 route.query/params 变空→依赖它的 computed 会误触发 watch 重置列表；
  用带 prev 的 computed 仅在本页 path 时读新值否则冻结旧值。②body 全局类（infuse-mode）
  要从 onMounted/onUnmounted 挪到 onActivated/onDeactivated 否则切页残留。③append-to-body 的
  teleport 弹窗后退带走后遮罩残留，onDeactivated 触发太晚关不掉，须用 onBeforeRouteLeave 收起。
  滚动位置本身靠 router scrollBehavior 返回 savedPosition 恢复
- **`npm run build 2>&1 | tail -N && go build ...` 会吞掉构建失败**：管道退出码取 tail 的 0，
  vite 失败也照样 go build 打包**旧 dist** 进 exe（曾部署出旧前端排查半天）。要么不接管道，
  要么查 `${PIPESTATUS[0]}`；部署后可 grep dist 产物特征串确认版本
- OneDrive 缩略图（/api/thumb 302 到 sharepoint transform 服务）偶发 ERR_BLOCKED_BY_ORB /
  翻页中断 ERR_ABORTED，均为云端瞬时/导航良性错误；缩略图整体联调仍在 M3 清单
  → **反馈#13 后基本消除**：远端缩略图改服务端下载落盘，浏览器不再直连 sharepoint，
  ORB/tempauth 波动被隔离在服务端（仅首次下载失败的 302 兜底还会直连一次）

## 下一步（新会话从这里续接）
1. **全部计划里程碑已完成**（M12 Docker ✅ 见上）。剩余均为用户依赖项或按需迭代。
2. **M3 OneDrive 联调✅（2026-07-08 收尾，见 M3 收尾段）**：全部写操作+缩略图+下载校验
   实测通过；token 瞬时错误重试已加。仅剩 token 轮换回写长跑观察（随日常使用自然验证）。
3. **M4 PikPak（用户依赖）**：用户提供账号+密码（或 refresh_token）→ TASKS.md M4 第 9 条
   【真实账号】清单（含 M6 调参 + M10 验收项「PikPak 上的 mkv 开加速后转码流畅」）；
   写操作联调可直接 `OD_ROOT=/pikpak挂载点 node od-tail-check.mjs` 复用（PikPak 无上传，
   上传两步会按预期报错，其余步骤可用）。
4. **转码可选优化（按需）**：多音轨/字幕选择、h265 mp4 的 direct 探测放行（Edge 硬解）、
   转码码率/分辨率档位、会话进度条预热。
5. 常用命令：起服务 `NL_ADMIN_PASSWORD=admin123 ./webvid.exe`（admin123 仅本地开发密码；
   用 run_in_background，`&` 会脱管；若报端口占用先 `taskkill //PID <pid> //F` 杀旧进程，
   注意重建后必须重启才加载新驱动）；
   改前端 `cd frontend && npm run build` 再 `go build -o webvid.exe .`；
   全量回归 `cd frontend && node e2e-check.mjs`（需服务在跑；**必须在 frontend/ 下执行**，
   ../_shots 相对 CWD，在仓库根跑会把截图写到桌面）；
   转码回归 `cd frontend && node hls-check.mjs`（7 格式能播能拖+mp4 不走 HLS，含 .ts ADTS）；
   移动端回归 `cd frontend && node mobile-check.mjs`（390×844 触屏视口 37 项，反馈#22/#29；
   含后台 Tab 头与存储/用户表格无内部横向溢出）；
   续播回归 `cd frontend && node progress-check.mjs`（15 项：seek→离开→重进续播/进度条/详情卡，反馈#25）；
   驱动单测 `go test ./internal/driver/onedrive/ ./internal/driver/pikpak/`；
   本机无 python，解析 JSON 用 `node -e`。
