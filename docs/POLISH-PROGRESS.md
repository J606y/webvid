# 上线后打磨进度（2026-07-25）

> 起因：用户「项目已正式上线，但很多地方还和正在开发中的项目一样，请扫描完善一下，
> 特别是报错语言改成能一眼看出是什么错、不要打哑谜」。
> 追加要求：①文案别一股 AI 味，**对标 Apple 软件更新说明——简洁干练**；
> ②**学 Apple 对细节吹毛求疵，以高标准要求产品**；③push 时注意什么该推什么不该推。

## 状态速览

| 批次 | 状态 |
|---|---|
| A. 报错人话化 + Apple 风文案 | ✅ 已提交 `38c6402` |
| B. UX 反人类/不一致清单（28 项） | ✅ **26 项修完并验证**；#19 用户明确不修；#20 按用户要求改成后台开关 |
| 提交与发布 | ✅ **已随 v2.0.0 发布（当前 Latest）**，README 与仓库 topics 一并更新（见文末） |
| C. 预载「不是现在」（2026-07-26 新需求） | ✅ 前后端完成、测试与 e2e 通过、二进制已重嵌；**未提交** |
| D. 后台「传输任务」+ 文件夹内逐文件清单（2026-07-26 新需求） | ✅ 同上，**未提交** |

用户对清单的决策原话：「播放入口不统一…这个不修是故意设置的；『所有视频/所有照片』实为随机
200 条…这个也是故意设置的但是可以在后台加一个切换按钮…其他的条目全按照标准要求来修」。

---

# A. 报错人话化（已提交 `38c6402`）

`util.Humanize` + 三处异步任务接入 + 驱动前缀正名 41 处 + Telegram 登录错误码翻译 +
`aadMessage`/`oauthMessage` 压缩云盘授权长错误 + ffmpeg/ffprobe stderr 收敛 +
服务端字段名中文化 21 处 + TextDrawer 裸 axios 报错当正文渲染。

本轮补掉一条漏网的（见 B 的验证过程）：**SSRF 拒绝被吞成「无法连接到服务器」**。
`internal/util/humanize.go` 新增「安全策略」分支并**排在网络分支之前**——
`拒绝访问内网/保留地址` 外面裹着 `dial tcp`，会被网络分支吃掉，用户只看到
「无法连接到服务器，请检查网络」，于是以为是网速问题反复重试。现在给出
「不支持内网或本机地址。离线下载只能拉取公网可访问的链接。」并补了单测。

---

# B. UX 清单 28 项（已全部处理）

## 第一层：真 bug（5/5 完成）

| # | 改动 |
|---|---|
| 1 | **管理员自锁死**。`handler_user.go` 的 `userUpdate` 拦住「停用/降级当前登录账号」（对齐已有的「不能删除当前登录账号」双保险）；`AdminUsers.vue` 编辑自己时角色/启用控件禁用并给出说明。新增 Go 测试 `internal/server/user_selflock_test.go` 锁住这条 |
| 2 | **拖到 96% 清掉续播点**。`/media/played` 新增 `ended` 字段，只有播放自然结束才归零；`Play.vue` 仅在 `video:ended` 带 `ended:true`。拖动/暂停不再清进度 |
| 3 | **HLS 运行期失败无兜底**。`Play.vue` 挂 `Hls.Events.ERROR`：网络类重试 3 次、解码类恢复 2 次，仍失败则落到兜底面板（新增「重试」按钮，从中断处接着播）。起播流程抽成 `start()`，卸载/重试共用 `teardown()` |
| 4 | **限速框无 max**。前端三个限速框加 `:max`（与后端 `1<<20` 对齐）；后端 `settingsPut` 改为回传钳位后的完整设置，前端 `saveSite` 用响应回填——界面值恒等于生效值 |
| 5 | **网格进度条死代码**。`/media/list` 改 `LEFT JOIN play_history` 带出 position/duration；`baseFilter` 加 `col` 参数（JOIN 场景传 `f.path`），`mediaHistory` 里手写的那份视野过滤一并收敛回 `baseFilter` |

## 第二层：明显反人类（11/11 完成）

