# WebVid 技术债务整改方案

> 依据 2026-07-22 的全量代码扫描（Go 后端健康度 7.5~8/10、前端 6.5/10）。
> 结论：**非烂屎山**，负债集中在「横向重复」与「对框架内部 DOM 的紧耦合」两轴。
> 本方案按「风险 × 收益」分 6 个阶段，每阶段独立可交付、可回滚，改完各跑对应 e2e 脚本。

## 通用原则

- **有安全网就用**：`frontend/*.mjs`（mobile/detail/scroll/zoom/progress/photos…）+ `go test ./...`。每项改动后跑对应脚本，绿了再进下一项。
- **一次一件、单独提交**：本项目多会话并行编辑，历史上出现过「PROGRESS 标题行被吃、WIP 混入提交」。每个子项一个原子提交，提交信息标注 `#refactor-N`。
- **先止血、后重构**：阶段一是唯一真 bug + 零风险清理，应最先做；阶段五/六是纯质量投资，可择机。

---

## 阶段一：止血（真 bug + 仓库卫生）

### 1.1 【高·真 bug】驱动 use-after-Drop 数据竞争

**问题**：`fs.Reload()`（`internal/fs/fs.go:119-127`）在替换挂载表后对旧驱动调 `Drop()`，但 `findMount`/`Resolve`/`LinkEx` 返回裸 `*Mount.drv` 给请求方，无引用计数、无使用中保护。改存储（`afterStorageChange → Reload`）与进行中请求重叠时：

- Telegram 最致命：`Drop()`（`telegram.go:93-99`）**无锁**写 `d.conn = nil`，而 `snapshot()`（`telegram.go:127`）在释放 `d.mu` 后才读 `d.conn.client.API()` → **数据竞争 + 空指针 panic**。`go test -race` 必现。
- `local.Drop()` 同型（关了 `l.root` 后仍无锁使用），但落到「file closed」错误而非 panic，危害较轻。
- onedrive/pikpak 的 `Drop` 是 no-op，实害小。

**方案 A（推荐·contained）——修 telegram 自身的锁纪律**，消除 UB，改动只在一个包内：

```go
// telegram.go：所有对 d.conn 的读写都收进 d.mu；网络调用只在锁内取指针、锁外执行。
func (d *Telegram) client() (*tg.Client, error) {
    d.mu.Lock()
    c := d.conn
    d.mu.Unlock()
    if c == nil {
        return nil, ErrNotLoggedIn // 驱动已 Drop：干净报错，不再 nil deref
    }
    return c.client.API(), nil
}

func (d *Telegram) Drop() error {
    d.mu.Lock()
    c := d.conn
    d.conn = nil
    d.mu.Unlock()
    if c != nil {
        c.close()
    }
    return nil
}
```

- `snapshot`/`message`/`getFile` 里 `d.conn.client.API()`、`d.conn.invoker(...)` 全部改为先经 `client()`/新增的 `invoker()` 取到本地指针再用。
- 锁内只捕获指针、锁外做网络 I/O：即便 Drop 并发关闭了连接，被捕获的 client 在途调用只会返回 error（gotd 对已关连接安全），不再 panic。

**方案 B（彻底·可选）——在 `fs` 层加借用守卫**，根治所有驱动：给 `Mount` 加原子引用计数 + `dropped` 标记，`borrow()`/`release()` 包裹驱动调用，`Reload` 换表后对旧挂载「引用归零再 Drop」。

- 代价：`LinkEx` 返回的 `Provider` 闭包在流式下载期间要一直持有借用，生命周期跨越整个下载，需把 release 挂到流的 Close 上（`stream` 层）。改动面大。
- 建议：**先做方案 A**（消 UB，1~2h），方案 B 作为后续加固单列（本文档不展开代码，仅记录方向）。

**验证**：新增 `telegram` 并发用例（一 goroutine 循环 `snapshot`、另一 goroutine `Drop`），`go test -race ./internal/driver/telegram/`。顺手给 `local.Drop` 同样纪律。

### 1.2 【低】仓库卫生

