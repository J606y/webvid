# 技术债务整改 · 执行进度（交接）

> 配套方案见 `docs/REFACTOR-PLAN.md`。本文件是跨会话交接：记录已完成/已验证、未完成、验证方式、注意事项。
> 截至此存档：**全部改动未 commit**（工作区改动，git 未提交）。用户当时开新对话续接。

## 一句话状态

方案阶段一~四**全部完成并 e2e 验证**（含唯一真 bug 数据竞争、最大前端负债 hero 转场合并）；
阶段五/六**已全部完成并 e2e 验证**（本轮续接把剩余「高风险/低价值尾部」做完）：
FeaturedCarousel 抽出、MediaGridCard 抽出、Admin.vue 拆分、api.js 集中、accent/断点 token 化。
**唯一未做 = 阶段 6.2 的 UploadDrawer 409**（需后端先返错误码，属阶段 2.1 的后端延伸，前端暂缓）。
**全部改动仍未 commit**（工作区）。

## 已完成并验证 ✅

### 阶段一 · 止血
- **1.1 telegram 驱动 use-after-Drop 数据竞争（唯一真 bug）** — `internal/driver/telegram/telegram.go`：
  新增 `currentConn()`（锁内快照 `d.conn`、锁外做网络 I/O），`Drop()` 改为锁内置 nil，`Init` 赋值也上锁；
  `snapshot`/`message`/`getFile` 全部经 `currentConn()` 取连接，消除对 `d.conn` 的无同步读 + nil 解引用。
  新增 `internal/driver/telegram/race_test.go`（并发 Drop×currentConn，`-race` 用；本机无 gcc 跑不了 `-race`，
  但普通 `go test` 绿、`go vet` 过；**Linux CI 可跑 `-race`**）。**注**：`local.Drop` 经核实只 Close 不重置指针、
  `os.Root.Close` 对并发安全，非数据竞争，未动（有意）。
- **1.2 仓库卫生** — 删空残骸目录 `zz_tmpquery/`，`.gitignore` 加 `/zz_tmpquery/`。（`PROGRESS.md` 拆分未做，可选）

### 阶段二 · 撤错误字符串判定
- 新建 `internal/db/errcode.go` 的 `IsUniqueViolation`（`errors.As`+`modernc.org/sqlite/lib` 的
  `SQLITE_CONSTRAINT_UNIQUE`/`SQLITE_CONSTRAINT` 双匹配）。`user.go`(×2)、`handler_storage.go`(×2) 的
  `strings.Contains(err,"UNIQUE")` 改用之。
- `local.go` 的 `escapes from parent` 集中到 `isPathEscape()`，新增 `local_test.go`
  `TestPathEscapeSentinel`（真实 os.Root 越界 → 断言识别 + mapErr→ErrNotFound，锁死 stdlib 文案）。

### 阶段三 · Go 侧 DRY（子代理 sonnet 执行，已 build+test 复核）
- 新建 `internal/util/util.go`：`BoolInt` / `JoinLogical`（root "/"） / `JoinRel`（root ""）。
- `boolInt`×4 → `util.BoolInt`（index/user/hls/handler_storage）。
- 逻辑 join → `util.JoinLogical`（fs.go、index.go、handler_fs.go、handler_offline.go）。
- 相对 join → `util.JoinRel`（transfer.go、pikpak.go）。**onedrive.joinRel 语义不同（先 Trim），有意未动**。
- `thumb.go` 三处 singleflight → 提取 `func (s *Service) once(ctx, key)` 公共准入逻辑。
- telegram `sanitizeName` → 改名 `safeSegment`（与 handler_offline 的同名但语义不同者消歧）。

### 阶段四 · hero 转场合并（最大前端负债，e2e 30/30）
- `utils/heroDialog.js` 泛化为**单一实现 + options**，**向后兼容旧位置签名**（`useHeroDialog(selector, setClosed)`
  仍工作、Admin 三处零改动）。options：`anchor`(center/top) / `coverBig` / `chromeClass` / `cornerVar` /
  `reanchor` / `dur` / `fadeOutClose`。
- `VideoDetailCard.vue` 删除本地 ~176 行转场，改 `useHeroDialog({...card 配置...})` 委托；537→约 310 行。
- **杀双字面量**：CSS 时长与 setTimeout 收尾均由 `dur` 派生（原 `0.6s↔660`/`0.48s↔500` 等手动同步已除）。

### 阶段五/六 · 已做的安全子集
- **monet.js 抽出**：`utils/monet.js` 导出 `extractVibrant(img)`；VideoDetailCard 的 onArtLoad 变 3 行。
- **VideoCard 抽出**：`components/VideoCard.vue`（16:9 卡 + 播放钮 + 可选续播条 + 默认插槽副标题）；
  LibraryVideo 的最近添加/最近播放/网格三处近同结构统一为 `<VideoCard>`；卡片本体 CSS 迁入组件、
  页面只留 `.v-grid`/`.shelf-card` 布局。**注意**：grid 的历史/非历史副标题用**显式 v-if/v-else**（不靠空插槽回落，
  因 `v-if=false` 产出注释节点不触发 slot fallback）。