| # | 改动 |
|---|---|
| 6 | **方格视图管理操作全消失**。`MediaGridCard` 加 `selectable`/`selected`/`actions`（默认关闭，搜索页不受影响）：卡片左上常驻复选框、右上「更多」菜单（重命名/下载/删除）。选中集直接写 `selection`，与列表视图共用工具栏批量操作 |
| 7 | **取消/删除任务零确认**。`TasksDrawer` 三处加确认，文案分两类：取消进行中说明进度会丢失需重新开始；删记录说明只删记录不动文件。按钮拉开差异（取消=danger+CircleClose，终态=「删除记录」+Delete） |
| 8 | **文件夹能移进自己**。`MoveCopyDialog` 目标树过滤掉源自身与源的子目录；`handler_fs.go` 的 `fsMoveCopy` 统一拦一道（local 驱动自己挡过，云盘与跨存储转存都没挡） |
| 9 | **多选删除首个出错即 return**。`fsRemove` 改为逐项执行、逐项收集，返回 `{removed, errors}`；前端 `removePaths` 无论成败都 `load()` |
| 10 | **先关弹窗再发请求**。`NameDialog` 的 `confirm` 事件改成 `onConfirm` 回调 prop（Vue 的 emit 丢弃返回值，拿不到父级 Promise），请求成功才关，失败保留弹窗与输入，提交期间 loading |
| 11 | **行内无删除**。列表操作列补单项删除，与重命名/下载同级 |
| 12 | **转存/离线完成后列表不刷新**。`onTaskCount` 在进行中任务数归零时补一次 `load()` |
| 13 | **切换视图不清选中**。`watch(app.viewMode)` 清空 `selection`，不再对看不见的集合执行删除 |
| 14 | **存储表单必填项不校验**。前端 el-form 补 `:model`/`:rules`/`ref`，规则随驱动的 `fields[].required` 动态生成（编辑时 secret 字段留空=沿用旧值，不算缺填）；后端 `missingRequired` 在 create/update 各拦一道（update 须在 `***` 还原之后） |
| 15 | **库页首屏无加载反馈**。两页加加载面板，200ms 延迟显示——本地存储秒回时定时器已被清掉，零闪烁 |
| 16 | **登录失败弹两次 toast**。`http.js` 拦截器对 `/auth/login` 不弹，交给 `Login.vue` 单点呈现 |

## 第三层：设计取舍（按用户决策处理）

| # | 处理 |
|---|---|
| 17 | 搜索点图片→直接开灯箱（可在结果内左右翻页），不再跳照片墙 |
| 18 | 搜索点 pdf/txt/md→直接预览。派发逻辑抽成 `composables/useFileOpen.js`，文件管理与搜索共用一套；Search 页补上 TextDrawer |
| 19 | **不修**（用户：详情卡两步是故意设置） |
| 20 | **改为后台开关**（用户要求）。`conf.MediaHomeSort()`（random/modified，默认 random）→ `/public/settings` 下发 → `app.mediaHomeSort` → `useMediaLibrary` 决定首页那一屏的取法。后台「站点设置」加「媒体库首页」下拉 |
| 21 | 照片墙补「最近添加」货架，与视频库对称。顺带修掉一个连带 bug：灯箱转场锚点选择器 `.shelf-row .p-card .art img` 原本只因页面上只有一个 `.p-card` 货架才成立，加了第二个会取错元素——已给两个 section 加区分类名并各自限定 |
| 22 | GD 授权「已完成」是假确认 → 回调页经 `BroadcastChannel` 同源通知（弹窗用了 `noopener`，`window.opener` 为 null，故不用 postMessage）；无论点哪个按钮都回查 `GET /storages/:id` 的 refresh_token + status 作判据，没授权成功就明确说「尚未完成」 |
| 23 | **索引重建全量清表** → 改成扫完再单事务原子替换（`replaceAll`）。扫描全程不碰数据库，读到的一直是上一版索引，提交那一刻整体切换，**重建期间搜索/媒体库不再空窗**。重建期间发生的增量写（上传/删除/改名）排进 `pending`，替换提交后重放，不会被整表替换抹掉；`ScanSubtree` 整棵子树作为一笔入队（内部走不入队的 `upsert`），避免一次目录复制塞进上万条 |
| 24 | **base_path 对管理员也生效** → 新增 `user.VisibleBase()`（管理员恒 `/`），`fs.accessOK`/`navOK` 与媒体库/搜索的 SQL 过滤统一走它。与既有的 `AllowWrite()`「管理员恒可写」同一条语义。`AdminUsers.vue` 里 base_path 对管理员禁用并说明 |
| 25 | **「重载」vs「保存」语义不一致** → `storageReload` 改走 `afterStorageChange`（Reload + Rebuild）。重载的典型场景就是「刚修好一个失败挂载」，只 Reload 不 Rebuild 的话索引里仍没有它的文件 |
| 26 | **TG「发送验证码」文案与行为不符** → 后端 `CodeSent.Resumed` 标记本次是否复用了未过期会话；新增 `GET /admin/telegram/:id/status`（`LoginManager.Pending`）让弹窗打开时以服务端为准，sessionStorage 只补充「上次发到哪」这类文案细节 |
| 27 | **一条 URL 非法就整单 400** → 跳过非法、创建其余，响应带 `skipped`；前端把无效链接留在输入框里（有效的已在下载，不必整批重粘） |
| 28 | **上传冲突只有「覆盖上传」+ 上传/传输两套系统** → 冲突给「覆盖 / 保留两者 / 跳过」三条出路，多个冲突时顶部出现「全部…」；「保留两者」在扩展名前加序号（同访达），撞名自动往下试。徽标合并后台传输与网页上传（`busyCount`），两个抽屉互留入口 |