- **删 `zz_tmpquery/`**：空残骸目录，未被 ignore。`rm -rf zz_tmpquery`，并在 `.gitignore` 补 `/zz_tmpquery/`（或直接删不留痕）。
- **拆 `docs/PROGRESS.md`（1158 行/118KB）**：按里程碑/月份归档到 `docs/progress/`，主文件只留最近条目 + 索引。降低并行编辑「标题行互吃」的事故面。
- 复核 `_shots/`、`webvid.exe`、`webvid-linux-*` 均已 ignore（已确认 OK，无需动）。

---

## 阶段二：撤除「用错误字符串控制流程」

库/标准库改文案就会静默错分流。集中判定 + 类型化。

### 2.1 SQLite UNIQUE 违反（`user.go:81,133`、`handler_storage.go:148,193`）

新建 `internal/db/errcode.go`（与 db 同包，天然可见）：

```go
package db

import (
    "errors"
    "modernc.org/sqlite"
    sqlite3 "modernc.org/sqlite/lib"
)

// IsUniqueViolation 报告 err 是否为唯一约束冲突（用于把 409 从 500 中分出）。
func IsUniqueViolation(err error) bool {
    var se *sqlite.Error
    if errors.As(err, &se) {
        c := se.Code()
        return c == sqlite3.SQLITE_CONSTRAINT_UNIQUE || c == sqlite3.SQLITE_CONSTRAINT
    }
    return false
}
```

四处 `strings.Contains(err.Error(), "UNIQUE")` 改为 `db.IsUniqueViolation(err)`。

### 2.2 `os.Root` 越界（`local.go:85`）

Go 标准库未导出该 sentinel，暂无法类型化。改为**集中到一个 helper + 用测试锁死前提**：

```go
// local.go：只此一处依赖文案，配单测监视 stdlib 文案变更。
func isPathEscape(err error) bool {
    return err != nil && strings.Contains(err.Error(), "escapes from parent")
}
```

新增单测：构造一个真实越界路径调 `os.Root.Open`，断言 `isPathEscape` 为真——将来 Go 改文案时该测试先红，给出信号。

**验证**：`go test ./internal/user/ ./internal/server/ ./internal/driver/local/`。

---

## 阶段三：Go 侧 DRY 收拢（防「只改一处」漂移）

新建 `internal/util`（放无依赖的纯函数），逐项迁移：

| 重复项 | 现状 | 收拢到 |
|---|---|---|
| `boolInt` ×4 | `index:309` `user:168` `hls:239` `handler_storage:230` 完全同实现 | `util.BoolInt` |
| 逻辑路径拼接 4 名 6 处 | `fs.joinPath` `index.joinPath` `transfer.joinRel` `onedrive.joinRel` `pikpak.pathJoin` `handler_fs.joinLogical` | `util.JoinLogical(dir, name)`，统一 `/` 特例；各处替换 |
| singleflight 块 ×3 | `thumb.go` `Get:148` `remote:203` `remoteVideoFrame:263`，每处 ~20 行 | `thumb.go` 内提一个 `func (s *Service) once(key string, ctx, fn) (string, bool)` |
| ffmpeg 探测 ×2 | `thumb.FFmpeg():79` 与 `media/probe.LookTool():25`（PATH+winget 同逻辑） | 二选一导出，另一处调用；建议留 `media`，thumb 依赖它 |
| ffmpeg 抽帧 `try("3")/try("0")` ×2 | `hls.FrameJPEG:127` 与 `thumb.genVideo:377` 近同 | 抽到共享 `media` 的 `FrameAt(ctx, in, out, w, offsets...)` |
| HTTP 重试/退避/换链 ×4 | `onedrive/graph:233` `pikpak/client:249` `stream/serve:175` `stream/accel:193` | **较大，单列**：抽 `internal/httpx.Do(ctx, req, policy)`，policy 描述「401/403/404/410→换链、429/503→Retry-After 待、预算内不计次」 |
| `sanitizeName` 语义不同 ×2 | `telegram:530`（`_` 替换）vs `handler_offline:149`（删除） | **改名消歧**：`telegram.sanitizeName` → `safeSegment`；不要强行合并（行为不同） |

