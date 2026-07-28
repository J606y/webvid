# 播放拉流性能整改（2026-07-29）

## 起因

用户反馈：高码率视频在 WebVid 里播放持续缓冲，同一文件在 Google Drive 网页端播放正常。
补充观察是最关键的线索——**"拉流非常不积极，卡很久后台都没有网络活动"**。

## 链路澄清（先纠正一个普遍误解）

Google Drive 的直链（`files/{id}?alt=media`）必须带 `Authorization: Bearer`，302 出去会丢掉
授权头导致客户端 401，因此 `handler_raw.go` 对它一律强制代理中转——**与挂载的「代理模式」
开关无关，关掉也照样中转**。实际链路是：

```
浏览器 → /api/raw → stream.Serve（并发分块）→ googleapis.com/drive/v3/files/{id}?alt=media
```

配置里的「代理模式」说明已补上这一点，免得后人照着开关去排查。

## 诊断（全部有实测支撑，非推断）

**1. 并发是假的（主因）。** `MultiReader` 用裸 `http.DefaultClient`。实测 `www.googleapis.com`
经 ALPN 协商 h2，`http.DefaultClient` 拿到 `Proto=HTTP/2.0`——Go 的 h2 Transport 把发往同一
host 的并发请求全部复用到**一条** TCP 连接，4 个 worker 共享一个拥塞窗口。多线程换不来一个
字节的额外带宽，反而把一条连接切成 4 段轮流启停。浏览器直连云盘是一条持续满速的长连接，
所以它不卡。

**2. 卡住时真的零流量。** 分块客户端实测 `ResponseHeaderTimeout=0s`，唯一兜底是
`chunkTimeout = 2 分钟`。上游一挂起，worker 干等最多两分钟，窗口占死、读端阻塞。
（`ServeSingle` 一直有 30s 响应头超时，偏偏并发这条路上漏了。）

**3. 首字节要等一整块。** `doRange` 用 `io.ReadFull` 收满 4MB 才交给读端，起播与每次拖动的
首字节延迟 = 下满一整块的时间。

**4. 几乎没有预读。** 窗口写死等于线程数，最多 4×4MB=16MB 在途，且第 N+4 块必须等浏览器
消费完第 N 块才能开工。

**5. 连接从不复用（诊断日志上线后才发现的）。** 只读满 Content-Length 就 `Close()`，
net/http 的收尾是异步的，赶不上下一块的请求 —— 实测 12 块 12 条新连接、零复用。
跨国链路上每块白白多一次 TCP+TLS 往返。这一条是写完诊断、看第一份日志时冒出来的，
原计划里没有。

## 改动

`internal/stream/accel.go`（核心）
- `newChunkTransport()`：`ForceAttemptHTTP2=false` + `TLSNextProto` 非 nil 空表（两者缺一
  不可）拒绝 h2，每个 worker 独占一条 TCP 连接；`MaxIdleConnsPerHost=32`（Go 默认只有 2）；
  `ResponseHeaderTimeout=15s`。
- `stallReader`：响应体连续 20s 无字节到达即取消本次请求，落进既有的换链/退避重试路径。
  `chunkTimeout` 随之从 2 分钟降到 60s。
- 渐进分块：块大小 512K→1M→2M…翻倍到 `ChunkBytes`。`ChunkBytes ≤ 512KB` 时自动退化为
  均匀分块（小分块配置行为不变）。
- `readBody` 读满后清尾到 EOF（上限 32KB），连接才能回到空闲池被复用。
- `Opts{Threads, ChunkBytes, ReadaheadBytes, Label}` 取代散装参数；
  窗口 = `max(ReadaheadBytes/ChunkBytes, Threads)`。

`internal/stream/debug.go`（新）
- `NL_STREAM_DEBUG=1` 时逐块输出首字节延迟/耗时/速率/连接是否复用，关流给汇总。
  关闭时零开销。**第 5 条根因就是它发现的。**

`internal/stream/serve.go`
- `singleClient`（ffmpeg 读源）同样禁 h2；`servePassthrough` 改用它，一并获得响应头超时。

配置贯通
- `driver/registry.go` 新增 `readahead_mb`（默认 32，后台存储表单自动出现，无需改前端）；
  补正 `proxy` 与 `threads`/`chunk_mb` 的说明文案。
- `fs.AccelOpts` 增 `ReadaheadBytes`，钳制 [4,512] MB；新增 `AccelOpts.Stream(label)` 适配。
  取值范围只在这里定——`stream` 是纯机制，不掺"多大算合理"的策略。

## 为什么服务器→浏览器那一段不能也切块多线程

用户问过。浏览器的 `<video>` 对一个文件只发一个 Range 请求，HTTP 语义上一个请求就是一条
有序字节流，服务器无法把它拆到多条连接上发。要并行只能放弃原生播放、改成前端 JS 并发抓块
喂 MediaSource——那会连带丢掉 iOS 的系统画中画与 AirPlay（它们绑在原生播放链上），
代价远大于收益。并发只发生在服务器↔云盘这一段。

## 测试

`internal/stream/accel_test.go` 新增：
- `TestChunkTransportRejectsHTTP2`——对着**真开了 h2** 的 httptest 服务器验协议是 1.1，
  并带对照组先确认服务端确实提供 h2，否则断言是空的。这条是整个加速的命门。
- `TestConnectionsAreReused`——新建连接数不超过线程数。已验证：去掉清尾即失败（12 条新连接）。
- `TestChunkScheduleTiles`——5×8×4 组参数下断言分块首尾相接、不重叠、不越界、恰好铺满。
  算错就是静默的数据损坏，比任何性能问题都严重。
- `TestStallWatchdog`、`TestReadaheadWidensWindow`、`TestRampShrinksFirstChunk`、
  `TestRampDegradesToUniform`、`TestOptsNormalize` / `TestOptsWindow`、
  `TestDebugTrace`（诊断默认关闭，没用例等于从没跑过）。

`go build ./... && go vet ./... && go test ./...` 全绿。本次无前端改动，dist 无需重嵌。

## 待办：真机实测（尚未做）

形式验证只能证明代码按设计工作，证明不了用户的片子不卡了。上线后需要：

1. `NL_STREAM_DEBUG=1` 起服务，播那部高码率 mp4，收集：平均速率、首字节延迟、
   `新建/复用` 比例。
2. 判定标准：连接复用数远大于新建数；并发块的速率之和明显高于单块速率（证明拥塞窗口
   已独立）；整片播放无缓冲。
3. 拖动到片中若干位置，确认首字节延迟已降到渐进首块（512KB）的量级。
4. 若速率之和上去了、播放仍卡，那瓶颈在 VPS→用户 这一段，与本次改动无关，另议。

## 暂不做，先观察

Google Drive 路径缓存 TTL 只有 2 分钟（`googledrive.go`），超时后每次 `/api/raw` 都要把父目录
整个重列一遍才拿得到直链，大目录下是起播前的一段纯等待。等诊断日志给出链接获取耗时的实测
数据再定——直接改 TTL 是拿外部改动的可见性换速度，没有数据不做这个交易。
