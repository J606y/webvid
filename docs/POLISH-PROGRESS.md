# 上线后打磨进度（2026-07-25）

> 起因：用户「项目已正式上线，但很多地方还和正在开发中的项目一样，请扫描完善一下，
> 特别是报错语言改成能一眼看出是什么错、不要打哑谜」。
> 追加要求：①文案别一股 AI 味，**对标 Apple 软件更新说明——简洁干练**，
> 「我们要做苹果项目不做安卓项目」；②**学 Apple 对细节吹毛求疵，以高标准要求产品**；
> ③push 时注意什么该推什么不该推，别一股脑全推上去。

## 状态速览

| 批次 | 状态 |
|---|---|
| A. 报错人话化 + Apple 风文案 | ✅ **代码完成、全量验证通过、二进制已重编、未提交** |
| B. UX 反人类/不一致清单（28 项） | 📋 **已查清待决策**——用户选择「先保存进度，新开对话继续」 |

---

# A. 已完成：报错人话化（未提交）

## 根因
上一轮「报错人话化」（v1.9.1 / `8e1f13e`）只覆盖了**同步 HTTP 响应**（`resp.go` 的 `humanize`），
**漏掉三处异步任务**——它们全是裸 `err.Error()`：

- `internal/task/task.go:211` → 转存/复制/离线/上传任务失败（TasksDrawer 显示）
- `internal/index/index.go:82` → 索引重建失败（AdminIndex 显示）
- `internal/preload/preload.go:205` → 媒体预载失败（AdminIndex 显示）

所以任务一失败，用户看到的是 `context deadline exceeded`、`connection refused`、ffmpeg 英文 stderr。

## 改动清单

**新增 `internal/util/humanize.go`（`util.Humanize`）**
从 `resp.go` 抽出并扩充关键词表：网络（超时/DNS/拒连/中断/证书/代理）、系统（磁盘/权限）、
接口授权（HTTP 429·FLOOD_WAIT 限流 / HTTP 401·invalid_grant 授权失效 / 403 / 404）。
**兜底策略改为**：已含中文 → 原样返回（项目自己写的错误已说清）；纯英文 → 包一层「操作失败：」。
中文判定用 `containsCJK`（`unicode.Is(unicode.Han, r)`）。
测试从 `internal/server/resp_test.go`（已删）搬到 `internal/util/humanize_test.go` 并补新分类。

**三处异步任务接入 Humanize**，原始错误改进服务端日志。
`task.go` 新增 `log.Printf("[task] 任务 %s 失败: %v")` —— **这条日志当场帮我定位了 offline-check 的失败真因**。

**驱动前缀正名（41 处，由 sonnet 子代理批量执行）**
`telegram:` → `Telegram：`、`onedrive:` → `OneDrive：`、`onedrive_app:` → `OneDrive（应用授权）：`、
`pikpak:` → `PikPak：`、`googledrive:` → `Google Drive：`。
小写包名前缀是「像开发中项目」的头号信号。`log.Printf` 一律不动。

**去 jargon**
- `internal/driver/onedrive/onedrive.go:566` `errors.New("empty body")` → 「OneDrive：返回了空响应，请重试」
- telegram 3 处 `%T`（甩 Go 类型名）→ 中文（`telegram.go:233,499`、`login.go:167`）
- `reader.go:72,75` `非法 whence` / `偏移为负` → 「内部读取错误（…）」

**Telegram 登录错误码翻译（新增 `tgAuthError`，`login.go`）**
用 `tgerr.Is` 映射：PHONE_CODE_INVALID → 「验证码不正确，请重新输入」、PHONE_CODE_EXPIRED、
PASSWORD_HASH_INVALID、PHONE_NUMBER_INVALID、PHONE_NUMBER_BANNED、API_ID_INVALID。
未知码保留原始错误交上层 Humanize（FLOOD_WAIT 等走限流分支）。
需 import `github.com/gotd/td/tgerr`。

