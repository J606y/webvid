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

## M12 Docker（2026-07-07 完成实现 ✅，本机 Docker Desktop 29.4.3 实测）
- `Dockerfile` 三阶段：node:22-alpine（npm ci + vite build → /src/public/dist）→
  golang:1.26-alpine（CGO=0 -trimpath -s -w；modernc sqlite 纯 Go 无需 gcc）→
  alpine:3.22 + apk ffmpeg(6.1.2)+ca-certificates+tzdata；ENV NL_PORT/DATA_DIR/FILES_DIR；
  HEALTHCHECK wget /api/ping（读运行时 $NL_PORT）；镜像 234MB
- `.dockerignore`：排除 data/files/exe/node_modules/docs/_shots；**public/dist 也排除**——
  镜像内前端产物一律以 web 阶段现build 为准，防本机旧 dist 混入（呼应踩坑「管道吞 vite 失败」）
- `docker-compose.yml`：./data:/data、./files:/files、5244:5244、TZ、
  NL_ADMIN_PASSWORD 注释示例（仅首次建库生效）、restart:unless-stopped；`docker compose config` 合法
- `README.md`（新写）：Docker/二进制两种启动、四驱动挂载教程（OneDrive token 获取要点/
  onedrive_app 四件套/PikPak 账密）、远端通用字段（proxy/threads/chunk_mb/三展示开关）、
  转码与加速原理说明、env 表、忘记密码=删 data/newlist.db、AGPL 说明
- **容器实测（隔离卷 docker-test + 5245 端口，不动真实 data）**：
  首启日志打印管理员账号✓ 自动挂载 /files✓ 索引 3 条✓ ping✓ SPA 深链 /library/video 200✓；
  容器内 ffprobe 探测 avi→transcode、HLS 列表/init/seg_0 正常、mp4 direct✓；
  **docker stats 空闲 CPU 0% / MEM 4.7MiB**（目标 <50MB）；restart 后免密登录数据俱在、
  优雅关闭日志「正在关闭…」、/data/transcode 启动清空✓；测完容器与测试卷已删
- 坑：git-bash 下 `docker exec ls /data` 的路径会被 MSYS 转换成本机 Git 安装目录，
  要 `MSYS2_ARG_CONV_EXCL="*"`；本机 Docker Desktop 平时不常开，实测前 powershell
  Start-Process 启动 + 轮询 docker info 等 daemon 就绪
- 未做（现阶段无必要）：多架构镜像/发布到 registry；Linux 宿主的 uid/gid 映射说明（自用 Windows）

## M10 转码播放（2026-07-07 完成实现 ✅，含真实 OneDrive 云端实测）
- `internal/media/probe.go`（新包）：LookTool（NL_FFMPEG/NL_FFPROBE env → PATH → winget 兜底）；
  runProbe（ffprobe -show_streams -show_format JSON，45s 超时）；decide 三档决策——
  可播视频=h264(仅 8bit 4:2:0，Hi10P 不算)/vp9/av1，可播音频保守只认 aac/mp3
  （opus/flac 进 fMP4 兼容性参差，转 aac 成本可忽略）；跳过 attached_pic 封面流；
  direct 档在 handler 层按扩展名先行判定（mp4/m4v/mov/webm **不起 ffprobe** 秒开）
- `internal/media/hls.go`：Service 会话管理器，**两种会话模式按决策自动选择**：
  - **event 模式**（视频 -c copy：纯 remux 或只转音频→aac）：ffmpeg 全速跑完整文件自写
    EVENT 播放列表（playlist 轮询 ≥1 个 EXTINF 才返回），copy 远快于实时，秒~分钟级出
    ENDLIST 变完整 VOD 全片可拖；不做 -ss 重启（copy 无法对齐关键帧，也不需要）
  - **vod 模式**（视频 libx264 veryfast 重编码）：服务端按探测时长**直接生成完整 VOD 列表**
    （4s 等分），ffmpeg 用 -force_key_frames expr:gte(t,n_forced*4) 对齐分片边界 +
    -output_ts_offset 让时间戳落在绝对时间轴 → hls.js 原生全时间轴可拖；
    分片请求超出进度窗口（cursor+12）或回拖到本轮起点前 → kill+`-ss N*4 -start_number N` 重启；
    已生成分片跨轮次保留（回拖秒回）；**vod 模式音频恒转 aac**（-ss 精确 seek 需解码丢帧，
    copy 的压缩包无法齐点裁切）
  - 通用：分片落 data/transcode/<sha1(路径)[:16]>/，`-hls_flags temp_file` 保证改名即完整；
    会话键=逻辑路径（跨用户共享，权限每请求校验）；并发 ≤2 超出驱逐最久未用；
    空闲 5min janitor 回收（destroy 等进程退出再删目录，Windows 句柄）；启动清 data/transcode；
    探测结果内存缓存（路径+size+mtime，>512 清表）；stderr 尾 2KB 进错误信息
  - **云盘输入走本地回环** /api/raw?token=（SignToken 现签 48h）：鉴权/直链过期重取/代理加速
    全复用 raw 层；本地盘 LocalPather 直接绝对路径
