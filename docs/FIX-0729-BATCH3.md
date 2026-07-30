# 第 3 批 · Google Drive 播放中断 —— 完成记录（2026-07-29）

对应 `docs/FIX-PLAN-0729.md` 的「第 3 批」。根因 3.1 ~ 3.5 全部落地，未越界改其它批次。

**代码已改完并自测通过，尚未提交**（原因见文末「提交为什么停住」）。

---

## 改了什么

因为几个批次在并行改同一批文件，下面按符号而不是行号定位——行号每分钟都在动。

### 3.1 · 403 被当成「链子过期」而不是「限流」

`internal/stream/accel.go`

- 新增 `escalate(d disposition, relinked bool)`：**换过一次链、同一段仍被拒，就不再当链过期**，
  改按限流退避。403 对 OneDrive 是链失效，对 Google Drive 是限流的主力返回码
  （rateLimitExceeded / userRateLimitExceeded），单看状态码分不出来；而换一条全新的链再被
  同样拒绝，本身就是判据。真过期的链换一次就活了，走不到这里。
- `disposition` 常量重排，**零值改为 `dispHard`**。原先零值是 `dispRelink`，`doRange` 改成返回
  处置枚举后，网络层错误（连响应都没拿到）会被零值误判成"该换链了"。
- `fetchChunk`：接住 `escalate` 的结果；限流分支不消耗尝试次数（原有契约不变）。
- `throttleWait(h, n)` 取代 `retryAfter(h)`：上游给 `Retry-After` 就听它的，**没给则 2s→4s→8s…
  递增**（钳到 `throttleMax`）。Google 的 403 限流不带这个头，固定 2s 死磕只会把限流窗口一直续上。
- `doRange` 签名 `(buf, disposition, retryAfterHdr, error)`，取代原来的 `(buf, bool, wait, error)`。

`internal/stream/serve.go`

- `openUpstream`（ffmpeg / HLS 读源那条路）套用同一套 `escalate` + `throttleWait`。
  计划里只点了 `fetchChunk`，但 `classifyErrStatus` 上方的注释明确要求两条路径判定一致，
  只改一半就是留一处漂移。

`internal/driver/googledrive/client.go`

- `forceRefresh` 加最小刷新间隔 `minRefreshInterval = 5s`，新增字段 `client.refreshedAt`。
  4 个 worker 同时撞限流会各喊一次换链，而 `token()` 是**持着锁做网络 I/O** 的
  （`refreshLocked` 里 15 秒超时的 HTTP 请求），无脑丢弃令牌等于把一次 OAuth 交换变成四次串行，
  期间该存储上的一切操作全堵在这把锁后面。

### 3.2 · 中转层在拿到第一个字节之前就把状态码提交了

`internal/stream/serve.go` · `Serve`

- `WriteHeader` 挪到**第 0 块到手之后**：先 `io.ReadFull` 取 1 字节（渐进分块已把首块压到 512KB），
  失败则删掉 `Content-Length` / `Content-Range` 并返回 502。客户端在传输途中断开不算故障，直接返回。
- 新增 `openFailMessage(err)`：限流 / 拒绝 / 不支持 Range / 超时各一句人话，其余回落到通用句。
  `ServeSingle` 的 502 也换用它（原先固定一句"拉取源失败"）。
- 配套：`errRelink` / `errThrottled` 由字符串改为哨兵错误并 `%w` 包装，**日志文案逐字未变**，
  `openFailMessage` 靠 `errors.Is` 穿过 `fetchChunk` 的外层包装做判定，不做字符串匹配。

### 3.3 · Google 侧的配额语义没接上

- `client.go` · `mapDriveError` 补 `downloadQuotaExceeded`（"下载配额已用尽，通常 24 小时后
  自动恢复"）与 `cannotDownloadAbusiveFile`（"Google 将该文件标记为可疑内容，拒绝下载"）。
- `googledrive.go` · `Link` 的下载 URL 加 `acknowledgeAbuse=true`。挂的是用户自己的云盘，
  Google 对体积大、传播广的文件常打可疑标记，不带这个参数直接 403。
- **补一刀（计划外）**：`internal/stream/accel.go` 新增 `upstreamNote`，非 2xx 时读 ≤4KB 响应体
  压平成一行附进错误。`stream` 是纯机制包不解析响应体，Google 的 `reason` 原先从头到尾没人读过——
  线上只剩一个光秃秃的 403，限流和没权限长得一模一样。现在服务端日志直接写着原因，
  「需要在服务器上确认的一件事」不用再开调试模式。

### 3.4 · ffmpeg 的续传不认 403

`internal/media/probe.go` · `httpInputArgs`：`-reconnect_on_http_error` 由 `429,5xx` 改为
`403,429,5xx`。已用本机 ffprobe 实测该取值可被接受（报错停在 TCP 连接失败，不是选项解析）。

### 3.5 · 前端把 error 对象丢了

`frontend/src/utils/playerHost.js`

- 新增模块状态 `failAt` 与 `markFail()`：**断点在第一次报错时就记下**。ArtPlayer 落到兜底面板
  之前已经重设 url 重载过两轮（每轮间隔 1 秒），hls.js 的 `startLoad` / `recoverMediaError` 同理，
  那时 `currentTime` 早已归零，「从中断处重试」实际是从头开始。`video:playing` 一触发即作废该断点。
- 新增 `mediaErrMessage()`：按 `art.video.error.code` 分流（3=解码失败、4=源不可用、其余=断流）。
- 新增 `hlsErrMessage(data, isMedia)`：按 `data.response.code` 分流
  （404/410=转码会话已回收、5xx=服务端从存储取不到源）。

  **实测结论**：hls.js（1.6.16）的 xhr-loader 只把 `xhr.statusText` 放进 `response.text`，
  **响应体拿不到**。所以 3.2 那句服务端写好的 502 原因传不到前端，前端只能凭状态码说话。
  查证位置：`node_modules/hls.js/dist/hls.mjs:31177`、`hls.d.ts` 的 `LoaderResponse`。