- **hasThumb / progressPct 集中**到 `utils/file.js`：Files/Search 删本地 `hasThumb`；LibraryVideo `pct` 与
  VideoDetailCard `resumePct` 用 `progressPct`。
- **Admin 加载错误 toast**：loadSite/loadStorages/loadDrivers/loadUsers 的 `catch(console.error)` 追加
  `ElMessage.error(...)`（357 secret 回显 → *** 是有意 graceful 降级、540 轮询防刷屏，两处保持静默）。
- **search-grid-check.mjs** 加 `NL_BASE` 支持（原只认 `process.env.BASE`）。

## 验证结果（对隔离实例 e2e，全绿除下述 1 项数据起因）

`go build ./... && go test ./...` **全绿**（telegram/local/db/server/fs/media/pikpak… 全过，含新增 2 个测试）。

前端 e2e（NL_BASE 指隔离实例）：**zoom 30/30、detail 15/15、mobile 37/37、history 12/12、progress 15/15、
search-grid 13/13、files-nav 11/11、photos-history 11/11、player 12/12、detect 3/3、photos-check ✔
（ERR_ABORTED 离页良性）**。

- **唯一「失败」= scroll-check 14/15 的「查看全部可深滚」**：隔离实例仅 18 视频、桌面网格短，深滚到不了
  阈值 y>200（得 y=137）。**非回归**：其余滚动断言全过、卡片正常渲染、脚本注释自承「样本少时滚不到」。
  用户内容更多的实例上应恢复绿。

## 本轮（续接会话）完成 ✅ —— 剩余尾部全做完

> 用户选「全部完成（含 Admin 拆分）」。做法均取「低风险实现」，每项 build+e2e 门控。

- **#2 FeaturedCarousel 抽出** — `components/FeaturedCarousel.vue`：LibraryVideo/Photos 的 `.feat` 横幅统一。
  差异经 `select` 事件（整块点击，$event.currentTarget=.feat-item 供转场锚）+ `action` 插槽（右下角按钮）注入。
  `.feat*` 容器样式随组件下沉；**`.hero-btn` 属插槽内容（父页作用域）故仍留 media-library.css**。
  `carousel` ref + `swipe` 不动 useMediaLibrary：组件 `defineExpose({next,prev})`，父绑 ref、`swipe` 作 prop。
- **#3 MediaGridCard 抽出** — `components/MediaGridCard.vue`：Files/Search 方格卡片统一。
  **原计划的「scoped 冲突」已过时**：`.g-card/.g-thumb/.g-name/.poster-grid` 早已上提 glass.css 全局（Files:393
  与 Search:126 注释为证）、`hasThumb` 已集中——故只剩模板抽取，无 CSS 迁移，低风险。`.folder` 色兜底进组件 scoped。
- **#5 Admin.vue 拆分** — `pages/admin/{AdminStorage,AdminUsers,AdminIndex}.vue`。**取实用低风险切法**：
  抽出三个大自足 pane（各自 **本地 `saving`** 消混线、各自 hero 弹窗、onMounted 自加载），
  **site/task 两小 Tab 留 shell 内联**（它们共用同一 `site` 对象 + `saveSite`；后端 `site_title` 必填、worker/限速
  为指针字段缺省保原值——拆开会碰这套共享保存语义，故不拆，零行为变更）。Admin.vue 618→~130 行。
  AdminIndex 收 `:active="tab==='index'"` prop，替代原 `watch(tab)` 的切 Tab 刷进度。
- **#4 api.js 集中** — 新 `utils/api.js`（按域 `api.fs/media/tasks/admin/auth...`）+ `utils/token.js`（单一
  getToken/setToken/clearToken，收 `nl_token` 3 处）。20 文件 call-site 迁移（子代理 sonnet 执行，逐条精确映射）。
  **有意保留**：`TextDrawer` 裸 axios（Range+responseType+transformResponse 透传，绕 baseURL 与响应解包）；
  `path.js` 的 rawUrl/thumbUrl/hlsUrl（媒体二进制 URL，token 走 query）。
- **#1 accent/断点 token 化** — glass.css `:root` 加 `--accent-rgb: 122,162,255;`，`--accent: rgb(var(--accent-rgb))`
  由分量派生（改色只一处）。16 处 `#7aa2ff`/`rgba(122,162,255,α)` → `var(--accent)`/`rgba(var(--accent-rgb),α)`
  （App/glass/media-library/VideoCard/VideoDetailCard/Play）。实测 `--accent`=rgb(122,162,255)、primary 按钮
  bg=rgba(122,162,255,0.32) 解析无误。断点：`viewport.js` 加 `MOBILE_BREAKPOINT=768` 单一 JS 源，heroDialog 复用之
  （**CSS @media 无法引用自定义属性，各 @media 仍是 768px 字面量**，与常量共同维护）。