**云盘 OAuth 长错误压缩（本次实测发现的真问题）**
- 新增 `aadMessage`（`onedrive/graph.go` 尾部）：AAD 把 `Trace ID / Correlation ID / Timestamp`
  塞在**同一行**，老的「取首行」截不掉 → 常见 AADSTS 码翻人话，未知码保留首句（含码便于检索）只丢噪音。
  **实测对比**：`AADSTS9002313: Invalid request. Request is malformed or invalid. Trace ID: 9b65c044-…
  Correlation ID: … Timestamp: …`（约 200 字符）→ **「凭据格式有误，请检查 client_id / client_secret / refresh_token」**
- 新增 `oauthMessage`（`googledrive/client.go` 尾部）：invalid_grant / invalid_client /
  unauthorized_client / invalid_scope 翻人话；invalid_grant 提示「若 OAuth 应用仍是『测试』状态，
  令牌约 7 天过期，建议发布为『生产』」。

**ffmpeg stderr 不再甩给用户**
- `internal/media/hls.go:466` 转码失败 → 日志 + 「转码失败：该视频可能已损坏或格式不受支持」
- `internal/server/handler_offline.go:278` HLS 合并失败 → 日志 + 「合并视频失败：源地址可能已失效或格式不受支持」
- `internal/media/probe.go:95,99` `ffprobe 探测失败` → 「读取视频信息失败」/「解析视频信息失败」

**服务端字段名中文化（21 处，sonnet 子代理执行）**
不再暴露英文接口字段名：`q 不能为空` → 「请输入搜索关键词」、`kind 必须是 video 或 image` →
「媒体类型无效（应为视频或图片）」、`paths/dst_dir 不能为空` → 「未选择文件或目标目录」、
`参数错误` → 「请求参数有误」、`参数不完整` → 「用户名不能为空」、`未知驱动:` → 「不支持的驱动类型：」等。
涉及 handler_search / handler_fs / handler_offline / handler_video / handler_media / handler_user /
handler_raw / handler_storage。
⚠️ `paramID` 是**共享** helper（storage/telegram/googledrive 路由都用），所以保持通用文案「无效的 ID」，
别改成「无效的用户 ID」（子代理一度改成那样，已回退）。

**两个授权 handler 的 `err.Error()` 改走 Humanize**
`handler_telegram.go`（send_code / sign_in 的 502）、`handler_googledrive.go`（回调页）。
另：「存储配置损坏: 」+ err → 「存储配置已损坏，无法读取」；
GD 回调 `授权被拒绝或取消：access_denied` → 「授权未完成：你在 Google 页面取消或拒绝了授权，请回后台重新点「授权」」。

**resp.go 去 AI 味（按 Apple 口吻改短）**
- ErrQuota：原三行长文 → 「存储空间已满，写入失败。有时显示仍有剩余也会这样，多半是账号配额或授权受限。」
- ErrDenied：→ 「存储拒绝写入：当前账号没有写入权限，请重新授权（OneDrive 需勾选 Files.ReadWrite）。」

**前端（只有两处；其余前端「问题」后端一改就自动干净了）**
- `TextDrawer.vue:65`（唯一确凿的前端 bug）：它用**裸 axios**绕过拦截器，`e.message` 是原始英文
  （`Request failed with status code 404`）且被当作**文件正文**渲染 → 改为固定中文
  「文件加载失败，请稍后重试，或点下方「下载」保存到本地查看」。
- `MoveCopyDialog.vue:72`：`errs.join('；')` 加前缀「部分项目未完成：」。
- `AdminIndex.vue:12,29`（`progress.err`/`preload.err`）与 `TasksDrawer.vue:18`（`t.error`）
  **无需改前端**——后端接入 Humanize 后这些字段已是中文。

## 验证结果

- `gofmt` / `go vet` 干净；`go test ./...` **全绿**（含新增 `internal/util` 包测试）。
  注：`internal/driver/telegram/telegram_test.go` 本来就未 gofmt 格式化，非本次引入，未动。