---

## 与计划的三处偏差

1. **3.2 没有用 `util.Humanize`**。`internal/stream` 的包注释写明「纯标准库实现，不依赖项目内
   其它包」，引它进来就破了这个契约；且 `Humanize` 按关键词匹配，会把限流的 403 一律说成
   「没有权限执行此操作」——那比不说更误导。改为在包内做一份基于哨兵错误的映射。
2. **3.1 选了计划里的「更稳」方案**（换链一次后降级为限流），没动 `driver.Link` 的契约，
   也没动 `LinkProvider` 签名。附带把限流等待改成递增、把处置同步到单流路径。
3. **多了 `upstreamNote`**（见 3.3）。计划没写，但它把「待实机确认清单」的第 1 条直接变成
   看一眼日志就有答案，代价是十行。

---

## 新增测试

| 用例 | 文件 | 盯住什么 |
|---|---|---|
| `TestForbiddenAfterRelinkBecomesThrottle` | `internal/stream/accel_test.go` | 持续 403 下靠限流预算读完；且 provider 只被重调 2 次（换链只该发生一次） |
| `TestServeFirstChunkFailureIs502` | `internal/stream/serve_test.go` | 首块失败落成 502，且不再带着原来的长度/区间承诺 |
| `TestOpenFailMessage` | `internal/stream/serve_test.go` | 分类能穿过 `fetchChunk` 的外层包装 |

`TestForbiddenAfterRelinkBecomesThrottle` 已验证是**真回归测试**：把 `escalate` 改成恒等函数，
它会失败（4 次尝试 1.2 秒烧完，整块判死）。

---

## 验证记录

- `go test -count=1 ./internal/stream/` ok
- `go test -count=1 ./internal/driver/... ./internal/media/ ./internal/util/` ok
- `npm run build`（前端）✓ built in 4.33s。`npx vite build` 第一次 segfault，第二次过——
  就是 `docs/POLISH-PROGRESS.md` 记过的那台机器的老毛病，与本批无关。
- `go build ./...` ok；`go build -o zz_batch3.exe .` ok（**`webvid.exe` 被另一进程占用，
  没能覆盖它**——前端已重新嵌入到临时产物验证过，正式那份要等文件释放后再 `go build` 一次）。

### 全量测试里有失败，但没有一条是本批的

已逐条查明，全部来自其它批次的在途改动：

| 失败 | 归属 | 依据 |
|---|---|---|
| `TestDecide/h265_全转码` | 第 5 批 5.7 | `probe.go` 已加 `Decision.VideoHEVC`，`probe_test.go` 未同步（`git diff` 显示测试文件未改） |
| `TestRawProxyFullChain`（200 但 0 字节）<br>`TestRawProxyInternalSingleStream`（416）<br>`TestRawDirectLinkInternalProxied`（416） | 第 5 批 5.5 | `fs.LinkEx` 已去掉 `Stat` 改用 `lk.Size`，而 `raw_proxy_test.go` 的 `rangeTestDriver.Link` 返回 `&driver.Link{URL: d.url}` **不填 Size** → `Info.Size=0` → 416 / 空 200 |
| `TestProxyLoopbackTranscodeE2E` | 同上 | ffprobe 经 `/api/raw` 读源，撞的是同一个 Size=0 |
| `TestStorageCreateScansOnlyNewMount` | 第 4 批 4.2 | `internal/index/index.go` 在途 |

---

## 提交为什么停住

本批的改动文件里有四个正被其它会话同时改：

| 文件 | 本批 | 同时在改 |
|---|---|---|
| `internal/stream/accel.go` | 3.1 / 3.3 | 第 6 批 6.7（`window()` 去掉线程数下限） |
| `internal/stream/serve.go` | 3.1 / 3.2 | 第 5 批 5.1（`ServeSingle` 前插预读缓冲） |
| `internal/media/probe.go` | 3.4 | 第 5 批 5.7（HEVC 直出） |
| `internal/driver/googledrive/googledrive.go` | 3.3 | 第 5 批 5.5（`lookup` 单飞） |
| `internal/driver/googledrive/client.go` | 3.1 | —— |
| `frontend/src/utils/playerHost.js` | 3.5 | —— |

现在提交这几个文件，会把别人**没做完**的活一并带走（第 5 批那两处正卡在测试红灯上）。
按仓库约定「提交一律显式列文件、且不卷走别人的活」，这里停下等指令。

两条路，任选：

- **等**。第 5 / 6 批收尾后，由收尾的那一方统一提交，本批改动随之带入。
- **现在提**。只提 `client.go` 与 `playerHost.js` 这两个独占文件（其余四个文件的本批改动留在
  工作区），但这样第 3 批在提交历史里是断的，不推荐。

## 待办

1. **实机确认**（计划里那条）：复现后抓服务端日志里 `[stream] 分块 N [...] 下载失败（已重试）` 那行。
   现在这行**尾部会带上 Google 的原话**，`rateLimitExceeded` 还是 `downloadQuotaExceeded` 一眼可辨。
2. `webvid.exe` 释放后补跑一次 `go build -o webvid.exe .`，把前端重新嵌进正式产物。
3. 发版更新日志（文风见计划末尾一节）。本批可写的用户可见变化：
   - Google Drive 视频遇到访问频率限制时会等待重试，不再直接中断
   - 播放失败时给出具体原因（限流 / 需重新授权 / 存储不可用），不再一律说"网络不稳"
   - 从中断处重试真的从中断处开始