**e2e（NL_BASE 指 5299 隔离实例）全绿**：mobile 37/37、zoom 30/30、detail 15/15、progress 15/15、files-nav 11/11、
search-grid 13/13、history 12/12、photos-history 11/11、photos ✔、detect 3/3、player 12/12；scroll 14/15（仅记录在案的
样本少「查看全部可深滚」；「返回后滚动位置保持」首跑冷启 flake，复跑即绿）。零控制台错误。

**offline-check**：本轮顺手修其**陈旧**（#33 把任务设置挪独立 Tab 后该脚本从未更新，且写死 5243）——加 `NL_BASE`
支持 + 切「任务设置」Tab + 保存按钮限定 `#pane-task`。修后 15 项过；**余 4 项（离线下载完成/文件落盘）失败=后端
SSRF 守卫（`internal/server/safedial.go`，committed 未改）拒绝下载回环地址 127.0.0.1**——脚本用 127.0.0.1:5321 起测试
源，与该守卫**天然冲突，非本次重构回归**（前端 `api.fs.offline` 提交路径正常：任务已建、命名正确）。

## 未完成（仅剩 1 项，需后端）

1. `UploadDrawer.vue` `includes('已存在')` 字符串判冲突 → 需后端返 409/错误码后前端按码分支（配阶段 2.1 的后端 409
   分流）。**属阶段 6.2、需先动后端**，本轮前端范围内暂缓。

## 如何续接 / 重新验证

```bash
# 1) 重建隔离实例二进制（前端改过必先 npm run build 再 go build 才重嵌）
cd frontend && npm run build && cd ..
go build -o webvid-e2e.exe .

# 2) 起隔离实例（端口 5299，数据在 temp，files 指现有样本；admin/admin123）
#    temp data 已残留一份（seed 过），删掉会自动重建
NL_PORT=5299 NL_DATA_DIR="C:/Users/v6488/AppData/Local/Temp/webvid-e2e-data" \
  NL_FILES_DIR="E:/桌面/newlist/files" NL_ADMIN_USER=admin NL_ADMIN_PASSWORD=admin123 \
  ./webvid-e2e.exe   # 后台跑

# 3) 跑 e2e（脚本支持 NL_BASE）
cd frontend && NL_BASE=http://localhost:5299 node zoom-check.mjs   # 等

# 4) 收尾：taskkill //PID <5299监听PID> //F（勿用 //IM，会误杀用户 5243 实例）
```

## 注意事项 / 坑

- **全部未 commit**。改动全在工作区。提交/发版按用户习惯委派下级模型。
- **e2e 用 Edge**（`C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe`）、playwright-core。
- **杀进程一律 taskkill //PID**，`//IM webvid.exe` 会误杀用户自己跑的 5243 实例。
- 本机**无 gcc**，`go test -race` 跑不了（Linux CI 可）。
- 阶段三由 sonnet 子代理执行，已 `go build`+`go test` 复核；`thumb.go` 无单测，`once()` 靠 build + 逐行比对。
- **本轮无任何 Go 改动**（纯前端 #1~#5 + 一处 e2e 脚本修陈旧）；Go 侧仍是阶段 1~3 的既有改动。
- **offline-check 与 SSRF 守卫冲突**：其 127.0.0.1 测试源被 `safedial.go` 拒绝，离线下载完成类断言在本机无法跑绿
  （非回归）；要真跑通需公网 URL 或临时放开回环——本机勿强跑。

## 改动文件清单（git status 快照，本轮后）

**本轮新增 (6)**：frontend/src/components/FeaturedCarousel.vue｜frontend/src/components/MediaGridCard.vue｜
frontend/src/pages/admin/{AdminStorage,AdminUsers,AdminIndex}.vue｜frontend/src/utils/api.js｜frontend/src/utils/token.js

**本轮改动 (前端)**：frontend/offline-check.mjs（NL_BASE+修陈旧）｜frontend/src/{App.vue, api/http.js,
assets/glass.css, assets/media-library.css, pages/Admin.vue, pages/Files.vue, pages/LibraryVideo.vue,
pages/LibraryPhotos.vue, pages/Play.vue, pages/Search.vue, components/{MoveCopyDialog,TasksDrawer,UploadDrawer,
VideoDetailCard}.vue, composables/useMediaLibrary.js, stores/{app,auth}.js,
utils/{path,heroDialog,lightbox,videoInfo,viewport}.js}

**先前遗留（阶段 1~4 + 5/6 子集，未 commit）**：.gitignore｜frontend/search-grid-check.mjs｜
frontend/src/{components/VideoCard.vue, utils/{file,monet}.js}｜docs/REFACTOR-{PLAN,PROGRESS}.md｜
internal/{db/errcode.go, driver/local/{local.go,local_test.go}, driver/pikpak/pikpak.go,
driver/telegram/{telegram.go,race_test.go}, fs/{fs,transfer}.go, index/index.go, media/hls.go,
server/{handler_fs,handler_offline,handler_storage}.go, thumb/thumb.go, user/user.go, util/util.go}