- 前端 `npm run build` 通过；`go build -o webvid.exe .` 重编完成（已嵌新前端）。
- **e2e 回归全绿**（服务跑 5243，用新二进制）：
  mobile 37/37、zoom 30/30、photos 19/19、progress 15/15、detail 15/15、scroll 15/15、
  player 12/12、history 12/12、files-nav 11/11、photos-history 11/11、search-grid 13/13、
  upload-conflict 8/8（含 409 冲突链路）、detect 3/3；`e2e-check` 全流程 exit 0（含 md 抽屉）。
- **两个脚本失败，均非本次引入**：
  - `offline-check` 4 项：`internal/server/safedial.go`（**初始提交就有**的 SSRF 防护）
    拒绝内网地址，而脚本源服务器是 `127.0.0.1:5321`。其豁免钩子 `offlineDialControl`
    只对 Go 单测（httptest）有效，对浏览器 e2e 无效。**要修得改脚本**（换非回环源或加豁免开关）。
  - `secret-echo-check`：崩在第 36 行 `list.find(s => s.driver === 'onedrive' && s.config.client_secret)`
    —— 环境无 onedrive 存储且脚本缺 skip 防御。已用临时假存储手工验证：列表脱敏 `***` ✅、
    单条明文 ✅、驱动前缀生效 ✅（验完已删该临时存储）。
- 缩略图 404 是**设计行为**（`handler_video.go:120` 注释：不可用一律 404，前端回落占位），非问题。

## ⚠️ 提交注意（用户明确问过「push 该推什么」）

工作区有 **8 个别的会话未做完的文件**（功能：跨存储转存的**文件级并发** `copy_file_workers`）：

```
go.mod  internal/conf/conf.go  internal/fs/fs.go  internal/fs/transfer.go
internal/fs/transfer_test.go  internal/server/handler_admin.go
internal/server/server.go  frontend/src/pages/Admin.vue
```

**已逐个 grep 验证：这 8 个文件里零污染**（我的报错改动一行都没混进去），可干净分开提交。

规矩：
- **绝不用** `git add -A` / `git add .` / `git commit -am`，只精确 `git add <文件>`。
- 二进制/产物/数据不用担心：`.gitignore` 已挡 `*.exe`、`webvid-linux-*`、`data/`、`files/`、
  `public/dist/`、`_shots/`、`*.log`、`node_modules/`。
- 本次新增未跟踪文件需 add：`internal/util/humanize.go`、`internal/util/humanize_test.go`。
- 本次删除需 add：`internal/server/resp_test.go`（测试已搬到 util 包）。
- 推送时机：用户点头才推，且只在发版时推。

---

# B. 待决策：UX 反人类 / 不一致清单（28 项）

由 3 个只读子代理分簇走查（文件操作簇 / 内容浏览播放簇 / 后台管理账户簇），逐条追代码确认。
用户已看过汇总，选择「先保存进度，新开对话继续」。**尚未做任何 UX 修改。**

## 第一层：真 bug（会造成实际损失，建议必修）

