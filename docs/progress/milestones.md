# 里程碑实现存档（从 PROGRESS.md 归档，2026-07-23）

> 已完成里程碑 M3/M4/M5/M6/M10/M12 的实现细节与实测记录。历史归档，只读参考。

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