---

# 验证结果

- `gofmt` 干净（`internal/driver/driver.go`、`telegram_test.go`、`stream/accel*.go` 本就未格式化，非本次引入）；`go vet ./internal/...` 干净；`go test ./...` **全绿**。
- 前端 `npm run build` 通过；`go build -o webvid.exe .` 已重嵌。
- **e2e 全部通过**（服务跑 5243，新二进制）：
  progress 15/15、hls 30/30、zoom 30/30、mobile 37/37、detail 15/15、scroll 15/15、
  history 12/12、player 12/12、upload-conflict 12/12、search-grid 13/13、files-nav 11/11、
  photos-history 11/11、secret-echo 7/7、detect 3/3、photos 全 PASS、offline 16 通过 0 失败 3 跳过、
  `e2e-check` 全流程 exit 0。

## 本轮同时修好的 e2e 脚本（此前红/崩）

- `secret-echo-check`：原先写死 onedrive，本机没有就崩在 undefined 上 → 改为从
  `/api/admin/drivers` 现取各驱动的 secret 字段（名称+标签），任意带密钥的存储都能测，
  一个都没有则跳过。另修：telegram/googledrive 行前面有带 title 的图标钮，
  `.el-button.first()` 会点到「验证码登录」而不是编辑 → 改用 `:not([title])`。
- `offline-check`：源站只能起在回环地址，而 SSRF 防护本就该拒绝——现在识别到这条
  按**跳过**计并说明，不再算失败（要真正端到端验证得换公网源站）。
- `upload-conflict-check`：跟进「覆盖上传」→「覆盖/保留两者/跳过」，补测「保留两者」
  改名链路，并在末尾清理落盘的测试文件。
- 抽屉定位歧义：两个抽屉互留入口后都含有对方的名字，`:has-text("传输任务")` 会命中两个 →
  统一改用 `[aria-label="…"]`。**因此 `el-drawer` 的 `title` 属性不能因为用了
  `#header` 插槽就删掉**——aria-label 取自 title，删了既破坏 e2e 也是可访问性倒退。

## 已知环境限制（非代码问题）

- `od-tail-check`：为 OneDrive/PikPak 这类全功能云盘写的写操作联调，默认 `OD_ROOT=/test`
  而本机 `/test` 是 Telegram 存储，rename/copy/move 一律「该存储不支持此操作」。
  需真实可写云盘账号才能跑（用户的 E5 OneDrive 已被微软砍配额）。
  已补建它缺的样本图 `files/图片/壁纸/暖阳橘子海.jpg`（ffmpeg 生成，`files/` 不入库）。
- **`npx vite build` 在这台机器上会随机 segfault**（rollup 原生解析器），
  `npm run build` 稳定。构建一律用后者，且**勿接管道**。

---

# 提交与发布

## 提交链

| 提交 | 内容 |
|---|---|
| `38c6402` | A 批次：报错人话化 |
| `f4c2ab1` | B 批次：UX 打磨 26 项 + 另一会话的 `copy_file_workers` |
| `f5de724` | `conf.Version` → 2.0.0 |
| `993f8c6` | README 重写 + 仓库描述与 topics |