| # | 位置 | 问题 |
|---|---|---|
| 1 | `AdminUsers.vue:63-65` / `handler_user.go:84-127` / `user.go:124-132` | **管理员能停用/降级自己 → 自锁死**。后端 `userUpdate` 只查「最后一个管理员」，不查「改的是不是当前账号」。保存成功 → 紧接的 `loadUsers()` 被中间件判 401 → 踢回登录页 → **再也登不进**。对比：删除按钮对自己 `:disabled` + 后端 400 双保险（`handler_user.go:135-137`），唯独停用/降级没防呆 |
| 2 | `handler_media.go:151`（触发点 `Play.vue:169-170`） | **拖到 96% 就被判「看完」**。`≥95% 或剩 ≤5s 归零`对**每一次**上报生效，含 `seeked`/`pause`。只是拖到结尾瞄一眼，续播点就被清零 → 进度条消失、「继续观看」退回「立即播放」。应只在 `video:ended` 时判完成 |
| 3 | `Play.vue:134-172` | **HLS 运行期失败无兜底**。挂了 hls.js 但**没有 `Hls.Events.ERROR` 处理**，转码会话中途挂掉/分片 500 → 播放器永远转圈，没有「下载原文件」逃生口（而探测期失败反而有 `unsupported` 兜底） |
| 4 | `Admin.vue:40-51,105-110` / `handler_admin.go:53-55` / `conf.go:107-109` | **限速框无 `:max`**。后端静默钳到 `1<<20`(1048576)，`saveSite` 又不回拉 settings → 填 99999999 提示「已保存并生效」，表单仍显示 99999999，**界面值 ≠ 生效值**。并发线程框有 `:max` 不受影响。⚠️ 修这条会碰 WIP 文件 `Admin.vue`/`handler_admin.go`/`conf.go` |
| 5 | `LibraryVideo.vue:90` + `handler_media.go:82` | **「所有视频」网格进度条永不显示**。网格挂了 `show-progress`，但数据源 `/media/list` 的 SELECT 只回 path/name/size/modified（无 position/duration）→ `progressPct` 恒 0，死代码。同一视频在「最近播放」货架有进度条、进网格就没有。修法二选一：`/media/list` LEFT JOIN `play_history`，或去掉无效的 `show-progress` |

## 第二层：明显反人类（改法无争议）

| # | 位置 | 问题 |
|---|---|---|
| 6 | `Files.vue:87-95` + `MediaGridCard.vue:5-16` | **方格视图下管理操作全消失**。多选/重命名/删除/移动/复制/下载**只在列表视图**（`Files.vue:33-38,50-79`）。而 `viewMode` 是持久化全局偏好（`stores/app.js:9`），切到方格后跨会话记住 → 用户被迫切回列表才能操作，无任何提示 |
| 7 | `TasksDrawer.vue:79-97` vs `Files.vue:335-342` | **取消/删除任务零确认**。删文件有 `ElMessageBox.confirm`（「此操作不可恢复」），取消任务/删记录/清已成功一点即执行。取消进行中的离线下载会丢已下载进度（离线**无断点续传**）。且「取消进行中」与「删除已完成记录」长得一样 |
| 8 | `MoveCopyDialog.vue:41-58` + `fs/transfer.go:50` / `fs.go:486` | **能把文件夹移进它自己**。目标树不排除源自身/其父目录/其子目录；只有 local 驱动在服务端挡了自嵌套（`local.go:243-258`），云盘驱动与 `fs.Transfer` 无此校验。建议 UI 禁用源子树 + `fs` 层统一加「目标不能是源或源的子路径」 |
| 9 | `handler_fs.go:93-104` + `Files.vue:335-342` | **多选删除首个出错即 return**（前面的已删、索引已改），前端此时**不刷新**、只弹一个「失败」toast → 列表仍显示已被删掉的项。建议后端逐项收集结果返回，前端无论成败都 `load()` |
| 10 | `NameDialog.vue:57-63` | **先关弹窗再发请求**。服务端失败（重名/无权限）只弹 toast，弹窗已关、输入丢失，得重开重输。客户端校验错误却会留住弹窗 → 两种错误体验割裂 |
| 11 | `Files.vue:72-79` | **行内有重命名/下载，唯独没有删除**。删单个文件要先勾复选框再去工具栏，同为单项操作入口不一致 |
| 12 | `MoveCopyDialog.vue:64-83` + `Files.vue:180-194` | **跨存储转存/离线完成后列表不刷新**。MoveCopyDialog 在「建任务」后就 `emit('done')→load()`（此刻任务还没跑完），`submitOffline` 干脆不 load → 操作「成功」却看不到变化 |
| 13 | `Files.vue:50,244` + `33-38` | **切换列表/方格不清 `selection`**。工具栏仍显示「已选 N 项」但方格无选中标记 → 对**看不见的选中集**执行删除；切回列表则表格空勾选而 `selection` 仍是旧值 |
| 14 | `AdminStorage.vue:35-67,179-193` / `handler_storage.go:129-138,161-174` | **存储表单必填项不校验**。el-form 无 `:model`/`:rules`/`ref`，`saveStorage` 不 `validate()`；后端只校验 mount_path/driver，**不校验驱动自己的 required 字段**（如 client_id）。红星是摆设：留空也「保存成功」，实则挂载不可用。对比用户表单/站点表单都有校验 |
| 15 | `LibraryVideo.vue:32` / `LibraryPhotos.vue:31` | **库页首屏无加载反馈**。`loaded=false` 时页面基本空白（无 Featured、无货架、也无空态），慢存储下像白屏；而文件管理有明确「加载中…」（`Files.vue:81,93`） |
| 16 | `http.js:43-52` / `Login.vue:47-49` | **登录失败弹两次 toast**（非 401 时）。拦截器弹一次 + Login.vue catch 又弹一次。401 因拦截器在登录页不弹而是单次 → 不一致 |