- `internal/server/handler_video.go`：videoInfo 三档（direct 按 directPlayExts 秒回；
  非视频扩展名 unsupported；其余 Decide→hls{reason:remux|transcode,duration}，
  探测失败/ffmpeg 缺失→unsupported 文案降级）；新增 GET /api/video/hls/*path
  （*path=<逻辑路径>/<index.m3u8|init.mp4|seg_N.m4s>，playlist no-store + **injectToken
  把 ?token= 注回相对分片 URI**（Safari 原生 HLS 也能用），分片 private,max-age=3600）
- `Play.vue`：DIRECT_EXTS 收窄为 mp4/webm/mov/m4v（**mkv 移出**，改走 HLS remux）；
  strategy=hls → 动态 import('hls.js')（独立 chunk 525KB 仅转码时加载）+ ArtPlayer
  customType m3u8（Hls.isSupported→attachMedia；否则 video.src 原生）；
  new Hls({startPosition:0})——event 列表默认会追直播沿；标题旁加「转封装/转码」badge；
  HLS 也上报 /media/played
- 测试（全绿，媒体包 -count=3）：probe_test.go 决策表 11 例（remux/DTS·AC3 只转音频/
  h265·Hi10P·wmv3·rv40 全转码/vp9+opus/封面流跳过/纯音频/无时长）；hls_test.go 8 例
  （真 ffmpeg 现场生成样片，无 ffmpeg 自动 Skip）：event remux 会话→ENDLIST/只转音频
  =event 模式/vod 列表 3 分片+越界 404+**init+seg 拼接 ffprobe 验证时长≈4s**/
  **seek 重启 runFrom=14 且拼接 start_time≈56s 证明绝对时间轴**/回拖重启 runFrom=5/
  驱逐+sweep 回收目录/非法分片名/ErrNotFound 透传/**空 init 回归**（见下）；
  server 包 injectToken/segNameRe 单测
- **踩坑（已修+回归测试）**：ffmpeg hls muxer 启动即建 **0 字节 init.mp4 占位**，
  首个分片完成后才一次性写入内容——waitFile 只查存在会把空 init 发给 hls.js
  → 起播失败+控制台 404（avi/wmv/flv 首测全挂，h265 侥幸过是复用了 curl 已跑完的会话）。
  修复：ready()=存在且 size>0（分片有 temp_file 改名保证，init 靠非空判定）
- **踩坑#2（2026-07-08 反馈#23 已修+回归）**：**.ts/.m2ts（mpegts）里的 AAC 是 ADTS 帧**，
  event 模式 `-c:a copy` 进 fMP4 时 ffmpeg 报 Malformed AAC bitstream **首个音频包即死**
  （mkv/mp4 源的 AAC 是 ASC 格式踩不到）。**且 ffmpeg 挂掉时 hls muxer 仍会 finalize 出
  「0.28s 截断片+ENDLIST」的格式合法列表**——播放器拿到列表但只有一瞬纯视频，表现为"无法播放"，
  只等 ENDLIST 的测试分不出成败。修复：Decision 加 AudioAAC（decide 记音频是否 aac），
  ffmpegArgs 在 copy+aac 时恒挂 `-bsf:a aac_adtstoasc`（ADTS 必需，ASC 源过 bsf 直通无害，
  实测 mkv 加挂零影响）；media_info 表加 audio_aac 列，**迁移时 ALTER 成功（=旧库首启）
  顺带 DELETE 清全表缓存**——否则升级前预载/探测过的 .ts 旧行缺标记命中缓存 bug 复发。
  回归：hls_test TestEventSessionRemuxADTS（查 runErr+产物须含 aac 流且时长≈全片，
  摘 bsf 负向验证确实 FAIL）、db_test 迁移双路径（旧库清缓存/新库重开不误清）
- 浏览器实测（frontend/hls-check.mjs 留库，30/30 过+控制台零错误）：
  mkv(h264+aac remux)/mkv(h265)/mkv(h264+DTS 只转音频)/avi(mpeg4)/flv(flv1)/wmv(wmv2)/
  **ts(h264+aac ADTS remux，反馈#23 新增)**
  逐个「起播<数秒、拖到 24s 续播、回拖 1s 续播」；**mp4 direct 0 个 HLS 请求不起 ffmpeg**；
  样片在 files/电影/转码样片/（ffmpeg 生成，rmvb 无编码器造不出——解码器有，真实 rmvb 可播）；
  **新加样片要经 PUT /fs/upload?overwrite=1 入索引**（直接落盘不入索引，/media/played 上报 404
  打破控制台零错误基线）
- **真实 OneDrive 云端 HLS 实测**：本地 9.4MB mkv 跨存储转存 /test（**顺带联调 M3 真实上传
  ≥4MiB uploadSession 分块✅**）→ /video/info 云端探测 2.5s（ffprobe 走回环 302）→
  播放列表 3.7s 就绪 → seg_0 3.2MB 正常 → /fs/remove 删除（**M3 真实删除✅**）
- e2e 全量回归：14 张截图过，唯一错误=基线已知 OneDrive 缩略图 ORB 良性兜底
- 已知限制（v1 设计内）：字幕流不封装（-map 只取 v0/a0，多音轨取第一条）；
  event 模式跑完前拖到未生成处会等待（copy 快，窗口极短）；vod 模式时长未知（探测无 duration）
  退化 event 模式；会话内存态重启即清

## M6 多线程加速（2026-07-06 完成实现 ✅，含真实 OneDrive 实测）
- `internal/stream/accel.go`（新包，纯标准库）：
  - LinkProvider func(ctx)(url,header,err)；NewMultiReader(ctx,provider,offset,length,threads,chunkBytes)
    → io.ReadCloser：worker 领块下载（每块 ≤3 次尝试+backoff+2min 超时子 ctx）、
    滑动窗口=threads（sync.Cond，内存 ≈(threads+1)×chunk）、Read 按序取块；
    206 ReadFull / 200 仅"单块且 offset=0"可接受否则报"不支持 Range" /
    401·403·404·410→判过期强制换链（**换链单飞**：gen 计数，worker 带旧 gen 才真调 provider）/
    其余状态码与断流直接重试；Close 幂等=cancel+Broadcast+wg.Wait
  - serve.go：parseRange（单区间 bytes=a-|a-b|-n；语法错/多区间宽松降级为全量；越界 416）+
    Serve(w,req,name,mod,size,ctype,provider,threads,chunk)——头组装/HEAD 不拉流/
    size<0 单流透传（Range 原样转发、镜像上游状态与长度头）
- `internal/fs`：Mount.accelOpts() 解析 proxy/threads(缺省4钳[1,32])/chunk_mb(缺省4钳[1,64])；
  LinkEx 返回 LinkResult{Link,Info,Accel,Refresh}（原 Link 变薄包装）；
  LinkResult.Provider() 适配 stream.LinkProvider（首链只消费一次，之后 Refresh 重取）
- `internal/server/handler_raw.go`：rawHandler 改用 LinkEx——Local 走 ServeContent 不变；
  URL 且 !proxy → 302 不变；URL 且 proxy → rawProxy（dl=1 头 + stream.Serve）
- `internal/fs/transfer.go` copyOne：lk.URL 且 threads>1 且 size>chunk → MultiReader 拉源
  （countingReader 仍在最外层，进度/文件级重试语义不变），否则原单 GET/Local 句柄
- 前端零改动（proxy/threads/chunk_mb 字段 M4 已进后台动态表单）
- 测试（全绿，全套 -count=3）：
  - stream/accel_test.go 9 用例：全量/offset+length 边界/乱序完成/滑动窗口上限（消费暂停时
    服务端最大起点不越窗）/403 换链（provider 被重调）/断流半块重试/200 不认 Range 报错+单块例外/
    取消立返 Close 不悬挂/单线程起点递增
  - stream/serve_test.go 8 用例：200 全量/bytes=a-/a-b（且不越界拉上游）/-n/416/HEAD 无 body 不拉流/
    多区间降级 200/size 未知透传（上游收到原样 Range、状态镜像）
  - fs/accel_test.go：accelOpts 默认与钳制 5 组；转存 URL 源 3MB×chunk1MB 分块并发内容一致进度相符/
    过期换链后转存成功（驱动 Link 被重调）
  - server/raw_proxy_test.go 全链路：_test 内注册 rangetest 驱动 → 真实 sqlite+fs.Reload+Router →
    登录 → 代理盘 200 全量/206 切片+Content-Range/HEAD；直链盘 302；未登录 401
- **真实 OneDrive 实测（用户自挂 /test，23MB 文件，chunk_mb=10→3 块）**：
  - 临时开 proxy：200 全量与 302 直链下载 sha256 一致；跨块边界 Range(9437184-11534335)
    206 切片一致；HEAD 正确；测完已恢复 proxy=false 并验证 302 回归
  - 跨存储转存 OneDrive→本地：23MB 约 4s done，sha256 与直链一致（轮询恰见 done=10485760
    =整块按序输出）；测试拷贝已删、任务已清
  - 耗时参考：直链 4.3s vs 代理 6.3s（本机可直连 sharepoint，代理无增益属预期；
    加速价值在客户端受限场景，threads/chunk 调参留给正式联调）
- e2e 回归：14 张截图通过、控制台 0 错误（唯一 requestfailed 为离开播放页视频流 ERR_ABORTED，良性）
- **实测新发现（挂账号后的环境事实）**：本网络下 login.microsoftonline.com 冷启动约半数概率
  `EOF`（graph.go 请求级重试 1 次不总够）→ 存储 Init 失败置 status、挂载 404，
  后台「重载」即恢复。后续可考虑 Init 时 token 获取多重试几次或退避（记入 M3 联调清单）。
- 已知限制（v1 设计内）：代理模式只支持单区间 Range（播放器均单区间，多区间降级全量）；
  size 未知时退化单流透传；加速依赖直链支持 Range（OneDrive/PikPak 均支持，防御性报错兜底）

## M5 任务+跨存储转存（2026-07-06 完成实现 ✅）
- `internal/task/task.go`：内存态 Manager（worker pool 2、queue 缓冲 256）+ 状态机
  pending|running|done|error|canceled；Task 满足 fs.Progress（SetTotal/SetFile/Add，
  Add 支持负数回退、每 ≥500ms 重算 Speed）；Submit/List(owner 过滤,admin 全量)/Get/
  Cancel(running 调 cancel、pending 直接置 canceled，run() 见非 pending 跳过)/
  Retry(仅 error|canceled，重置进度复用 fn 重入队)/ClearDone；List/Get 返回 snapshot 深拷贝。
  哨兵错误 ErrNotFound/ErrForbidden/ErrBadState → handler 映射 404/403/409。
- `internal/fs/transfer.go`：SameStorage(u,src,dst)→(same,dstUploadable,err) 供 handler 分流；
  Transfer：Resolve 两端→目标断言 Writer+Uploader→planDir 递归规划 files+dirs（浅→深）→
  SetTotal→MakeDir(忽略 ErrExist)→逐文件 copyOne（Link→Local 句柄或 http GET URL(带 Header)→
  countingReader 计progress→Put；**文件级重试 2 次**，失败回退已计字节）→isMove 全成后删源子树。
  **countingReader 每次 Read 检查 ctx**——本地句柄不走 HTTP，不查 ctx 则取消无法中断 io.Copy
  （e2e 实测发现后修复，8GB 拷贝取消即停、临时文件被 local Put 错误路径清掉）。
- `internal/server/handler_task.go`：GET /api/tasks、POST /api/tasks/:id/cancel|retry、
  DELETE /api/tasks/done（authed 组，权限在 Manager 内按 owner 过滤）。
- fsMoveCopy 分流：每条 path 先 SameStorage——同存储走原同步逻辑；跨存储 !upOK 收集
  errors 跳过；跨存储 upOK → tasks.Submit（闭包取 src/dst/target 副本，任务成功后
  RenamePrefix/ScanSubtree 更新索引）；返回 {task_ids, errors}。
- 接线：Server 加 tasks 字段（server.New 增参）、router 加 tasks 四路由、
  main.go task.New(2) 注入；go.mod google/uuid 转直接依赖。
- 前端：components/TasksDrawer.vue（append-to-body，开抽屉起 1.5s 轮询、关清 interval；
  进度条/速度/状态标签/取消/重试/清除已结束，emit count 供 badge）；
  Files.vue 工具栏 Van 图标+el-badge（进行中数），首进拉一次任务数；
  MoveCopyDialog 读返回 task_ids→提示"已创建 N 个转存任务"+emit('tasks') 开抽屉，
  errors 用 warning 提示。
- 测试（全绿；本机无 gcc，-race 不可用，改 -count=3 重复跑）：
  - task_test.go 10 用例：done 进度/error/cancel running/cancel pending/权限/retry 后成功/
    List 过滤/ClearDone 只清自己/50 并发 Submit/速度计算
  - transfer_test.go 8 用例：**两个真实 local 挂载**（t.TempDir）单文件 copy 内容一致+进度相符/
    目录树三层复制/move 删源/预取消/中途取消(cancelingDriver)/只读目标拒绝(roDriver)/
    flaky 源重试 1 次后成功(进度回退干净)/重试 3 次耗尽报错
- e2e 验证（双本地存储 /本地存储2 → E:/桌面/newlist/files2，已留在 DB 供后续测试）：
  - curl：copy 返回 task_ids→done 88/88→内容 diff 一致；目录树 copy 三文件齐；move 后源删；
    同存储 copy task_ids=null（纯同步）；错误源路径任务进 error 态；retry 复跑；
    8GB 文件 cancel 立即 canceled 无残留；DELETE /tasks/done 清空
  - e2e-check.mjs 新增 05b-tasks-drawer 截图（建真实转存任务→点 Van 图标开抽屉），
    全流程 14 张截图通过；脚本内清理 fetch 要 await r.json() 读完 body 再翻页，否则 ERR_ABORTED
- 已知限制（v1 设计内）：任务内存态重启丢失；跨存储转存不做目标重名预检（Put 行为由驱动定，
  local 是覆盖写临时文件+改名）；handler 对同步条目失败仍是中断式返回（与 Demo 行为一致）

## M3 真实联调收尾 + token 重试（2026-07-08 ✅）
- **token 获取瞬时错误重试**（graph.go，解决 M6 段记录的「AAD 冷启动半数 EOF → Init 失败挂载 404」）：
  - refreshLocked 失败分类：网络层错误（Do/读 body，EOF·超时·连接重置）与 AAD 5xx
    包装成 transientTokenError；4xx/invalid_grant 等配置类错误不包装
  - token()：瞬时错误最多 4 次带退避（0.5s→1s→2s，tokenRetryBase 测试可调小）；
    配置类错误保持旧行为共 2 次即止；上抛前剥掉包装（存储 status 文案不变）；
    退避期间响应 ctx 取消。覆盖 Init 与所有运行期请求（都经 token()）
  - 单测新增 2 例：503+连接级 Hijack 断连(EOF)→第 3 次成功；400 invalid_grant 止步 2 次
    且 AADSTS 文案上抛。全套 go test ./internal/... 通过
- **真实账号尾项实测 19/19 全过**（frontend/od-tail-check.mjs 留库，OD_ROOT 可参数化
  给 PikPak 复用；对用户自挂 onedrive_app /test）：
  挂载可列 → mkdir 中文目录 → 小文件 256KiB 上传(<4MiB 单请求，名称含中文+空格) →
  列表大小一致 → raw 302 直链 → 下载 sha256 一致 → 图片上传+缩略图首查即中
  (image/jpeg 服务端落盘) → rename → copy(Graph 异步+monitor 轮询) → 副本大小一致 →
  move → 源删目标齐 → 移动后 sha256 一致 → remove 整目录 → 清理无残留
- M3 至此仅剩「token 轮换回写长跑观察」（时间依赖，随日常使用自然验证）

## M3 OneDrive 双驱动（2026-07-06 完成实现）
- `internal/driver/onedrive/`：graph.go（token 缓存/提前5min刷新/失败重试1次/401强刷重试/
  429 Retry-After≤5s、错误映射 itemNotFound→404 等、itemURL 逐段转义、refresh_token 轮换回写）+
  onedrive.go（"onedrive" refresh_token 模式 + "onedrive_app" client_credentials 模式共用实现；
  List $top=1000 翻页、Link=@microsoft.graph.downloadUrl 302、Thumb /thumbnails/0/large、
  MakeDir 逐级、Move=PATCH parentReference.path、Copy=202+Location 轮询、
  Put <4MiB 单请求 / ≥4MiB uploadSession 10MiB 分块每块重试2次、size<0 落临时文件、checkName）
- 框架新增：driver.ConfigPersister 接口 + fs.Reload 注入 UPDATE storages 回调（token 轮换持久化）
- 测试：onedrive_test.go 11 个用例全绿（httptest mock：token 表单/轮换回写、401 重试、翻页、
  404 映射、downloadUrl、mkdir 最后级 ErrExist、小上传 body 完整、640KiB 分块序列+中途500重试、
  copy 轮询、checkName、itemURL 中文转义）
- 冒烟：/api/admin/drivers 返回两驱动完整 schema（secret/remote 通用字段正确）；
  后台添加存储切换驱动动态表单渲染正确（_shots/13、14）；假凭据存储 status 记录 AAD 真实错误
  且不影响本地挂载（fs.Reload 容错）
- **待办（需用户参与）**：真实账号联调——用户提供 client_id(+secret)+refresh_token 或
  onedrive_app 四件套后验证浏览/下载/上传/写操作/缩略图/token 自动刷新与轮换回写

## M4 PikPak 驱动（2026-07-06 完成实现）
- `internal/driver/pikpak/`：
  - consts.go（auth=user.mypikpak.net / drive=api-drive.mypikpak.net；android/web/pc 三套
    client_id·secret·version·package·UA·盐表常量，失效时集中此处更新）
  - sign.go（captcha_sign：起始串 clientID+ver+pkg+deviceID+毫秒ts，逐盐 md5hex 链，前缀"1."）
  - client.go（token 缓存/提前5min；refresh_token 换 token，4126 失效自动转账密登录；
    账密登录=captcha/init(按 username 形态选 email/phone_number/username meta)→signin；
    drive 请求统一 req：Bearer+X-Device-ID+X-Captcha-Token+UA；业务错误看 body error_code：
    4122/4121/16→强刷 token 重试1次、9→captcha 重刷重试1次、10→"操作频繁"、not_found→ErrNotFound；
    refresh_token 轮换/device_id 生成回写 persist）
  - pikpak.go（ID 寻址：lookup 路径→ID 逐级 List 匹配 + 2min TTL 缓存，写操作后清表；
    List/Stat/Link(web_content_link→回退 medias[0].link.url，带 UA 头)/Thumb(thumbnail_link)/
    MakeDir 逐级/Rename PATCH/Remove batchTrash/Move batchMove/Copy batchCopy(均预检目标重名→ErrExist)；
    **不实现 Uploader**→fs 层 can_upload=false、上传按钮隐藏、Put 被拒）
- 复用框架 driver.ConfigPersister（fs.Reload 通用注入，无需改框架）
- 测试：pikpak_test.go 17 个用例全绿（httptest 同充 auth+drive：captchaSign 黄金向量、
  loginMeta 三态、账密登录链+persist、refresh 轮换、4126 转登录、翻页、lookup 缓存命中/ErrNotFound、
  Link 选择、MakeDir 末级 ErrExist、Move 重名预检+body、删根 ErrNotSupported、请求头齐全、
  16 号 token 过期重试、9 号 captcha 重试、10 号操作频繁）
- 冒烟：/api/admin/drivers 返回 pikpak 完整 schema（password/refresh_token secret=true、
  remote 通用字段 proxy/threads/chunk_mb 追加、platform select android|web|pc）；假凭据存储
  status 记录 PikPak 真实错误（"触发人机验证…"）且不影响本地挂载；根目录挂载点合并正确
- **待办（需用户参与）**：真实账号联调——用户提供 PikPak 账号+密码（或 refresh_token）后验证
  浏览含中文路径/302 直链播放(注意绑定 UA)/缩略图/改名移动复制删除/refresh_token 失效自动重登/
  上传被拒提示

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

## UI 迭代记录（用户反馈）
- 2026-07-09 反馈#34「传输任务监控 + 只清已成功、单条删除」：#32 的传输任务抽屉本已完整
  （TasksDrawer.vue：1.5s 轮询 /api/tasks，逐条显示状态标签/进度条/已传·总·速度·当前文件、
  失败显错误、running/pending 可取消、error/canceled 可重试，工具栏🚚按钮带活动数角标）。
  用户点：原「清除已结束」一键把 done+error+canceled **全部**删掉，与「重试失败」冲突
  （清列表=失败任务也没了）。改为**只清已成功**：
  - 后端 task.go：`ClearDone` 收窄为只删 `StateDone`（失败/取消刻意保留便于重试）；
    新增 `Remove(id,owner,isAdmin)` 删单个终态任务（running/pending 返回 ErrBadState 须先取消，
    带 find() 权限校验）。handler_task.go 加 `taskRemove` + router `POST /tasks/:id/remove`
    （用 POST 而非 DELETE /tasks/:id，避免与静态 DELETE /tasks/done 在 gin DELETE 树冲突）。
  - 前端 TasksDrawer.vue：底部「清除已结束」→「清除已成功」（仍打 DELETE /tasks/done，语义已收窄）；
    每条终态任务（done/error/canceled）加「删除」按钮 → POST /tasks/:id/remove，可单独移除
    不想重试的失败/取消任务。
  - 回归：`go test ./internal/task/ ./internal/server/` 全绿（TestClearDone 扩：加失败任务断言其
    「清除已成功」后仍在；新增 TestRemove：失败可删/重复删 ErrNotFound/他人 ErrForbidden/运行中
    ErrBadState）；go vet 净；npm run build + go build 重嵌 webvid.exe（已核 dist 含「清除已成功」
    与 /tasks/${id}/remove）。e2e-check/offline-check 直打 DELETE /tasks/done 仍有效、mobile-check
    按🚚 title 定位抽屉不受影响（本环境无跑服，未跑三 mjs）。
- 2026-07-09 反馈#32「AList 式任务设置：复制/离线下载线程数 + 离线下载功能 + 下载/上传/复制限速」：
  - **task.Manager 分组队列+动态线程数**：GroupCopy/GroupOffline 各自独立队列与 worker 池，
    Task.Group 进 JSON；SetWorkers(group,n) 运行时扩缩（钳 1..32）——每 worker 一个 quit chan、
    worker 循环双 select **先查 quit 再取队列**（否则队列积压时收缩不生效）；收缩只关多余
    worker、不打断在跑任务；New(copyWorkers) 保旧签名，Submit 走 copy 组、SubmitIn 指定组、
    Retry 按 t.Group 回原队列。
  - **internal/limiter 全站限速**：令牌桶允许透支（tokens 减成负→按欠额睡）、闲置累积上限 1s
    配额、SetKBps 热调（对在途流立即生效）、ctx 取消打断等待；Reader/Writer 限速时按 64KB
    分块。三个限速器 server.New 内建：limUp 包 /fs/upload 请求体、limCopy 经 fs.SetCopyLimiter
    注入 transfer.copyOne 源流、limDown 包 /raw 下行+离线下载拉流。**坑①**：包
    http.ResponseWriter 必须只内嵌接口（limitedResponseWriter），刻意不提升底层 ReadFrom——
    否则 io.Copy 走 sendfile 直通绕过限速 Write。**坑②**：回环请求豁免下载限速（本机
    ffprobe/HLS/预载都走 127.0.0.1 的 /raw 回环，不豁免会把自己的转码探测限死）。
  - **离线下载** POST /api/fs/offline {urls[],dst_dir}（CanWrite 组）：仅 http/https，逐 URL 建
    offline 组任务：拉流→fs.Put(不覆盖)→index.Upsert；文件名 Content-Disposition 优先、次 URL
    末段（sanitize 去分隔符/控制符，兜底 download.bin）；源无 Content-Length 时完成后
    SetTotal(实收字节)——否则 run() 收尾 Done=Total 会把进度归零。前端 Files 工具栏
    「离线下载」钮（caps.upload 才显示）+弹窗（textarea 每行一 URL，显示目标目录），
    提交成功自动开传输任务抽屉。
  - **任务设置持久化+热生效**：settings 表 6 键 copy/offline/upload_workers +
    copy/upload/download_speed_kb（KB/s，0 不限），conf 钳位 getter；PUT /admin/settings
    指针字段缺省=保持原值，保存即 SetWorkers+SetKBps 热应用（免重启）；main.go
    task.New(cf.CopyWorkers())；/public/settings 透出 upload_workers → stores/app →
    UploadDrawer 并发替换写死的 CONCURRENCY=2；Admin.vue 站点设置卡加「任务设置」
    「速度限制」两组表单（el-input-number + field-help 说明）。
  - 坑③：server 集成测试挂 local 驱动时 os.OpenRoot 持根目录句柄挡 Windows TempDir 清理
    → t.Cleanup 里 DELETE storages + f.Reload 触发 Drop 释放；坑④：check 脚本后台路由是
    /@admin 不是 /admin（守卫静默重定向回首页，.el-tabs 等不到超时）。
  - 验证：go test 全绿（task 分组/扩缩 2 项、limiter 4 项、offline+设置热生效集成 2 项）；
    新增 frontend/offline-check.mjs **19 项**（表单 6 字段/保存热生效 API 读回/还原/离线下载
    全链路落盘校验+清理）；mobile-check 37 项回归全绿；npm build + 重编 webvid.exe 重启实测。
- 2026-07-09 新驱动「Telegram 收藏夹」（需求：TG 视频转发到收藏夹→本站复制到 OneDrive=离线下载）：
  只读驱动 internal/driver/telegram（gotd/td v0.160.0 MTProto **用户账号**，Bot 读不到收藏夹且
  Bot API 无拉历史接口）。收藏夹平铺成文件：`messages.Search`(InputPeerSelf+FilterDocument)
  分页拉全量、2min 缓存（防索引重建反复打 API），命名 `<消息ID>_<清洗文件名>`（O(1) 反解析，
  无名按 mime 兜底 file_日期.mp4）；Stat/Link 走 `messages.getMessages` 按 ID 单取（file_reference
  最新鲜）。下载=自实现 io.ReadSeekCloser（reader.go）：512KiB 块 `upload.getFile`（4096 对齐+
  整除 1MiB+不跨 1MiB 边界三约束一次满足）、Seek 纯指针运算（ServeContent 先 SeekEnd 探大小
  不打网络）；docSource 兜 FILE_MIGRATE_x（换 DC 重试并记住，client.DC 建异地池——**池不经
  客户端中间件，FLOOD_WAIT 要手动包 floodwait.SimpleWaiter.Handle**）与 FILE_REFERENCE 过期
  （重取消息刷新重试）。会话持久化走 ConfigPersister（同 pikpak refresh_token 套路）：
  sessionStore 桥接 gotd session.Storage↔cfg["session"](base64)。登录流程（收藏夹必须用户账号=
  手机号+验证码+可选两步密码）：LoginManager 在 send_code/sign_in 两个 HTTP 请求间**保持同一
  bg.Connect 连接**（phone_code_hash 绑定会话，5min 过期）；POST /api/admin/telegram/:id/
  {send_code,sign_in}，sign_in 遇 SESSION_PASSWORD_NEEDED 回 {need_password:true} 前端补密码框，
  成功把 session 写回 storages.config 并 afterStorageChange 重载。Admin.vue：telegram 行操作列
  多🔑按钮开登录弹窗（有 tg 存储时操作列 170→200/移动 100→124 防 #29 的表格内溢出复发）；
  配置表单零改动（FieldSpec 动态渲染）。**字段名坑：网络代理字段叫 `socks5` 不能叫 `proxy`**
  （与 CommonRemoteFields 服务器中转开关撞名）；Remote:false（无直链，Link.Local 恒走
  ServeContent/转存流，media 的 ffprobe/HLS 自动走 /api/raw 回环无需适配）。
  验证：单测 telegram_test.go 10 项（命名映射回环/reader 对齐+跨块+SeekEnd 零网络+取消/
  FILE_MIGRATE 换 DC 记忆/引用过期刷新/分页翻页+防死循环/非文档消息跳过），go vet+全套 go test 过、
  npm run build 过；**真实账号联调待用户提供 api_id/api_hash（my.telegram.org）+手机号+SOCKS5 代理**：
  添加存储→状态「未登录」→🔑发码→登录→文件页复制到 OneDrive 走既有跨存储转存任务。
  已知边界：单流下载 ~1-2MB/s（转存后台任务可接受，提速留 reader 内并发预取）；TG Photo
  类型（非文件图片）不映射；show_video 默认开会触发预载对每个视频回环 ffprobe（偏慢，可关）。
- 2026-07-08 反馈#29「移动端后台管理 UI 有的显示不全需要滑动」：后台存储/用户**表格列宽写死**
  （存储 4 列 min-160+110+140+170=580px、用户 6 列合计 640px），390px 窄屏内 el-table 内部横向
  溢出——页面级无溢出（mobile-check 老断言据此仍绿），但**操作列（编辑/重载/删除）被挤出屏外**，
  必须在表格里横向滑动才能点。另 el-tabs 头 4 项 344px 略超 336px 触发左右滚动箭头、「索引管理」
  被切。修复照抄 Files.vue 的 `isMobile` 列显隐模式（Admin.vue）：①两表移动端缩列宽，存储保留
  4 列全进屏（驱动 82 让「OneDrive」不断词换行）、用户**隐可见根路径+可写**（移交编辑弹窗查看，
  与 Files 隐修改时间同思路），角色列 72 防「管理员」标签被 `.cell` 省略号截成「管理员..」；
  ②scoped `@media(max-width:768px)` 收 `.el-tabs__item` padding 0 13px+font 13px（nav 344→286）、
  `.el-table .cell` padding 0 8px、操作列相邻按钮 margin-left 2px（默认 12px 过宽）。
  **坑：mobile-check 老「后台页无横向溢出」只测 documentElement.scrollWidth，el-table 内部溢出照样
  绿**——本轮给 mobile-check 补 3 断言（Tab 头 nav≤wrap、存储/用户表 table.scrollWidth≤clientWidth）。
  顺手修了 mobile-check 两处早坏（反馈#28 已察觉未修）：播放按钮 `has-text("立即播放")` 在有续播进度
  视频上失配→改用稳定类 `.vdc-play`（#25 文案继续观看/立即播放）；播放页沉浸模式隐顶栏后直接点
  `.user-chip` 进后台点不到→先 goto /library/video 让 user-chip 复现。回归 frontend/mobile-check.mjs
  **26→37 项全绿、零控制台错误**；截图 _shots/diag-admin-{storage,users}.png（前后对比：操作列由屏外→屏内）。
- 2026-07-08 反馈#28「移动端后台管理二级卡片（弹窗）的字体排列有问题」：EP el-form 默认
  label-position=right，字数不一的中文标签右缘齐、左缘参差（「角色」「启用」贴右、「可见根路径」
  伸左），窄屏尤显乱。纯 CSS 修复——glass.css `@media (max-width:768px)` 内改 `.el-form-item__label`
  **左对齐**：`justify-content: flex-start !important`（EP 标签是 flex，改 justify-content 即可）。
  所有标签从同一左缘起排、右侧留白，文字保持自然。**踩坑/返工：起初用两端对齐
  `text-align:justify; text-align-last:justify`（须先 `display:block` 破 EP 的 flex 才生效），但会在
  「角色」「启用」等短标签的字与字之间插入空隙，用户明确反对「不要在字体里面加空格」→ 改左对齐。**
  已核 admin 用户弹窗：6 标签左缘齐（left=29），「用户名」渲染宽 42px＝自然三字宽（若被 justify 撑开
  会到 ~98px），确认无字间距。全站移动端表单共用此规则（编辑用户/添加存储/站点设置）。注：本轮
  mobile-check 卡在播放页——反馈#29 已查明是 #25 续播按钮文案过期（现已改用 `.vdc-play` 类），与本改无关。
- 2026-07-08 反馈#26「照片墙的最近查看也加『查看更多』」：与反馈#24 对称的纯前端改动
  （LibraryPhotos.vue）——加第四态 `?viewed=1` 视图（viewedView 冻结取值 computed、isHome 加
  `!viewedView`、watch 依赖加 viewedView）；loadMore 命中 viewedView 改打
  `/media/history?kind=image&limit=50`（一次取满不分页）；「最近查看」货架头加「查看更多」按钮
  → push `{query:{viewed:'1'}}`，大标题「最近查看」隐排序/横幅/货架、空态专用文案；网格复用纯图墙
  `.photo-grid .cell`（无字幕，点开灯箱）。**吸取反馈#24 教训：按钮直接用独立类 `.see-more`
  不复用 `.see-all`**（photos-check.mjs 断言 `.see-all` count===1，撞类会破）。验证
  frontend/photos-history-check.mjs（新，留库 11 项）+ photos-check 19、e2e 14 全绿、零控制台错误；
  截图 _shots/17-photos-viewed.png。（本轮服务器由并行会话反复 rebuild/restart，我的源码改动落盘
  后其构建自动带上，served LibraryPhotos chunk 含 viewed 已核）
- 2026-07-08 反馈#25「增加播放进度功能」＝断点续播 + 进度展示：
  - **后端**（handler_media.go / db.go / router.go）：play_history 表加 `position`/`duration`
    两列（db.go migrate 尾部 `ALTER TABLE ADD COLUMN` 补旧库，新库重复列失败忽略）；
    POST /media/played 收 `{position,duration}`——**接近片尾（≥95% 或剩余 ≤5s）position 归零**
    （下次从头）；**duration 仅在 >0 时更新**（播放器 ready 早于元数据会上报 dur=0，
    不能覆盖已知时长否则进度条画不出，用 `duration=CASE WHEN excluded.duration>0 THEN
    excluded.duration ELSE play_history.duration END`）；新增 GET /media/progress?path=
    读单文件续播位置（无记录返 0/0，播放页起播定位用）；/media/history 带回 position/duration
  - **Play.vue**：起播前 GET /media/progress 取续播点（与 /video/info 并行不拖慢），
    `resumeAt>0` 时——hls.js 用 `new Hls({startPosition:resumeAt})`、direct/Safari 原生走
    `art.currentTime=resumeAt`（在 `ready` 事件里）；上报点＝ready(起播记最近播放)+
    **video:loadedmetadata(补 duration)**+每 10s 定时+pause/seeked/ended(播完报满进度→后端归零)+
    onBeforeUnmount(SPA 返回时最后一次)；`?restart=1`（详情卡「从头播放」）跳过续播定位
  - **进度条 UI**：LibraryVideo 最近播放货架/`?played=1` 网格卡片缩略图底沿加 `.prog-bar`
    （`pct(v)=position/duration`，0 不画）+ 副文本「看到 x% · 时间」；VideoDetailCard 打开时
    GET /media/progress 回填，大图底 `.vdc-prog` 进度条 + 「观看进度」信息行 +
    主按钮据进度显示「继续观看／立即播放」+ 有进度时多一个「从头播放」（→ restart=1）
  - **踩坑**：①直连分支加 `await progP` 拓宽了「异步期间快速离页 → artRef 变 null 仍
    new Artplayer」竞态 → `mount()` 里 `await nextTick()` 后加 `if(!artRef.value) return`
    卸载守卫（detail-check 曾报 ArtPlayerError container 无效）。②去掉 report 的整秒去重——
    它会挡掉 loadedmetadata 的补报（ready(0) 后 loadedmetadata 仍是 sec=0 被跳过 → duration 存不进）；
    改由各调用点自身节流（timeupdate 10s 定时，其余生命周期事件低频）
  - 验证：单测 media_progress_test.go（上报→读回→history 带回→duration=0 不覆盖→片尾归零→
    图片无进度→不存在 404）+ db_test 迁移；frontend/progress-check.mjs（新，留库 15 项）——
    direct mp4/HLS .ts「播放→seek→离开→重进从原位续播」、最近播放货架进度条宽度>0、
    详情卡继续观看/从头播放/进度行、从头播放不跳续播点；hls-check 30/detail-check 15/
    e2e 14 张全过（照片页 OneDrive 缩略图 429 限流属真实环境已知基线）
- 2026-07-08 反馈#24「最近播放加『查看更多』，显示最近播放的 50 个视频」：纯前端，
  复用现有 `?dir=`/`?all=1` 三态视图机制加第四态 `?played=1`（LibraryVideo.vue）——
  - `playedView` 冻结取值 computed（同 dir/all，keep-alive 驻留期切走 query 变空不误触发
    重置）；`isHome` 加 `!playedView` 条件；watch 依赖加 playedView；loadMore 命中
    playedView 时改打 `/media/history?kind=video&limit=50`（后端 limit 上限 100，一次取满
    不分页 hasMore=false）；大标题「最近播放」、隐藏排序下拉与 Featured/货架、空态专用文案
  - 「最近播放」货架头加「查看更多」按钮 → push `{query:{played:'1'}}`；网格副文本 playedView
    时显示 subText（含断点续播「看到 x%」，与并行的续播特性天然合流）
  - **踩坑：查看更多按钮初版复用 `.see-all` 类**，与「所有视频·查看全部」撞类 → scroll-check
    `page.click('.see-all')` 命中 DOM 里更靠前的它、跳 played 而非 all 视图导致断言超时。
    改独立类 `.see-more`（CSS `.see-all,.see-more` 共享样式）解决；后续新增货架入口按钮务必用
    独立类，勿复用 `.see-all`（scroll-check/photos-check 都按该类唯一定位）
  - 验证 frontend/history-check.mjs（新，留库 12 项）：先 POST /media/played 预置历史 →
    货架按钮存在/点击进 played=1/大标题/无排序下拉·横幅·货架/网格条数=接口且 ≤50/副文本以
    「播放」结尾/卡片弹详情/返回键回主页/深链直达/播放后返回仍在视图；截图 _shots/16-played-view。
    scroll-check 18/18、detail-check 15/15、e2e 14 张全绿（本轮与并行「断点续播」特性合并部署，
    server 二进制含两者，e2e 零控制台错误）
- 2026-07-08 反馈#23「.ts 文件还是无法播放」：mpegts 里的 AAC 是 ADTS 帧，event remux
  `-c:a copy` 进 fMP4 首个音频包即被拒（Malformed AAC bitstream），ffmpeg 死后 hls muxer
  照样 finalize 出 0.28s 截断片+ENDLIST 的"合法"列表 → 前端拿到列表却播不动。
  修复=copy+aac 恒挂 `-bsf:a aac_adtstoasc`（ASC 源直通无害）+ media_info 加 audio_aac 列
  （迁移清旧缓存防复发）。明细/负向验证/回归见 M10 段「踩坑#2」；hls-check.mjs 26→30 项
  （新样片 转播录像.ts 已经 upload API 入索引）；vod 档不受影响（音频恒转 aac 无 ADTS 问题）
- 2026-07-08 反馈#22「桌面端 UI 已差不多，针对移动端进行 UI 优化」：全站 ≤768px 响应式适配——
  - **导航重构**：App.vue 移动端隐藏顶栏 `.nav`，新增底部玻璃 Tab 栏（视频库/照片墙/文件/搜索，
    图标+文字、active 高亮、`env(safe-area-inset-bottom)` 刘海屏留白）；顶栏压到 48px 只留
    brand+用户链；后台管理入口仍在用户下拉。glass.css `.page` 移动档 padding
    `68px 14px calc(88px + safe-area)` 给 Tab 栏让位
  - **新增 `utils/viewport.js`**：`isMobile` 响应式 ref（matchMedia 768px，与 CSS 断点同源），
    只给纯 CSS 够不着的地方用——el-carousel 高度 prop（420px→240px）、Files 表格列显隐
  - 视频库/照片墙：Featured 标题 34→20px、info 内边距收紧；货架卡 264→190px（照片 188→140px）；
    视频网格固定 2 列、照片网格 3 列；lib-title 28→22px
  - Files：工具栏 flex-wrap 换行（选中批量操作时第二行）；表格隐藏「修改时间」列、
    大小/操作列收窄（总宽恰好 ≤390px 视口）
  - Search：搜索条 flex-wrap，输入框独占一行；结果行大小/时间折到名称下方第二行
  - 弹窗/抽屉全局收窄（glass.css 移动档，`!important` 压过行内 width）：el-dialog/el-message-box
    `max-width: calc(100vw-24px)`；el-drawer 统一 `width: calc(100vw-24px)`（420px 定宽超屏、
    TextDrawer 60% 太窄，移动端一律近全宽）；VideoDetailCard 信息区收紧、双按钮 flex:1 均分；
    Login 卡片 `min(380px,100%)`；Admin tabs/index-card 移动档
  - **触屏交互修复（真 bug）**：`.v-card` 中央的悬停播放图标 `.play` opacity:0 但仍参与命中测试，
    触屏点卡片中心会被它拦截直接进播放页、点边缘却弹详情（行为随落点漂移且 mobile-check
    首跑就撞上）。修复=`@media (hover: none)` 整个 display:none——触屏统一「点卡片=详情，
    详情里立即播放」，桌面悬停行为不变（detail-check 第 5 步直达语义仍过）
  - 验证：**frontend/mobile-check.mjs（新，留库）26/26**（390×844 isMobile+hasTouch：登录/
    Tab 栏可见与切换/各页无横向溢出/轮播 240/网格 2·3 列/时间列隐藏/批量按钮换行/任务抽屉与
    存储弹窗收进屏幕/灯箱开合/播放器挂载/搜索条换行；截图 _shots/m01~m08）；控制台零错误
    （thumb 404/500 与 ERR_ABORTED 为云端瞬时+翻页中断已知良性，脚本按 URL 归类）；
    桌面回归全绿：e2e 14 张 / scroll-check 18/18 / detail-check 15/15 / photos-check 19/19
    （photos 首跑失败为 OneDrive 缩略图 429 限流风暴瞬时导航超时，风暴过后复跑全过）
  - 注意：el-dialog/el-drawer 的 width 是行内样式，媒体查询覆盖必须 `!important`；
    EP 按需样式 chunk 加载晚于 glass.css，同权重规则会被级联盖掉（message-box 亦加 !important）
- 2026-07-08 反馈#21「挂载云盘勾选在视频库/照片墙展示后，后台预载封面与源信息并储存」：
  - **新表 media_info**（db.go）：视频 ffprobe 决策持久缓存——path 主键 +
    size/modified（RFC3339 秒级，与 files.modified 同构）失效判定 +
    video_copy/audio_copy/audio_aac/has_video/has_audio/duration/probed_at；
    旧库补列 ALTER audio_aac 成功（=首次升级）顺带清表强制重探（并行会话的 ADTS 修复合入）
  - **media 包**：`Decide` 三级缓存——内存(路径+size+mtime) → media_info 持久层 →
    现场 ffprobe 后**双写回内存+库**（media.New 增 db 参数，nil=仅内存供测试）；
    新增 `Info()`（info.go）收拢 direct/hls/unsupported 三档判定（directPlayExts 从
    server 迁入，导出 `IsDirectExt`），handler_video.go videoInfo 瘦身为 fs.Get+Info。
    效果：云盘视频重启后再开详情卡/播放页，策略+时长免现场探测秒回
  - **新包 internal/preload**：后台预载服务——`collect()` 查 files 表全部视频/图片，
    按**最长前缀归属挂载**（Mounts() 本身降序）用 `MediaVisible` 过滤
    （视频→show_video / 图片→show_photo，关开关即不预载该盘对应类型）；
    4 worker 并发 `process()`：thumbs.Get 预热封面（远端下载落盘/本地生成，已缓存秒过）+
    非 direct 视频 media.Decide 探测入库；代际 gen 取消旧轮、Progress 快照（done/covers/
    probes/current）；幂等可随时重跑
  - **触发接线**：index.Builder 加 `OnComplete` 钩子（全量重建成功才回调）→ main.go
    `idx.OnComplete(pl.Run)`——存储增删改/勾选开关保存 → Reload → Rebuild → 完成即预载；
    启动时索引已存在则直接 `pl.Run()` 补漏；**main 改 net.Listen 先绑端口再触发预载**
    （云盘探测走本机 /api/raw 回环，必须已在监听）
  - **API**：GET /admin/preload/progress + POST /admin/preload/run（server.New 增 pl 参数）；
    Admin.vue 索引管理页签改双卡片——「文件索引」+「封面与源信息预载」（百分比进度条/
    当前路径/封面·源信息计数/重新预载按钮），轮询与索引进度合一
  - 测试：media/info_test.go（持久层读写/size·modified 失效/覆盖写/db=nil 空操作/
    真 ffprobe 探测→新会话持久层命中）；preload/preload_test.go（collect 可见性三态/
    图片封面预热落盘/mkv 探测入库+mp4 direct 不入库，无 ffmpeg 自动 Skip）；
    坑：测试 Cleanup 必须 d.Close()，否则 Windows 下 newlist.db 占用删不掉 TempDir
  - 实测：隔离库（本地 23 文件）启动自动预载 19 项封面+6 探测；重启 probed_at 不变
    （持久缓存生效不重探）；开关三态验证（全开 19/全关 0/只开视频库 11，恢复秒完成）；
    /video/info mkv=remux·avi=transcode 带真实时长、mp4=direct；
    **真实库 44324 项**（OneDrive /test）自动开跑，封面持续落盘（sharepoint 偶发
    429/406 单文件失败容错继续，下轮重试）；e2e 14 张、hls-check 26/26、
    detail-check 15/15 全过，Admin 截图 _shots/11-admin-index.png 见双卡片
  - 已知限制（v1）：无缩略图/探测失败的文件不做负缓存，每轮预载会重试一次（好处=瞬时
    错误自愈，代价=每轮对失败文件各打一次 API）；重命名/移动后按新路径重新下载/重探
    （封面键=逻辑路径哈希、media_info 键=path）；预载不删 thumbs 孤儿文件
- 2026-07-08 反馈#20「照片墙的最近添加改成最近查看」：复用视频「最近播放」的 play_history
  机制——**后端本就 kind 无关，零改动**：POST /media/played 校验 `ext_type IN ('video','image')`
  接受照片，GET /media/history?kind=image 早已按 kind 过滤（handler_media.go）
  - utils/lightbox.js 集中上报查看：openLightbox 内 `pswp.on('change')` + init 后各调一次
    `reportView(paths[idx])`；防抖 600ms（快速翻页只记停留那张，不逐张刷屏）；
    `http.post('/media/played',…,{silent:true})`。照片墙 & 文件管理灯箱都计入查看历史
  - api/http.js 加 `silent` 选项：响应拦截器 `else if (!err.config?.silent)` 才弹 ElMessage，
    供 fire-and-forget 上报失败静默（照片不在媒体索引时 /media/played 返 404 也不打扰）；
    Play.vue 播放上报同步改 `{silent:true}`
  - LibraryPhotos.vue：`recent`→`viewed` ref；loadStatic 第二请求由 /media/list?sort=modified
    改 /media/history?kind=image&limit=12；货架标题「最近添加」→「最近查看」、副标题
    `formatTime(played_at)+"查看"`、点击 openList(viewed,…)
  - **行为说明**（同视频最近播放）：查看历史为空时货架不显示（v-if viewed.length）；
    不 live-refresh（loadStatic 仅 watch([dir,all]) 触发，看完照片需重进/刷新主页才更新货架）
  - 验证：photos-check.mjs 改为先点开网格照片记查看历史→重进主页断言「最近查看」货架标题+
    卡片出现（19/19 全过，控制台/pageerror 零错误，requestfailed 全为翻页中断 ERR_ABORTED 良性）
- 2026-07-08 反馈#19「每次打开视频返回时都会退回到最上方」：列表页滚动位置丢失——
  返回时 router 重建组件、watch 重跑 loadMore(true) 清空重拉、window.scrollTo(0) 归零。
  - router/index.js 加 `scrollBehavior`：后退/前进 `return savedPosition`，否则新导航回顶
  - App.vue `<router-view>` 包 `<keep-alive :include="['LibraryVideo','LibraryPhotos','Files','Search']">`，
    四个列表页驻留内存——返回时数据、DOM、滚动高度原样保留，配合 savedPosition 秒回原位；
    四页均加 `defineOptions({ name })` 供 include 精确匹配
  - **坑：keep-alive 驻留期路由切走（进 /play 或 /files 深链）时 route.query/params 会变空**，
    原 `computed(() => route.query.dir)` 会读到空值触发 watch([dir,all]) 重置列表+滚动归零。
    修复=用带 prev 参数的 computed 冻结取值：仅当 `route.path===本页路径` 时读新值，否则保留旧值
    （LibraryVideo/Photos 冻 dir/all，Files 冻 current）——切走不动、返回不重拉
  - **坑：body.infuse-mode 类原挂在 onMounted/onUnmounted**，keep-alive 下不再卸载→切到别的页
    仍残留极光样式。改挂 `onActivated/onDeactivated`（随激活态增删），onMounted 只留 IO 观察器
  - **坑：teleport 弹窗（VideoDetailCard/TasksDrawer append-to-body）挂在 body 上不随组件树隐藏**，
    浏览器后退带着弹窗切走后遮罩残留（z-index 2010 display:block、body 锁滚 el-popup-parent--hidden）。
    `onDeactivated` 关不掉（触发太晚，此时组件已被 keep-alive 冻结，teleport DOM 不再刷新）→
    改用 vue-router `onBeforeRouteLeave(() => visible=false)`：在冻结前触发、仍可正常收起遮罩
  - 验证：frontend/scroll-check.mjs（新，留库）18/18 断言+控制台零错误——主页/查看全部/文件页
    三处「深滚→播放→返回」滚动位置保持(±50px)且不重拉(media/list、fs/list 请求数不变)、
    随机网格内容返回不变、返回按钮回主页仍归顶、开着详情卡片后退遮罩不残留、infuse-mode 随页增删；
    detail-check.mjs 15/15、e2e 14 张全过（thumb ERR_ABORTED 翻页中断良性）
- 2026-07-08 反馈#18「用户管理已可改密码，删除单独的修改密码功能」：
  - 前端：Admin.vue 站点设置页签移除「账号密码→修改密码」按钮+分隔线+弹窗挂载
    （Key 图标/pwDlg/import 同步清理）；删除 components/ChangePasswordDialog.vue
  - 后端：删 router.go `PUT /user/password` 路由与 handler_auth.go changePassword
    （users.UpdatePassword 保留——用户管理 handler_user.go 改密仍在用）
  - 改密路径现在只剩：后台·用户管理编辑用户填新密码（管理员操作）
- 2026-07-08 反馈#17「照片墙主页照片也像视频库一样随机展示，查看全部按序」：
  照抄反馈#12 同套改法——LibraryPhotos isHome 网格 sort=random（200 条每次进主页重抽）、
  主页区块头移除排序下拉只留「查看全部」；?all=1/?dir= 保持有序+排序下拉不变；
  photos-check.mjs 无排序下拉断言不用改
- 2026-07-08 反馈#16「视频库点击视频弹出二级卡片（缩略图+详细信息）」：
  - 新组件 `components/VideoDetailCard.vue`：el-dialog（append-to-body，壳样式须全局
    `<style>` 块——teleport 后 .el-dialog 元素不吃 scoped）；上半 16:9 大缩略图
    （thumbUrl 1200）+右上圆形关闭钮，下半标题/徽标行（扩展名+播放策略+时长+大小）/
    详情行（文件名/所在目录→点击跳文件管理/修改时间/最近播放来源加上次播放）/
    「立即播放」「文件位置」按钮；unsupported 显示后端降级文案
  - 打开时异步拉 /video/info 填策略徽标（direct=原生直连/remux=转封装/transcode=转码；
    seq 代际丢弃过期响应；本地文件秒回、云盘几秒内到）
  - LibraryVideo.vue：三处卡片（最近添加/最近播放/网格）点击改弹详情；**悬停播放图标
    @click.stop 仍直达播放页**；Featured 横幅点击弹详情、「立即播放」按钮 @click.stop
    直达（**e2e 04-play 依赖 .hero-btn 直达语义保留**）
  - 验证：frontend/detail-check.mjs（新，留库）12/12 断言+控制台零错误——弹出/缩略图/
    信息行/策略徽标/立即播放跳转/Escape 关闭/文件位置跳转/播放图标直达/横幅两种点击；
    截图 _shots/15-video-detail.png；全量 e2e 14 张过（requestfailed 全为翻页中断
    ERR_ABORTED 良性，云盘缩略图多时数量偏多属预期）
  - 附：本次 vite build 首跑遇 rollup 原生解析器对 photoswipe 偶发 panic
    （utf16_positions.rs unwrap None），重跑即过，非代码问题
  - **追加（2026-07-08）**：卡片壳从不透明 #14141d 改液态玻璃——background 用
    --glass-bg 半透明白、--glass-border 边框、::before 顶部高光线（同 .glass 签名）、
    @supports 无 backdrop-filter 回退深色；磨砂 blur 本就来自 glass.css 全局
    .el-dialog 规则（之前被不透明底色盖住看不出）。detail-check.mjs 复跑 14/14 过
    （含并行会话加的 keep-alive onDeactivated 关弹窗两项）；控制台仅剩随机抽中
    无封面视频的 thumb 404（数据依赖，fallback 图标兜底，良性）
  - **莫奈取色（2026-07-08）**：信息区玻璃按封面动态染色（Material You 风格）——
    onArtLoad 在封面 `<img>` 加载后 canvas 缩样到 24×24 → 5-bit/通道量化分桶 →
    跳过近黑(<24)/近白(>235)像素 → 主导色打分 `n*(1+饱和/n/64)`（占比为主+饱和度加成
    防灰底压过主体）→ vibrant() 转 HSL 抬饱和 s≥0.42、钳亮度 l∈[0.34,0.58] → 半透明
    竖直渐变（顶 0.30 底 0.14 主色衰减到 0.55×）叠在信息区。**取色失败自动回退中性玻璃**
    （无封面/跨域 302 兜底 getImageData 抛错→catch→tint=null；缩略图同源不会跨域）；
    open() 重置 tint 防上一部残留；:key 切换后旧 img 迟到 load 用 isConnected 挡掉。
    纯 CSS/canvas 前端实现，无新依赖。detail-check.mjs 加「莫奈取色染上玻璃」断言
    （本地样片封面 waitForFunction 查 .vdc-info 内联 linear-gradient）15/15 过、
    控制台零错误；截图 _shots/15b-video-detail-tint.png
- 2026-07-07 反馈#15「视频点击跳转后播放器要等一会才加载出来」：三处串行延迟拆解——
  ① Play.vue await /video/info（对云盘 = 一次 Graph Stat）才挂播放器；
  ② /api/raw 每次 302 前都现调 Graph 拿 downloadUrl；③ 首次进播放页还要现下 Play chunk。
  - Play.vue：DIRECT_EXTS（与后端 directExts 同表，M10 后改由 /video/info 全权判定）
    命中则**进页立即挂 ArtPlayer**不等接口；其余扩展名仍走 /video/info（unsupported 文案）
  - LibraryVideo.vue onMounted：`import('./Play.vue')` 预热播放页 chunk（含 ArtPlayer 143KB）
  - onedrive.go：**直链缓存**——linkCache[relPath]{url,size,mod,exp}，TTL 10min
    （远低于 downloadUrl ~1h 有效期，M6 换链重取到的缓存链必然未过期，安全）；
    Rename/Remove/Move/Put 成功后整表清空；Link 内惰性建表（测试不经 Init 直接构造）
  - 测试：onedrive_test.go 新增 TestLinkCacheAndInvalidate（TTL 内命中不打 API/
    不同路径独立/Rename 后失效重取）；全套 go test ./internal/... 通过
  - 实测（frontend/play-timing.mjs 留库，真实 OneDrive mp4）：播放器挂载 1.6~2.2s →
    **0.7~0.9s**；二次播放元数据就绪 6.8s → **2.7s**（省 Graph 往返）；首次 6.8s 不变——
    大头是 sharepoint 拉 moov（源录制文件 moov 常在尾部，须多次 Range），属直链播放
    固有开销，待 M10 转码/faststart 处理；全量 e2e 14 张通过
- 2026-07-07 反馈#14「点击照片要等几秒才显示原图」：旧灯箱 init 前 `await probe(原图)`
  ——为测尺寸把整张原图先下载一遍，下载完才弹窗，PhotoSwipe 再加载第二遍
  （云盘 302 直链每次都变 → 缓存不命中，原图真下两遍；反馈#11 踩坑段早有记录）。
  - `frontend/src/utils/lightbox.js` 重写秒开策略：立即 init；首图 msrc 用列表页
    已缓存缩略图（PhotoSwipe 仅 isFirstSlide 用 msrc 当占位，正好覆盖点击场景）；
    占位尺寸 probe **缩略图**取比例×8（浏览器已缓存≈瞬时，250ms 超时兜底防变形）；
    真实尺寸在 `loadComplete` 事件从 `<img>.naturalWidth` 原地刷新
    （slide.width/height + content + data 同步改，`slide.resize()` 重算 zoomLevels，
    **不用 refreshSlideContent**——那会重建 content 再发一次原图请求）；
    原 change+probe 原图逻辑删除，全程零额外原图请求
  - `LibraryPhotos.vue`：openList 增加 msize 参数，hero=1200 / recent=480 / 网格默认 320，
    与各货架正在展示的缩略图同尺寸 → 占位图直接命中浏览器缓存；Files.vue 网格本就 320 免改
  - `handler_raw.go`：图片响应加 Cache-Control（本地/代理分支 private,max-age=86400；
    302 直链分支 private,max-age=600 同 thumb 兜底惯例——灯箱前后翻页重建 slide 会
    重复请求同一 URL）；新增 isImage() 按 contentTypeFor 前缀判断
  - 验证：photos-check.mjs 18 项全过；全量 e2e 14 张截图通过（仅翻页 ERR_ABORTED 与
    基线一致）；curl 实测 GET/HEAD raw 图片均带 Cache-Control: private,max-age=86400
- 2026-07-07 反馈#13「每次刷新都会重新加载封面」：远端盘缩略图无任何缓存——
  thumbHandler 对 Thumber 盘 302 直链，redirect 响应无缓存头且 sharepoint 目标 URL
  每次带新 tempauth → 浏览器缓存永远失效，服务端还每次都打一轮 Graph API。
  - `internal/thumb/thumb.go`：Thumber 分支改**下载落盘缓存**（remote()）——
    键 = sha1(逻辑路径+"|remote")（远端 Stat 是网络调用，mtime 不进键；忽略 size 参数，
    OneDrive 固定 large≈800px 一份多用）；TTL 30 天，过期刷新失败沿用旧文件；
    复用既有 singleflight；新增 dlSem(6) 下载并发（与 CPU 生成 sem(2) 分开）；
    download() = 30s 超时 + 浏览器 UA（防网盘校验）+ 20MB 上限 + tmp+rename；
    首次下载失败回退 302（至少显示一次），驱动无缩略图继续走本地分支→404 不变
  - `handler_video.go`：302 兜底分支加 Cache-Control private,max-age=600；
    本地文件分支原有 public,max-age=86400 + c.File 的 Last-Modified/304 不变，
    远端缓存文件走同一分支自动获得
  - 前端零改动（thumbUrl 的 token 来自 localStorage，刷新间稳定，URL 不变）
  - 实测：curl 首次 1.77s（Graph+下载落盘）→ 二次 52ms 磁盘直出、内容一致、
    If-Modified-Since → 304；playwright 照片墙首载 74 张全走网络(2.8MB) →
    reload 后 70 缓存/21 网络 → 再刷 87 缓存（增量为随机 Featured 新抽图+
    懒加载新进视口的图，属预期）；go test server 包通过
- 2026-07-07 反馈#12「视频库首页所有视频随机挑选，查看全部按序」：
  - LibraryVideo.vue loadMore()：isHome → sort=random（HOME_CAP=200 每次进首页重新抽，
    与 Featured 同款后端 ORDER BY RANDOM()）；?all=1 / ?dir= 视图保持 modified/name 排序不变
  - 首页「所有视频」区块头移除排序下拉（随机下无意义），只留「查看全部 ›」；
    全部/目录视图 lib-head 的排序下拉保留
  - 验证：playwright 断言首页无下拉/两次进入网格顺序不同/全部视图首屏与 API
    「最新在前」一致/控制台零错误（_shots/01e）；全量 e2e 14 张通过
- 2026-07-06 反馈#1「主页不要显示图片」：MediaHome 移除图片/相册板块（已被反馈#2 覆盖）。
- 2026-07-06 反馈#2「去掉首页，视频库/照片墙按首页风格」：
  - 删除 MediaHome.vue、MediaShelf.vue、home-check.mjs；glass.css 清理 .shelf-scroll
  - router：`/` → redirect `/library/video`（登录后落地视频库）；App.vue 导航去掉「首页」
  - LibraryVideo.vue：顶部加 Hero 轮播（最新 5 个视频，立即播放）+ 海报网格铺开（Apple TV 式），
    空态改 glass 引导卡；Hero 仅在非 ?dir= 视图显示；hero 排序用 new Date(modified)（ISO 字符串）
  - LibraryPhotos.vue：同款 Hero（最新 5 张，点击开灯箱「查看大图」）+ 照片网格，空态同上
  - e2e-check.mjs 重排：登录 waitForURL 改 /library/video，截图 01-library-video…11-admin-index，
    _shots/ 旧图已清空重生成；全流程通过，唯一 requestfailed 是离开播放页时视频流 ERR_ABORTED（良性）
- 2026-07-06 反馈#3「后台添加存储弹窗被裁剪卡在面板里」：Admin.vue 两个 el-dialog（存储/用户）
  加 append-to-body 修复（根因见踩坑记录 backdrop-filter 条）；e2e 已加弹窗回归截图验证通过
- 2026-07-07 反馈#4「视频库改成 Infuse iPad 版样式」：LibraryVideo.vue 整页重写——
  - 近黑影院背景：页面挂载时 body 加 `infuse-mode`，glass.css 把极光 span 压暗到 opacity 0.12（带过渡）
  - 顶部 Featured 大横幅轮播（kicker「最近添加」+大标题+立即播放，圆点指示器右下）；
    「最近添加」横向货架（12 个，隐藏滚动条）；「合集」货架（复用后端 /media/groups，
    此前前端弃用的接口重新启用，封面叠名称+数量，点击进 ?dir= 视图）；「所有视频」网格
  - 目录视图改 Infuse 资料库样式：圆形返回键+28px 大标题+排序在右
  - **顺带解除 200 上限**：网格 offset 翻页滚动加载（IntersectionObserver rootMargin 600px、
    每页 120、gen 代际计数丢弃 dir/sort 切换后的在途响应）；照片墙仍一次 500 未改
  - e2e：`.hero-btn` 类保留（04-play 步骤依赖）；全流程 14 张截图通过、无新增错误；
    另拍 01b（货架+网格）/01c（目录视图）两张参考图在 _shots/
- 2026-07-07 反馈#5「把合集改成最近播放」：新增服务端播放历史——
  - db.go 加 `play_history(user_id,path,played_at)` 表（PK=user_id+path，upsert 只留最新一次）
  - handler_media.go：POST /api/media/played {path}（NormPath+索引内可见媒体文件校验，
    否则 404）；GET /api/media/history?kind=&limit=（JOIN files，base_path 视野过滤内联在
    f.path 上——baseFilter 的裸 path 列在 JOIN 下有歧义；文件删/移后历史条目自然消失）
  - Play.vue：video/info 返回 direct 时 fire-and-forget 上报；LibraryVideo.vue「合集」货架
    整段替换为「最近播放」（卡片同「最近添加」，副文本=播放时间），/media/groups 前端再度弃用
  - 验证：node 冒烟（空历史→上报 200→出现→不存在路径 404→重复上报不重复）；
    playwright 播放后回库页货架正确按时间倒序（_shots/01d）；全量 e2e 通过；
    go test ./internal/server/ 通过
  - 注意：Windows 下运行中的 exe 被锁——先 go build -o newlist-new.exe，停服后 mv 替换再启
- 2026-07-07 反馈#6「主页所有视频默认 200 个，点查看全部看完整列表」：
  - LibraryVideo.vue 三态视图：isHome(!dir&&!all) / ?all=1 全部视图 / ?dir= 目录视图；
    主页网格一次拉 200（HOME_CAP）不再翻页，区块头加「查看全部 ›」（.see-all）；
    全部视图复用 lib-head 大标题（返回键+「所有视频」+排序）+ 120/页滚动加载；
    视图切换 window.scrollTo(0)
- 2026-07-07 反馈#10「存储加『在搜索中展示』开关」（复用反馈#8 机制）：
  - registry.go CommonFields 追加 show_search（缺省 true）；fs.go MediaVisible(kind) 加
    "search"→show_search 分支；handler_search.go fsSearch 在 base 过滤后追加
    `mediaVisFilter(sql,args,"search","path")`。关闭后该存储**全部内容**（含目录与挂载点行）
    不出现在搜索结果；三开关互相独立；文件管理不受影响；前端零改动（动态表单自动渲染）
  - media_vis_test.go 扩展：/隐藏盘 加 show_search=false → 搜 mp4 不含其文件但含嵌套
    可见子盘与可见盘；搜 jpg 验证 show_search 独立于 show_photo（照片墙有、搜索无）
  - 实测：schema 四驱动含新字段；关开关搜索立即排除、重建完成后对照
    「同批索引行：搜索无/视频库有/文件管理有」；恢复回基线；索引收尾无错、条数完整
  - **验证脚本踩坑**：存储 PUT 会先 DELETE files 再重扫——PUT 后立刻轮询可能看到
    「清空前的旧行」造成假阳性/假阴性，正确姿势是等 /admin/index/progress 结束再断言
- 2026-07-07 反馈#9「修改密码移入后台管理」：
  - App.vue：顶栏用户下拉移除「修改密码」项及 ChangePasswordDialog 挂载（下拉只剩后台管理/退出登录）
  - Admin.vue：站点设置页签内加「账号密码 → 修改密码」按钮 + ChangePasswordDialog（组件复用）
  - ChangePasswordDialog.vue：el-dialog 加 append-to-body（现在渲染于 .glass 容器内，
    不加会被 backdrop-filter 裁剪——踩坑记录已知问题）
  - 后端 PUT /user/password 不动（仍 authed 级）；**副作用：非管理员用户失去自助改密入口**，
    需要时管理员在用户管理里「重置密码」代改（用户知情选择）
  - 验证：playwright 断言下拉无该项/后台按钮在/弹窗完整弹出（含真实改密 admin123→admin456→
    改回，成功提示均出现）、控制台零错误；截图 _shots/12-admin-password-dialog.png；全量 e2e 回归通过
- 2026-07-07 反馈#8「挂载存储可控制内容是否进视频库/照片墙」：
  - registry.go 新增 CommonFields（show_video「在视频库展示」/ show_photo「在照片墙展示」，
    bool 缺省 true），Register 时追加给**所有**驱动（区别于仅远端的 CommonRemoteFields）；
    后台存储表单自动渲染，无需前端专门改动
  - fs.go 新增 `Mount.MediaVisible(kind)`：读 Cfg，键不存在视为 true（旧存储缺省展示）
  - handler_media.go 新增 `mediaVisFilter(sql,args,kind,col)`：按**文件归属挂载=最长前缀**
    生成 CASE WHEN 条件（fs.Mounts() 本身按路径长度降序，命中即最长前缀；嵌套可见挂载
    不被隐藏的父挂载牵连）；全部挂载可见时不追加条件（默认零开销）。
    应用于 mediaList / mediaHistory（f.path 列）/ mediaGroups；**查询期过滤，改开关即时生效
    无需等索引重建**（存储保存本来就会触发重建，属既有行为）。mediaPlayed 上报不拦（只影响展示）。
    文件管理 /fs/list 与搜索不受影响
  - Admin.vue openStorage 编辑旧存储时按驱动字段默认值补齐 config 缺失键
    （否则新增 bool 字段开关会错误显示为关）
  - 测试 media_vis_test.go（全链路 sqlite+local 驱动+索引重建+Router）：隐藏盘视频不出现/
    旧存储无键缺省展示/嵌套可见挂载(/隐藏盘/子盘)按最长前缀正常展示/show_photo 独立/
    最近播放同过滤/groups 同过滤/fs/list 不受影响。**坑**：server 测试二进制需
    `_ "newlist/internal/driver/local"`（生产由 main 引入）；local 驱动 os.OpenRoot 持有
    根目录句柄，Windows 下测试收尾要清 storages 再 Reload 让旧驱动 Drop，否则 TempDir 删不掉
  - 真实服务端到端实测通过：4 驱动 schema 含新字段；关 show_video 视频库**立即**排除本地存储
    （重建完成后仍正确）、照片墙不受影响、文件管理可列、最近播放同过滤；关 show_photo 照片墙
    排除；恢复后回到基线。注意：每次存储 PUT 触发全量重建，真实 OneDrive 4.3万条一轮约 7 分钟
- 2026-07-07 反馈#11「照片墙改成视频库那样的界面」：LibraryPhotos.vue 按 LibraryVideo.vue
  整页重写（结构/样式逐段对齐，含 infuse-mode 影院暗背景）：
  - 三态视图 isHome / ?all=1 / ?dir=；Featured 随机推荐横幅（limit=5&sort=random，
    点击开灯箱「查看大图」）+「最近添加」货架（12 张方形卡片，点击开灯箱）+
    「所有照片」网格（主页 HOME_CAP=200 + 「查看全部」；all/dir 视图 120/页
    IntersectionObserver 滚动加载——**顺带解除照片墙一次 500 的旧上限**）；
    lib-head 大标题（返回键+排序：最新在前/按名称）；灯箱按各自列表开
    （hero/recent/grid 独立传 paths+index）
  - 网格 cell 改 Infuse 画框样式（圆角 12px/深底/hover 放大+白描边），**类名保留
    `.photo-grid .cell`**（e2e 03-lightbox 步骤依赖）；货架卡片方形 188px 带名称+时间
  - 验证：photos-check.mjs（新增，仿 home-check 惯例留库）18 项断言全过——横幅 5 张/
    kicker/货架 12/网格 200 封顶/查看全部跳转/所有照片大标题/返回/目录视图（风景 5 张）/
    灯箱开合/infuse-mode 挂摘；all 视图实测 120→240→360 滚动加载（真实 OneDrive 几万张图）；
    全量 e2e 14 张截图通过、无 JS 错误
  - 踩坑：灯箱 init 前 probe 预载原图，OneDrive 走 302 原图下载慢——测试等 .pswp 要长超时；
    PhotoSwipe 开场动画期间 Escape 可能被忽略，关灯箱要重试至 .pswp 消失
- 2026-07-07 反馈#7「顶部大屏改随机展示」（追加：横屏过滤需求已撤回——封面
  object-fit:cover 竖屏也能铺满，且实际封面多为横图）：
  - 后端 /media/list 支持 sort=random（ORDER BY RANDOM()）；前端 Featured 改独立请求
    limit=5&sort=random，kicker 文案「最近添加」→「随机推荐」，每次回主页重新抽
  - 验证：random 连查三次顺序均不同；DOM 断言全过（hero=5/主页 200/查看全部跳转/
    all 视图 120→240 滚动加载/返回键/控制台零错误）；全量 e2e 14 张通过

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