前 5 项低风险、机械替换，一次做完。HTTP 重试合并收益大但改动面广，建议阶段三尾单独立项，配 `httpx` 单测覆盖各策略分支。

**验证**：`go test ./...` 全绿（现有 driver/media/stream 测试即覆盖）。

---

## 阶段四：前端 hero 转场合并（前端最大负债）

`utils/heroDialog.js`（137 行）与 `VideoDetailCard.vue:172-348`（~176 行）是同机制两份拷贝，卡片版已分叉出：顶边锚 `originTransform`、大横幅非等比 `coverTransform` + 淡入淡出、`vdc-closing` chrome 淡出、`--vdc-art-r` 圆角补偿、关场按轮播容器再锚定。一边修 bug 传不到另一边。

**方案**：把 `heroDialog.js` 泛化为带 options 的单一实现，卡片委托它。

```js
// utils/heroDialog.js  —— 单一真源，选项覆盖差异
export function useHeroDialog({
  selector,                 // 弹窗定位，如 '.el-dialog.vdc'
  setClosed,                // 非 EP 关闭兜底（取消/保存成功）
  anchor      = 'center',   // 'center'（按钮）| 'top'（缩略图顶边对齐）
  reanchor,                 // (originEl) => Element：关场回位锚（卡片传：轮播容器优先）
  coverBig    = false,      // 来源大于卡片时非等比贴四边 + opacity 淡入淡出（横幅）
  chromeClass,              // 关场附加类做 chrome 淡出（卡片传 'vdc-closing'）
  cornerVar,                // 圆角补偿 CSS 变量名（卡片传 '--vdc-art-r'，基值 12）
  dur = { in: 440, close: 340 }, // 时长单一真源 → 下文 timer 由它 +补偿 派生，杜绝双字面量
}) { /* zoomIn / animatedClose / cancelClose / open，逻辑同现 heroDialog，
       在 big / chromeClass / cornerVar / reanchor 处按选项分支 */ }
```

- `VideoDetailCard.vue`：删除本地 176 行转场，改 `const { open, animatedClose, cancelClose } = useHeroDialog({ selector:'.el-dialog.vdc', anchor:'top', coverBig:true, chromeClass:'vdc-closing', cornerVar:'--vdc-art-r', reanchor: el => el.closest('.el-carousel')||el })`。
- `Admin.vue`（后台弹窗）保持现调用不变（默认 `anchor:'center'`）。

**顺带杀掉「时长 × setTimeout 双字面量」**（`0.6s↔660`、`0.48s↔500`、`0.44s↔470`、`0.34s↔360`）：时长只在 `dur` 定义一次，transition 字符串用 `${dur.in}ms`，收尾定时器用 `dur.in + 60`。从此改一个数不会转场错乱。

**验证**：`zoom-check.mjs`（26 项，含开/关转场态、磨砂暂停、关场滚动解锁、横幅点击、reduced-motion）+ `detail-check`。这是全项目 e2e 覆盖最密的地方，是合并的安全网。合并后转场中间帧截图 `_shots/zoom-t*.png` 目视复核。

---

## 阶段五：前端组件拆分（消灭巨型组件与三连拷贝）

### 5.1 `VideoDetailCard.vue`（537 行 → 目标 <250）

- **抽取莫奈调取色** `onArtLoad:94-121` + `vibrant:124-152`（canvas 分桶 + HSL，纯函数）→ `utils/monet.js` 导出 `extractVibrant(img) => {r,g,b}`。可单测。
- **转场**已在阶段四委托给 `heroDialog`。
- 残留只剩详情卡模板 + 信息 fetch + 续播进度。

### 5.2 `Admin.vue`（614 行 = 实质 5 组件）

- 按 Tab 拆 `AdminSite.vue` / `AdminStorage.vue` / `AdminUsers.vue` / `AdminTelegram.vue` / `AdminIndex.vue`，各自异步加载。
- **消除共享 `saving` ref 混线**（`:367` 存储保存 与 `:489` 用户保存共用一个 ref）：各组件本地 `saving`。