## 第三层：设计取舍（需用户拍板，勿擅自改）

| # | 位置 | 取舍点 |
|---|---|---|
| 17 | `Search.vue:98` | 点图片结果 → 跳 `/library/photos?dir=父目录` 展示整个文件夹，**不打开你点的那张**。而文件管理里点图片直接开灯箱（`Files.vue:288-291`） |
| 18 | `Search.vue:99` | 点 pdf/txt/md/audio → 只跳到所在文件夹，**不预览**。文件管理里会直接预览（`Files.vue:296-306` 的 dispatch）。可考虑把 dispatch 抽成共享函数 |
| 19 | `LibraryVideo.vue:149-152` vs `Files.vue:294` / `Search.vue:97` | **播放入口分叉**：媒体库点视频弹详情卡（两步），文件/搜索点视频直接播（一步）。是刻意分工还是该统一？ |
| 20 | `useMediaLibrary.js:71-72` + `LibraryVideo.vue:79`/`LibraryPhotos.vue:72` | **「所有视频/所有照片」实为 `sort=random` 抽 200 条**，每次进首页重抽。「所有」语义暗示完整稳定，旁边还有「查看全部」才是真完整列表 → 易误解。改名「随机浏览/发现」？还是固定排序？ |
| 21 | `LibraryPhotos.vue:110-125` vs `LibraryVideo.vue:125-141` | **照片墙没有「最近添加」**（视频库有三条货架，照片墙只有两条）。刚上传的照片无快捷入口。是否补齐对称？ |
| 22 | `AdminStorage.vue:277-292` vs `74-102,256-273` | **Google Drive 授权的「已完成」是假确认**。开新标签跳 OAuth，前端只弹 confirm，两个按钮都只 `loadStorages()` → 没真授权也能点，点完无任何「尚未完成」提示。Telegram 流程是弹窗内闭环、有明确成功反馈。建议回调页 `postMessage`/轮询状态 |
| 23 | `index.go:149` / `handler_storage.go:119-126` | **索引重建全量清表**：`DELETE FROM files` 后从零回填，期间（云盘可能几分钟）搜索/媒体库**查不到东西**；且改任一存储都触发全量重扫。真实文件不受影响。是否投入「临时表原子替换 / 按挂载增量」？至少给前端「重建中，结果暂不完整」提示 |
| 24 | `fs.go:163-167`（`accessOK`/`navOK`） | **`base_path` 对管理员也生效，无 admin 旁路**。给某管理员设了非 `/` 会静默限制他自己的视野；而弹窗里 base_path 对 admin 可编辑、`can_write` 反被禁用（`AdminUsers.vue:57-61`）。管理员该不该无视 base_path？ |
| 25 | `handler_storage.go:224-230` vs `119-126` | **「重载」vs「保存」语义不一致**：per-row 的「重载」按钮实际 `s.fs.Reload()` **重载全部挂载且不重建索引**；而保存/删除走 `afterStorageChange` 会 Reload + Rebuild。修好失败挂载后点重载，索引不刷新、新文件搜不到 |
| 26 | `AdminStorage.vue:82-83,229-255` / `login.go:131-149` | **TG「发送验证码」文案与行为不符**：关掉弹窗再重开，按钮文案回到「发送验证码」，但后端若仍有未过期 pending，实际走 `resendCode` **切换投递通道**。有 fallback 故多数无害 |
| 27 | `handler_offline.go:54-64` | **离线下载一条 URL 非法就整单 400**（不创建任何任务，但错误会点名坏 URL）。粘 10 条坏 1 条要全部重来。是否改成「跳过非法、创建其余并回报被跳过的」？ |
| 28 | `UploadDrawer.vue:22-24,126-129` + `Files.vue:172,197-199,43-45` + `handler_fs.go:180-205` | ①**上传同名冲突只给「覆盖上传」**，无「重命名/跳过/全部覆盖」，批量冲突要逐个点。②**上传与传输任务是两套系统**：上传走同步 PUT、进度只在「上传队列」抽屉，**不进传输任务抽屉、不计 Van 徽标** → 关掉抽屉后全局无指示。彻底统一改动较大（浏览器直传 vs 服务端拉流），低成本折中=徽标计入上传 + 两抽屉互相可达 |