## 关于「一起提交」这个决定

本轮改动碰到了另一会话 `copy_file_workers`（文件级并发转存）的 4 个文件——
`conf.go`（`CopyFileWorkers` vs `MediaHomeSort`）、`handler_admin.go`、`Admin.vue`、`fs.go`——
而这 4 个又与 `transfer.go` / `transfer_test.go` / `server.go` / `go.mod` 互相依赖
（`handler_admin.go` 调 `s.fs.SetCopyFileWorkers`），**拆开提交会直接编译不过**。

用户决定一起提交，并要求先审 `copy_file_workers` 是否达标。**审查结论：达标**——
- 共享状态全部加锁：`task.Task` 的 `SetTotal`/`SetFile`/`Add`、`limiter.Limiter`
  （本就为多流共享设计）、测试里的 `fakeProgress`。
- errgroup 的失败语义与原串行一致：首个错误取消 gctx、整任务失败，移动因此不删源。
- 断点续传（目标同名同大小则跳过）在并发路径里保留。
- 有针对性测试 `TestTransferParallelFiles`：验证 workers=4 时并发峰值 >1、workers=1 时恒 =1。

两点如实记录：
- **本机跑不了 `go test -race`**（需 cgo + gcc，未安装），并发安全是逐处核对共享状态得出的。
- ~~**未修的小瑕疵**：并发传输时任务抽屉的「当前文件」会在多个文件间跳~~
  **已修复（2026-07-25）**。根因在契约而不在参数：`SetFile` 是单值语义，每个文件一开始
  就覆盖展示值，后开始的不断顶掉前面的。改为在途集合——`fs.Progress` 用 `FileStart` /
  `FileDone` 成对上报（`copyOne` 以 `defer` 覆盖重试耗尽、ctx 取消等全部出口，不留悬空
  在途项）；`task.Task` 维护有序 `active`，`CurFile` / `Active` 由其派生且仅在 `snapshot`
  填充；展示取最早开始且仍在途的那个，只在它自己完成时前进，因此单调不跳。抽屉并发时补显
  「等 N 个」，不再隐藏并发信息；任务进终态即清空在途集合。**`copyOne` 签名未变**，当初
  担心的撞车点不存在。离线下载仍走 `SetFile`（置为唯一在途项）。上文审查结论里 `SetFile`
  的加锁判断依然成立，只是方法换了名。
  测试：`TestFileTrackingStable` 锁住「后开始的顶不掉最早的」这条不变量，另覆盖同名文件、
  空集合误删、终态清空；`TestTransferParallelCurFileStable` 走真实并发路径，断言展示序列里
  每个文件名最多出现一次。把规则临时改回「取最后开始的」该测试立刻失败，序列为
  `f3→f1→f0→f2→f4→f5→f4`——反向验证过断言确实抓得住这个 bug。

## 发布

**v2.0.0 已发布，当前 Latest**：tag `v2.0.0`，CI run `30143620030` success，
资产 = 3 个 Linux 架构 tar.gz + checksums.txt，更新日志按 Apple 口径手写覆盖
（新增 / 优化 / 修复 / 升级）。大版本号因改动面覆盖全站交互。

发版踩到的两点：
- `gh release view --json isLatest` 这个字段不存在，查 Latest 用
  `gh api repos/J606y/webvid/releases/latest --jq .tag_name`。
- notes 文件在 Bash 里用 heredoc 写（沿用既有教训：子代理 Write 的 `/tmp` 与 Bash 的不是同一处）。

## README 与仓库元信息（`993f8c6`）

按面向用户的口径重写：讲能做什么，不堆实现细节。同时修正两处过时内容——
补上 **Google 云端硬盘**驱动与一键授权（1.9.0 就有，README 一直没写）；
「忘记密码」原文写的是**删掉 `data/newlist.db`**（会连用户和存储配置一起清空），
改为 `webvid reset-password`。

仓库描述已设置，topics 17 个：`self-hosted` `personal-cloud` `cloud-storage` `media-server`
`video-streaming` `file-manager` `onedrive` `google-drive` `pikpak` `telegram` `ffmpeg`
`hls` `transcoding` `golang` `vue3` `sqlite` `nas`。

---

# C. 预载「不是现在」（2026-07-26，未提交）