### 5.3 卡片/轮播子组件抽取

- `VideoCard.vue`：消 `LibraryVideo.vue` 同文件 3 连拷贝（最近添加 `:60-70` / 最近播放 `:86-97` / grid `:113-127`）。
- `FeaturedCarousel.vue`：消 `LibraryVideo`/`LibraryPhotos` 首屏轮播 ~18 行重复模板。
- 网格卡（`.g-card/.g-thumb/.g-name`）：现于 `Files`/`Search`/`LibraryPhotos` 各自 scoped 重定义 → 抽 `MediaGridCard.vue` 或把样式提到 `media-library.css` 全局 + 一个组件。
- `hasThumb`（`Files:281` 与 `Search:110` 完全一致）、进度率 `Math.min(100, round(pos/dur*100))`（`LibraryVideo:185` 与 `VideoDetailCard:77`）→ `utils/file.js` / `utils/media.js`。

**验证**：`files-check`/`search-grid-check`/`photos-check`/`history-check`/`scroll-check` 逐个跑。拆分不改行为，脚本应零变更即绿。

---

## 阶段六：前端健壮性与去硬编码

### 6.1 集中 API 层（`utils/api.js`）

- 30+ 个 endpoint 字符串散在各组件 → 收成 `api.media.list(params)` / `api.tasks.list()` / `api.admin.settings.save(...)` 等。改路径一处改。
- `nl_token` 字面量 3 处（`http.js:8`/`path.js:21`/`auth.js`）→ 单一 `token()` 访问器。
- `TextDrawer.vue:19` 绕过共享 http 用裸 `axios`（为 Range 头）→ 改用共享实例传 `headers:{Range}`，恢复 auth/error 拦截一致性。

### 6.2 错误处理

- `Admin.vue` 的 `loadSite/loadStorages/loadUsers/...` 现 `catch(e){console.error}` → 失败无提示空白页。改为 `ElMessage.error` 给用户可见反馈。
- `UploadDrawer.vue:95` `e.message.includes('已存在')` 判冲突 → 后端返错误码/HTTP 409，前端按码分支（配合阶段 2.1 的 409 分流）。

### 6.3 去魔法值

- 断点 `768`（CSS 15+ 处 + JS `innerWidth>768` + `viewport.js`）→ `useBreakpoint()` composable 或 CSS 变量 + JS 单一常量。
- 缓动 `cubic-bezier(0.32,0.72,0,1)`、accent `#7aa2ff`/`rgba(122,162,255,…)`（有 `--accent` 却散 15+ 生值）、轮询 `1500ms`、z-index 裸数 → 收进 design token / 常量文件。

**验证**：`mobile-check`（37 项）+ 各页 e2e。

---

## 排期与优先级建议

| 阶段 | 内容 | 收益 | 风险 | 粗估 | 建议 |
|---|---|---|---|---|---|
| 1.1 | telegram Drop 锁纪律（方案 A） | 消唯一真 bug | 低 | 1~2h | **立即** |
| 1.2 | 删 zz_tmpquery + 拆 PROGRESS | 卫生 | 极低 | 0.5h | 立即 |
| 2 | 撤错误字符串判定 | 抗版本升级 | 低 | 2~3h | 高 |
| 3（前5项） | boolInt/join/singleflight/ffmpeg 收拢 | 防漂移 | 低 | 3~4h | 中高 |
| 3（HTTP重试） | httpx 统一 | 防漂移 | 中 | 0.5~1d | 中 |
| 4 | hero 转场合并 + 杀双字面量 | 前端最大负债 | 中（有 e2e 兜底） | 0.5~1d | **高** |
| 5 | 组件拆分 + 卡片抽取 | 可维护性 | 中 | 1~2d | 中 |
| 6 | API 层 + 错误提示 + 去魔法值 | 一致性 | 低~中 | 1~2d | 中 |

**最小可行首刀**（半天内）：1.1 方案 A + 1.2 + 阶段 2。消掉唯一真 bug、清仓库、去掉最脆的错误字符串判定，全程有 `go test -race` 与现有 e2e 兜底。