## 子代理确认「不是问题」的部分（避免返工）
- 删除类确认策略一致到位（删存储 confirm 且注明「文件本身不会被删除」、删用户 confirm、删自己双保险）。
- 会话过期不会卡白屏（拦截器 `clearToken()` + 硬跳 `/login`；路由守卫兜底）。
- 保存后均有 success toast 并刷新列表；重复点击有 `saving`/`:loading`/后端 409 防抖。
- 密钥回显防误清（编辑取单条明文回填，失败保留 `***`，后端把 `***` 还原为旧值）。
- 切换驱动无残留（编辑时驱动 `:disabled`，新建时 `onDriverChange` 重建 config）。
- 搜索页与文件页**视图偏好本就同步**（共用 `app.viewMode` + `localStorage nl_view`），刻意设计。
- 前后端直连扩展名表一致；详情卡与播放页共用同一次 `/video/info` 探测（无重复 ffprobe）。
- 触屏下 VideoCard 隐形播放图标已 `display:none`，不拦点击。
- 「查看更多」（历史 50 条）与「查看全部」（完整列表）是刻意区分的两种语义。
- 前端无 `console.log`/`debugger`/`alert`/TODO/占位文本/死链/英文标签；空状态齐全；品牌统一 WebVid。

---

# 环境要点（新对话续接必读）

- 登录路由是 **`/api/auth/login`**（不是 `/api/login`）；搜索是 **`/api/fs/search`**（不是 `/api/search`）。
- e2e 账号 `admin` / `admin123`。若被 e2e 改掉：`./webvid.exe reset-password admin123`。
- **`player-check.mjs` 默认连 5299**，跑本机实例要 `NL_BASE=http://localhost:5243 node player-check.mjs`。
- `files/文档/说明.md` 是 `e2e-check` 必需样本，缺失会在「md 预览抽屉」步超时。**本次已补建**。
- 用户本机 5243 实例是**开发测试用的，可以随便起停**（用户 07-25 明确说过）。
- 改前端后**必须 `go build` 重嵌**（`go:embed`）；构建**勿接管道**（会吞 vite 失败）。
- 服务端日志里 `[thumb] 远端视频抽帧失败 … context canceled` 是良性噪音（ffmpeg 频繁开关连接）。
- 浏览器控制台 `ERR_ABORTED` 多为离页良性中断；上一个脚本残留会污染下一个，**重跑即干净**。

# 文案口径（用户强要求）

对标 **Apple 软件更新说明**：简洁干练、克制有质感。
- 报错：说清「什么错 + 一句怎么办」，绝不甩英文/jargon/Go 类型名/`pkg:` 前缀。
- 更新日志：短句列点（`•`）、陈述式。**禁用** `①②③`、`——`铺陈、emoji、感叹号刷屏、
  「强大/无缝/轻松/贴心/助力」等营销词。
- 产品标准：学 Apple 对细节吹毛求疵——反人类逻辑、跨功能不一致、卡顿、多余一步、
  无反馈、防呆缺失都要挑出来修。