> 用户：「封面与源信息预载里面增加一个不是现在的按钮，点击后默认延迟 1 天，
> 然后按钮变成继续按钮，点击后可以继续预载工作」。
> 确认过的两点：**到点自动继续**（「继续」只是提前恢复）、**时长固定 1 天不做选择器**。

预载是后台批量下封面 + 探测源信息，几万条时会长时间占带宽。原来只能眼看着它跑完
或反复「重新预载」，没有「现在别跑」的出路。

- `internal/preload/preload.go`
  - `Snooze()`：**只停止派发，不打断在途下载**——手头 ≤4 项跑完即止，未派发的
    `files[i:]` 存进 `pending` 交给「继续」。这样计数不会重复累加（取消在途再重跑会
    把 covers/probes 多加一遍），代价只是停下要等几张缩略图，可接受。
  - `Resume()`：有 `pending` 就接着跑（`start(files, resume=true)` 沿用已有计数），
    否则整轮重来。定时器到点与用户点击共用这一个入口，先在锁内判 `snoozeUntil` 是否
    已清空，谁先到谁生效，不会起两轮。
  - 旧轮还在排空时点「继续」：剩余清单尚未落定 → 直接重跑整轮（幂等，已缓存的快速
    跳过），`gen++` 让排空中的旧轮收尾时自动认输，不写脏状态。
  - `Run()`（手动「重新预载」）清推迟；新增 `AutoRun()` 供索引完成/启动时调用，
    推迟期内跳过并作废旧清单（索引已变）。`main.go` 两处自动入口改 `AutoRun`。
  - 推迟到点写 settings 键 `preload_snooze_until`（RFC3339），`New()` 里恢复：
    未到点接着计时，关机期间已到点则清除，启动照常预载。故 `preload.New` 多收一个
    `*conf.Store`。
  - `Progress` 加 `snoozed` / `resume_at` / `pending` 三个字段。
- `handler_admin.go` + `router.go`：`POST /api/admin/preload/{snooze,resume}`，返回最新进度。
- `AdminIndex.vue`：卡片三态——推迟态（「已推迟，明天 6:53 自动继续。」+ 已缓存计数 +
  剩余项数 + 「继续」）、运行中（进度条 + 「重新预载」「不是现在」）、常态。
  「不是现在」**只在运行中出现**（没在跑就没什么可推迟的）；推迟态只留「继续」一个按钮。
  排空中（已推迟但 worker 还没跑完）单独给一句「正在停下手头的几项…」，不装成已停。
- 验证
  - `internal/preload/preload_test.go` 新增三例：推迟后 `AutoRun` 跳过且状态落库、
    「继续」接着剩余清单跑且计数不清零（total/done/covers 累加到 2）、推迟跨重启恢复
    与到点作废。全包测试通过。
  - 真实服务（隔离实例 5299）：snooze→progress→resume→progress 四连正确；重启后日志
    「预载推迟中，2026-07-27 04:45 后继续」且自动预载被跳过。
  - `frontend/preload-snooze-check.mjs`（新增留库）11/11、零控制台错误，截图复核两态。
    进度接口用桩驱动状态（真实预载几毫秒跑完，稳不住 running），两个 POST 打真后端。

---

# D. 后台「传输任务」+ 文件夹内逐文件清单（2026-07-26，未提交）

> 用户：「在后台管理中增加一个传输任务的界面，比主页的传输任务界面要多个能看到
> 文件夹内的情况的功能」。确认过：任务表格 + 文件清单抽屉（非内嵌展开行）、
> 清单要全量状态（含断点续传的「已跳过」）。

原来一个文件夹转存只上报「总字节 + 在途文件名」，文件夹里到底哪些传完了、哪些还没轮到、
哪个失败了，界面上看不出来。这次在任务层建了文件级模型。

- `internal/task/task.go`
  - 新增 `FileProgress`（路径/大小/已传/状态/原因）与 `FileCounts`（分状态计数）。
    状态：`pending → running → done / skipped / error`。
  - 计数**增量维护**而不是每次数一遍：任务列表 1.5 秒轮询一次，几万条的清单不能全表扫。
    同理 `snapshot()` **不拷清单**（那是每轮几万次拷贝），清单走 `Manager.Files` 分页取。
  - `Files(id, owner, isAdmin, FilesQuery)`：按状态筛 + 路径子串搜 + 分页（默认 200，上限 1000），
    返回的 `Counts` 是不受过滤影响的全量计数。
  - 取消 → 在途文件回落 `pending` 且进度清零（重试要重传，标成失败是谎报）；
    重试 → 清空清单由任务体重新规划；任务结束还挂着 running 的行按结局归位（兜底不留僵尸行）。
