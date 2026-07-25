# 上线后打磨进度（2026-07-25）

> 起因：用户「项目已正式上线，但很多地方还和正在开发中的项目一样，请扫描完善一下，
> 特别是报错语言改成能一眼看出是什么错、不要打哑谜」。
> 追加要求：①文案别一股 AI 味，**对标 Apple 软件更新说明——简洁干练**；
> ②**学 Apple 对细节吹毛求疵，以高标准要求产品**；③push 时注意什么该推什么不该推。

## 状态速览

| 批次 | 状态 |
|---|---|
| A. 报错人话化 + Apple 风文案 | ✅ **已提交 `38c6402`** |
| B. UX 反人类/不一致清单（28 项） | ✅ **26 项已修完并验证**；#19 用户明确不修；#20 按用户要求改成后台开关 |
| 提交 | ⚠️ **B 尚未提交**——4 个文件与另一会话的 WIP 混在一起，需用户拍板（见文末） |

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

# ⚠️ 待用户拍板：提交怎么切

工作区里有 **8 个文件属于另一会话未完成的 `copy_file_workers`（文件级并发转存）**：

```
go.mod  internal/conf/conf.go  internal/fs/fs.go  internal/fs/transfer.go
internal/fs/transfer_test.go  internal/server/handler_admin.go
internal/server/server.go  frontend/src/pages/Admin.vue
```

A 批次当时能干净分开，是因为改动完全没碰这 8 个。**这次碰了 4 个**：

| 文件 | 里面同时有 |
|---|---|
| `internal/conf/conf.go` | 别人的 `CopyFileWorkers()` + 我的 `MediaHomeSort()` |
| `internal/server/handler_admin.go` | 别人的 `copy_file_workers` 三处 + 我的 `settingsMap` 重构、`media_home_sort`、钳位回传 |
| `frontend/src/pages/Admin.vue` | 别人的「文件夹内并发」表单项 + 我的限速 `:max`、媒体库首页下拉、`saveSite` 回填 |
| `internal/fs/fs.go` | 别人的 import/struct/New 三处 + 我的 `accessOK`/`navOK` 走 `VisibleBase` |

而且这 4 个与另外 4 个（`transfer.go` / `transfer_test.go` / `server.go` / `go.mod`）**互相依赖**
（`handler_admin.go` 调 `s.fs.SetCopyFileWorkers`），拆开提交会直接编译不过。

三个选项：
1. **一起提交**（含别人的 `copy_file_workers`）——最省事，但把别人未完成的功能带进历史。
2. **只提交不冲突的 37 个文件**——但 #20（后台开关）依赖 `conf.go`/`handler_admin.go`，
   得连 `handler_auth.go` 一起排除；#24 的 fs 层旁路也会缺一半，功能半截。
3. **手工拆 hunk** 分两次提交——干净，但要逐块核对，出错风险最高。

**尚未执行任何提交。** 当前 HEAD = `38c6402`（A 批次）。

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