- `internal/fs/transfer.go`：`Progress` 接口从「名字」改为「下标」——规划一出来
  `SetFiles(items)` 交出全量清单，之后 `FileStart(i)/AddFile(i,n)/FileSkip(i)/FileDone(i,err)`。
  两包只共用 `model.TransferFile` 一个 DTO，仍不互相 import。
  `relTo` 把源路径压成相对转存根的展示路径（「第一季/蓝星球 S01E01.mkv」）。
  断点续传命中改走 `FileSkip`，界面因此能区分「真传了」和「跳过了」。
- `GET /api/tasks/:id/files?state=&q=&offset=&limit=`（owner 或 admin 可查）。
  `state` 传非法值当「全部」——筛出个空列表会让人以为文件没了。
- `frontend/src/pages/admin/AdminTasks.vue` + Admin.vue 新增「传输任务」Tab：
  全站任务表格（任务/发起人/状态/进度/文件数/操作）→「查看文件」开抽屉（搜索 + 状态筛选 +
  分页 + 单文件进度）。**主页顶栏抽屉不动**，仍是轻量视图。
- 验证
  - Go：`TestFileStates`（四种结局与计数）、`TestFilesQuery`（筛/搜/分页/越权）、
    原 `TestFileTrackingStable` 改索引版并锁住「展示只给文件名」；fs 侧断言清单与跳过上报。
    全包测试 + vet 通过。
  - `frontend/admin-tasks-check.mjs`（新增留库）15/15、零控制台错误：真实跨存储转存 41 个文件
    （限速 512KB/s 造出稳定的「在传」态），看到「传输中/等待」共存、子目录层级路径、
    搜索与筛选、跑完全绿，再转一轮全部「已跳过」。
  - 踩坑：① el-table 会渲染隐藏的测量行，Playwright 定位必须 `:visible` 或限定表格类名；
    ② 焦点在筛选控件里时 Escape 不一定关得掉抽屉，点 `.el-drawer__close-btn` 才稳；
    ③ el-select 的空串当「没选」会掉回灰色占位文字，「全部」要用 `'all'` 这种真值；
    ④ **Git Bash 里用 curl 传中文 JSON 会乱码**（存储挂载点建成了 `/�洢A`），
    起测试数据一律走 node 的 fetch，路径也要 `cygpath -w` 转 Windows 形式。

---

# 环境要点（新对话续接必读）

- 登录路由 **`/api/auth/login`**；搜索 **`/api/fs/search`**。e2e 账号 `admin` / `admin123`，
  被改掉用 `./webvid.exe reset-password admin123`。
- `player-check.mjs` 默认连 5299，跑本机要 `NL_BASE=http://localhost:5243 node player-check.mjs`。
- `files/文档/说明.md` 是 `e2e-check` 必需样本；`files/图片/壁纸/暖阳橘子海.jpg` 是
  `od-tail-check` 必需样本。两者都在 `files/`（不入库），缺了要补建。
- 改前端后**必须 `go build` 重嵌**（`go:embed`）；构建用 `npm run build`，**勿接管道**。
- 服务端日志里 `[thumb] 远端视频抽帧失败 … context canceled` 是良性噪音。
- 浏览器控制台 `ERR_ABORTED` 多为离页良性中断（photos-check 会打印一堆但 exit 0）。
- 用户本机 5243 实例是开发测试用的，可以随便起停。

# 文案口径（用户强要求）

对标 **Apple 软件更新说明**：简洁干练、克制有质感。
- 报错：说清「什么错 + 一句怎么办」，绝不甩英文/jargon/Go 类型名/`pkg:` 前缀。
- 更新日志：短句列点（`•`）、陈述式。**禁用** `①②③`、`——`铺陈、emoji、感叹号刷屏、
  「强大/无缝/轻松/贴心/助力」等营销词。
- 产品标准：反人类逻辑、跨功能不一致、卡顿、多余一步、无反馈、防呆缺失都要挑出来修。
