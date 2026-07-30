# WebVid 故障排查与修复计划（2026-07-29）

本文档是一次完整排查的产出。所有结论都有 file:line 证据，且已逐条复核过源码。
排查过程未修改任何代码。

## 怎么用这份文档

按「批次」拆开，每批自成上下文。开新会话时说清做第几批，先读本文档开头的
「每个会话都必须先读」，再读对应批次。

批次之间有依赖关系的地方已经标注。没标注的可以并行开会话。

**每个会话只做自己那一批。** 看到别批的问题记下来，不要顺手改——并行会话同时改
同一个文件会互相覆盖。

---

## 已完成的事（不要重做）

- **v2.3.0 已撤版**。release 和 tag 都已从远程删除，`releases/latest` 回落到 v2.2.0，
  `install.sh` 的安装与 `update` 恢复正常。tag 指向的提交 `0803331` 就是 main 的 HEAD，
  代码没丢。修完第 1、2 批后重发 v2.3.0。
- 排查本身已完成，不需要再查一遍。直接按批次动手。

---

## 每个会话都必须先读：项目约定与踩坑

1. **改了前端必须 `go build` 重新嵌入**。前端是编译进二进制的，只跑 `vite build`
   不重新 `go build`，运行时看到的还是旧页面。
2. **构建命令不要接管道**。接管道会导致 segfault，这是本项目踩过的坑。
3. **提交禁止 `git add -A`**。仓库常有并行会话同时改，一律显式列出本次改动的文件，
   并且 `git add` 之前先清暂存区（别人 `git add` 过的东西会被你的 commit 一并带走）。
4. **本机跑不了 `-race`**（Windows + cgo 环境限制）。并发相关的改动靠代码审查和
   e2e 验证。
5. e2e 相关：登录路由是 `/api/auth/login`；`player-check` 需要 `NL_BASE` 环境变量；
   admin 密码被 e2e 改掉后用 `./webvid.exe reset-password admin123` 恢复。
6. **发版必须写更新日志**，`gh release edit --notes-file` 覆盖流水线自动生成的 notes。
   文风要求见本文档末尾「更新日志文风」一节。

---

## 第 1 批 · 封面与源信息预载（功能等于没有）

> **已全部做完（2026-07-30，未提交）**。三处与本计划不同的判断、上线要知道的副作用，
> 见本节末尾「已落地」；提交处境见文末。

**优先级最高。** 这一批修完，「一开始索引 TCP 就打到 2000」和「预载完成了还是没封面」
两个问题同时解决。

### 现象

- 后台显示预载完成，回到主页还是没有封面。
- 索引期间服务器 TCP 连接数冲到 2000 左右，服务崩掉。
- 预载进度条不动。

### 根因 1.1 · 预载用了一个数据库里不存在的管理员，云盘回环必 401

`internal/preload/preload.go:55`

```go
var admin = &user.User{Role: "admin", BasePath: "/"}
```

`ID` 是零值。这个假身份传给 `thumbs.Get`（`preload.go:545`）和 `media.Decide`
（`preload.go:558`）。云盘上抽封面和探测源信息都要 ffmpeg 走回环 `/api/raw`，
token 由 `internal/media/hls.go:107` 的 `auth.SignToken(u.ID, ...)` 签出，`sub` 是 `"0"`。

而 `/api/raw` 挂在鉴权组下（`internal/server/router.go:45`），中间件
`internal/server/middleware.go:41-44` 拿 id=0 去 `GetByID`，`users` 表是
`AUTOINCREMENT` 从 1 起（`internal/db/db.go:38-39`），**永远查不到，一律 401**。

`X-Internal-Auth` 只被限速器和「选 ServeSingle 还是 Serve」用到
（`internal/server/limit.go:23-27`），**不参与鉴权**。

受影响面：
- Google Drive / Telegram 的**所有**视频封面（这两个驱动没实现 `driver.Thumber`）
- OneDrive / PikPak 上云盘给不出自带缩略图的 mkv / ts / flv
- **所有非 direct 扩展名的视频源信息探测**（`internal/media/info.go:13-15` 的
  `directPlayExts` 只有 mp4/m4v/mov/webm，其余都要 ffprobe，云盘上全部 401）

本地盘不受影响——`hls.go:100-106` 对 `LocalPather` 走 `AbsPath`，不发 HTTP。

**改法**

`preload.New()` 里查一次真实管理员并存起来：

```sql
SELECT id, username, role, base_path FROM users WHERE role='admin' AND enabled=1 ORDER BY id LIMIT 1
```

把 `preload.go:545` 和 `preload.go:558` 的 `admin` 换成它。查不到就整轮跳过，
并在预载卡片的错误信息里写清「没有可用的管理员账号」。

配套防呆：`internal/media/hls.go:107` 在 `u.ID <= 0` 时直接返回错误，别再发一个
注定 401 的请求——这类必错请求不该被伪装成网络故障。

**不要给 `X-Internal-Auth` 开鉴权旁路。** 反代之后外部同样能带这个头，等于开后门。

### 根因 1.2 · 本地盘封面写 400、读 480/1200/320，缓存键逐字节不同

封面缓存键是 `sha1(路径|mtime|大小|宽度)`，**宽度进哈希**（`internal/thumb/thumb.go:436`），
本地盘走这个键（`thumb.go:165`）。

预载固定用 `coverWidth = 400`（`internal/preload/preload.go:39`）。前端实际请求的宽度：

| 调用点 | 宽度 |
|---|---|
| `frontend/src/components/VideoCard.vue:6` | 480 |
| `frontend/src/components/FeaturedCarousel.vue:10` | 1200 |
| `frontend/src/components/MediaGridCard.vue:8` | 320 |
| `frontend/src/components/VideoDetailCard.vue:11` | 480 |
| `frontend/src/components/VideoDetailCard.vue:13` | 1200 |
| `frontend/src/pages/LibraryPhotos.vue:65,89` | 480 |
| `frontend/src/pages/LibraryPhotos.vue:111` | 320 |
| `frontend/src/utils/mediaSession.js:26` | 480 |

**一个都不是 400。** 预载生成了、落盘了、进度显示完成了，然后原样烂在
`data/thumbs` 里，任何前端请求都不会命中。

路径、大小写、分隔符、URL 编码全部一致，唯一不一致的字段就是宽度。
`preload.go:37-38` 的注释专门写了「判缓存与真下载必须用同一个宽度」——
只对齐了预载内部，没对齐前端。

远端两条分支的键不含宽度（`thumb.go:288` 的 `|remote`、`thumb.go:338` 的 `|vframe`，
mtime/size/width 全传零值），所以云盘不受此影响。

**改法**

在 `internal/thumb/thumb.go` 加一个宽度归档函数，`Get()`（`thumb.go:165`）与
`Cover()`（`thumb.go:260`）都用它，`preload.go:39` 用同样的档位：

```go
// normWidth 把请求宽度归到固定档位，避免每个调用方各写一个数字导致缓存互不命中
func normWidth(w int) int {
	if w <= 640 { return 640 }
	return 1280
}
```

改完 320/480 共用 640 那份，1200 落 1280 那份。

注意：改档位会让现有 `data/thumbs` 里的本地盘缓存全部失效（键变了）。这是一次性的，
可以接受；也可以在后台「删除封面缓存」里顺手清一次。

### 根因 1.3 · `Cover()` 与 `Get()` 分支不一致，预载永远不收敛

`internal/thumb/thumb.go:216-218` 的注释白纸黑字要求两者分支必须一致，实际不一致：

- `Get()`（`thumb.go:134-151`）：对有 `Thumber` 能力的驱动，先试 `remote()`，
  **取不到就继续往下走** `remoteVideoFrame()`，产物落在 `|vframe` 键。
- `Cover()`（`thumb.go:231-236`）：**只看 `|remote` 键，没有就无条件返回
  `CoverPending`，永远不看 `|vframe`。**

后果：OneDrive 上的 mkv 即使封面已经抽出来落盘，也永远被判成「还有活要干」。
每轮进待办、每轮重打一次 Graph 缩略图请求才发现没有、每轮算失败、失败又触发
退避重试（`preload.go:616-617`，2 分钟起翻倍到 6 小时）。

这就是「后台一直在忙、进度条总在同一个位置」的成因，也是「凭空增加流量」的实锤。

**改法**：`thumb.go:231-236` 的分支补 `|vframe` 判定，与 `Get()` 对齐——
`|remote` 新鲜则 Ready；否则若是云盘视频且 `|vframe` 新鲜也是 Ready；再否则按现有
逻辑 Pending / None。

### 根因 1.4 · 失败也算完成

`internal/preload/preload.go:424` 的 `s.done.Add(1)` 只要 `process()` 返回就加，
不区分成败；`preload.go:611` 无条件写 `finishedAt`。**每一项都失败也会显示 100% 完成。**

**改法**：只在本项全部成功时累加，或者 `finish()` 里 `failed > 0` 时记为「部分完成」
而不是「完成」。

### 根因 1.5 · 封面接口把服务器连接钉死 5 分钟

`internal/server/handler_video.go:126`

```go
ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Minute)
```

摘掉浏览器取消信号的动机是对的（不摘的话滚动一取消，正在抽的帧当场被杀、半成品丢弃）。
但代价是：**浏览器早就取消的封面请求，服务端仍占着一条 TCP 最长 5 分钟**，
而抽帧闸只有 2 个名额（`thumb.go:75`），其余全在排队等。

主页一次滚动几百张卡片，连接就是这么攒起来的。用户说的「一开始索引就打到 2000」，
实际触发点是索引期间打开主页。

**改法**：封面未命中缓存时**立刻返回 404**（前端本来就有 `@error="hideImg"` 兜底，
见 `frontend/src/utils/file.js:55`），同时把该路径推进预载待办去后台做——
新增一个 `preload.Nudge(path)`，内部去重并复用现有的 sem / gate。
浏览器毫秒级拿到结果，连接不再被钉住，封面照样在后台做完落盘。

过渡方案（改动更小）：把 5 分钟压到 10 秒，并在进闸前加一道
「拿不到名额就 404」的 `TryAcquire`（需要给 `internal/util/gate.go` 补这个方法）。

### 根因 1.6 · 云盘自带缩略图那条路一点闸都不过

`internal/thumb/thumb.go:287-315` 的 `remote()` 里，`t.Thumb(ctx, rel)`（`thumb.go:307`）
前面**没有** `s.jobs.Acquire`，只有一个按 key 去重的 singleflight 和硬编码的
`dlSem = 6`（`thumb.go:76`）。并发数 = 同时到达的 HTTP 请求数，**没有上限**。

**改法**：`t.Thumb()` 之前补 `s.jobs.Acquire`；`dlSem` 的 6 提成配置项。

### 根因 1.7 · 三个云盘驱动的 HTTP 客户端是裸的，没有 Timeout 也没有连接池

```go
// internal/driver/onedrive/graph.go:48
// internal/driver/pikpak/client.go:20
// internal/driver/googledrive/client.go:37
var httpClient = &http.Client{}
```

既没设 `Transport`，也没设 `Timeout`。用的是 `http.DefaultTransport` 的默认值：
每 host 只留 2 条空闲连接、`MaxConnsPerHost` 为 0（不限）。

一次 200 张封面的并发调用会开 200 条新 TCP+TLS，完事只留 2 条，其余 198 条进
TIME_WAIT。连接曲线正是「瞬间冲高、迟迟下不来」的形状。

**全仓只有 `internal/stream/accel.go:78-80` 一处调过连接池**
（`MaxIdleConnsPerHost=32` / `MaxIdleConns=128` / `IdleConnTimeout=90s`）。
`MaxConnsPerHost` 全仓一处都没设。

另外 `internal/server/handler_offline.go:136` 的 Transport 只设了
`Proxy` / `DialContext` / `ResponseHeaderTimeout`，**`IdleConnTimeout` 未设等于 0，
空闲连接永不回收**。

**没有 Timeout 是更狠的一刀**：加上预载用的是无 deadline 的
`context.Background()`（`preload.go:353`），云盘一挂 `t.Thumb()` 就永久卡住。
worker 只有 2 条，卡死 2 条 = **进度永久停住而界面仍显示「运行中」**。
`thumb.go:99-119` 的 singleflight 还会把同 key 的后来者一起挂住。

**改法**
- 三个驱动客户端补 `Transport`（`MaxConnsPerHost: 16`、`MaxIdleConnsPerHost: 16`、
  `IdleConnTimeout: 90s`）和 `Timeout`（或给每个请求挂 ctx deadline）。
- `handler_offline.go:136` 补 `IdleConnTimeout: 90s`。
- `preload.go:541` 的 `process()` 给封面和探测各套一个 `context.WithTimeout`
  （封面 3 分钟，覆盖 `hls.go:151` 的 60s×2 抽帧；探测 2 分钟，覆盖
  `probe.go:87` 的 45s），杜绝 worker 被永久钉死。

### 根因 1.8 · `media_jobs` 名不副实，实际是 2N

`internal/thumb/thumb.go:75` 和 `internal/media/hls.go:78` 各建了一把
`util.NewGate(2)`，`main.go:128-129` 把同一个 `media_jobs` 分别设给这两把闸。
用户以为设了 N，实际能同时跑的 ffmpeg / ffprobe 是 **2N**。
`conf.go:108-110` 与 `hls.go:59-61` 的注释都写「总闸」。

而且云盘视频封面会同时持两把闸（`thumb.go:358` → `hls.go:134`），挤掉 ffprobe 的名额。

**改法**：`main.go` 里建**一把** `util.Gate` 同时传给 `thumb` 和 `media`
（要改 `thumb.New` / `media.New` 的签名），删掉各自的 `NewGate(2)`。

### 验收

- 后台预载卡片上的失败数归零（或只剩真正取不到封面的项）。
- 主页刷新后能看到封面，且 `data/thumbs` 里的文件数与卡片上的数字对得上。
- 索引期间在服务器上 `ss -s` 看连接数，不再冲高。
- 预载进度条会动，重启后不再从 0 开始（配合下面这条）。

### 附带可做（同批，改动很小）

- `preload.go:519` 的 `countCached()` 目前只在推迟态下跑（`preload.go:159`），
  正常启动不跑，所以重启后卡片会长时间显示 0。改成正常启动也跑一次。
- `Progress` 结构加一个阶段字段（`counting` / `running`）。collect 阶段
  （`preload.go:381-390`）要对全库每个媒体做一次磁盘 stat 加一次 SQL，跑完之前
  `total` 一直是 0，前端百分比恒为 0。这个阶段前端应该用不确定态进度条。
- `preload.go:493` 每项一次 `os.Stat`、`preload.go:501` 每项一次 `media_info` 查询，
  是 N+1。改成一条 `SELECT path FROM media_info` 拉进 set 内存比对。
  代码自己在 `preload.go:380` 承认「几万条要几秒」。

### 已落地（07-30）

八条根因全部按计划修完。落点：`preload.go` 的 `adminUser()` 与身份传递、`hls.go:input`
的 `u.ID <= 0` 防呆、`thumb.go` 的 `normWidth`（`widthCard=640` / `widthHero=1280`）与
`Cover` 补 `|vframe`、`finish` 有失败就不写 `finishedAt`、`thumbHandler` 的
`coverWait`/`coverBuild`、`remote()` 把网络闸提到 `t.Thumb` 之前、新文件
`internal/util/httpclient.go`、`main.go` 建一把闸经 `SetGate` 交给 thumb 与 media。

**三处与上文不同的判断，别再按原计划改回去：**

1. **1.6 挂的是新建的一把网络闸 `dlLimit=6`，不是 ffmpeg 总闸。** 取云盘缩略图全程是
   网络等待、不吃 CPU，挂上总闸会让一屏封面排在抽帧和探测后面、首屏干等，1.8 合并成
   单闸之后两边还互相饿死。`6` 也**没有**提成配置项——缺陷是「一点闸不过」，不是
   「不可调」，加个用户看不懂的旋钮不解决问题。
2. **1.7 三个驱动只补 Transport，绝不能加 `http.Client.Timeout`。** 那个 Timeout 覆盖
   body 读取，加上去等于把大文件上传和视频拉流一并砍断（三个驱动的注释本来就写明了
   这点）。改为连接池 + 握手/响应头时限，body 不设限；worker 被永久钉死改由
   `preload.process` 给每件活挂 deadline 解决（封面 3 分钟、探测 2 分钟）。
3. **1.5 用「handler 只当场等 8 秒、超时 404、生成在后台跑完落盘」，没做 `preload.Nudge`。**
   效果一样，但不必新增跨包 API，也不用另做一套去重——thumb 本来就有 singleflight。

**附带三条**：`Progress` 加了 `phase`（`counting` / `running`），**第 2 批要消费它**：
清点阶段进度条该用不确定态，别再显示 0%。`media.Probed()` 一次拉出探测缓存消掉 N+1，
`ProbeStatus` 因此多一个 `cached ProbedSet` 参数。**「正常启动也跑一次 `countCached`」
没做**——前提不成立：正常启动走的是 `start → run → collect`，collect 本身就会算出并
写入计数，再跑一遍只是把同一件事做两遍；「重启后长时间显示 0」的真因是清点那几秒，
由 `phase` 解决。

**上线要知道的两件事：**

- **`data/thumbs` 里本地盘的旧封面缓存会全部失效**（宽度进哈希，档位变了键就变了）。
  一次性代价，云盘那两条键（`|remote` / `|vframe`）不含宽度，不受影响。
- **`media_jobs` 从此名副其实**（此前实际是 2N），并发会减半。此前按体感调小过的话
  可以调回去。

首页 hero 大图（1200 → 1280 档）预载不覆盖，第一次打开时现场生成，之后落盘复用。

---

## 第 2 批 · 后台索引管理页面（前端）

**可与第 1 批并行**，改的文件不重叠（第 1 批不动 `AdminIndex.vue`）。

### 现象

点了开始索引后，进后台要等一会儿才看到进度条。

### 根因

后端已经是后台跑了（`internal/index/index.go:104` 和 `internal/preload/preload.go:369`
都是起 goroutine 立即返回），问题全在前端和一个接口：

1. `frontend/src/pages/admin/AdminIndex.vue:130` 用 `Promise.allSettled` 把索引和
   预载两个接口绑在一起等，**两个都回来才赋值**。预载接口本身是纯内存微秒级
   （`internal/server/handler_admin.go:131-137`），被索引接口拖住。
2. 索引的 `Progress()`（`internal/index/index.go:88-93`）第一件事就是在请求线程里跑
   `refreshCount()` → `SELECT COUNT(*) FROM files`，正好和预载 collect 阶段抢那
   4 条 SQLite 连接（`internal/db/db.go:24` `SetMaxOpenConns(4)`），最坏排队 5 秒
   （`db.go:18` busy_timeout=5000）。
3. `AdminIndex.vue:36` 的进度条挂在 `v-else-if="preState.loaded && preload.running"`，
   首次响应回来之前 **DOM 里根本没有进度条**，只有三个占位条。
4. `AdminIndex.vue:153-154` 的轮询定时器装在 `await` 之后，第一次响应回来前一次都不轮询。
5. `AdminIndex.vue:100` 的百分比用 `Math.round`。单件最坏 165 秒
   （抽帧 60s×2 + ffprobe 45s），并发 2，总数上千时百分比几十分钟才跳 1。

### 改法

- 拆掉 `allSettled` 耦合，两个请求各自 `.then` 各自赋值，谁先回来谁先渲染。
- 定时器挪到 `await` 之前，或者 `onMounted` 里直接 `setInterval`。
- `index.go:88` 的 `COUNT(*)` 改成异步刷新（起 goroutine，本次先返回上一次的数字），
  或者维护增量计数器。
- 百分比保留一位小数；进度条旁边已有的「已处理 N / M 项」为准，另加「本轮已跑 X 分钟」。

### 验收

进后台的瞬间就能看到进度条和数字，不再先闪一下占位。

---

## 第 3 批 · Google Drive 播放中断

**独立，可单独开会话。**

> **已完成（07-29），代码改完未提交，见 `docs/FIX-0729-BATCH3.md`。**
> 3.1~3.5 全部落地。注意：3.1 的改动同时落在 `accel.fetchChunk` 与 `serve.openUpstream`；
> `stream` 包的 403 判定语义变了（换链一次后仍被拒 → 按限流退避），改这两个文件的会话先读那份记录。

### 现象

只有 Google Drive 的视频报「播放已中断：视频流断开，可能是网络不稳或文件已不可访问」。

### 为什么只有它

`internal/driver/googledrive/googledrive.go:306` 的 `Link()` 会设
`Authorization: Bearer`（`alt=media` 端点必须带这个头）。

`internal/server/handler_raw.go:76` 的分流判据正是「直链带不带 Authorization」：

```go
if !res.Accel.Proxy && !s.isInternal(c) && lk.Header.Get("Authorization") == "" {
    c.Redirect(http.StatusFound, lk.URL)   // 302 直链
    return
}
s.rawProxy(c, res)                          // 服务器中转
```

带了头就不能 302（302 会把头丢掉，客户端必然 401），只能中转。

| 驱动 | Link 返回 | 走法 |
|---|---|---|
| Google Drive | `googledrive.go:306` 带 Bearer 头 | **恒中转** |
| OneDrive | `onedrive.go:239` / `:260` 无 Header | 302 直链 |
| PikPak | `pikpak.go:331` 只带 UA | 302 直链 |
| Telegram | `telegram.go:208` 返回 `Local:` 句柄 | `http.ServeContent`，不进中转 |

**这不是设计失误，是必然。** 后台的「代理模式」开关对 Google Drive 无效，
关掉照样中转，`docs/PLAYBACK-PERF.md:10-16` 已记过。
代价是 Google Drive 的流量全部过服务器，浏览器每开一个视频等于 4 条上游连接
（默认 `Threads=4`）。

**结论**：OneDrive / PikPak / Telegram 从来没跑过 `internal/stream` 这段代码，
所以这段代码里的缺陷只在 Google Drive 上显形。

### 根因 3.1 · 403 被判成「链子过期」而不是「限流」

`internal/stream/accel.go:163-172`

```go
func classifyErrStatus(code int) disposition {
	switch code {
	case 401, 403, 404, 410:
		return dispRelink       // 换链重试
	case 429, 503:
		return dispThrottle     // 走 2 分钟退避预算
	default:
		return dispHard
	}
}
```

403 对 OneDrive 确实是链失效，对 Google 却是限流的主力返回码。
项目自己的 `internal/driver/googledrive/client.go:209-213` 就写着
`403 + rateLimitExceeded / userRateLimitExceeded 是限流`，但这个函数只被
`req()`（`client.go:272`）、`doUpload()`（`client.go:628`）、`putChunk()`
（`client.go:741`）调用——**全是写路径，下载路径根本不经过它**。

于是每次 403 都去 `forceRefresh()`（`client.go:106`）强刷一个仍然有效的 token，
刷完照样 403。`chunkAttempts = 4`（`accel.go:25`）配 0/200/400/600ms 退避
（`accel.go:26`），**1.2 秒烧完全部重试**，整块判死。
`throttleBudget = 2 分钟`（`accel.go:38`）一秒都没用上。

雪上加霜：`client.go:73-75` 的 `token()` 是**持着锁做网络 I/O**
（`refreshLocked` 里 15s 超时的 HTTP 请求），刷新期间该存储的所有操作全部串行阻塞。
4 个 worker 同时撞 403 就是 4 次串行的 OAuth 交换。

**改法**（二选一）
- 轻改：`driver.Link` 增一个字段（如 `ThrottleStatus []int`），Google Drive 驱动填
  「403 归限流」，`stream` 从 `LinkProvider` 一并拿到。`stream` 仍是纯机制包，
  不认识 Google（`accel.go:2` 的注释要求它不依赖项目内其他包，这个改法不违反）。
- 更稳：403 保留「先换链一次」，若换链后同一块仍是 403，降级为 `dispThrottle` 走
  `throttleBudget`，不再消耗 `attempt`。改动点在 `fetchChunk`（`accel.go:349-396`）
  加一个「已换链过」标志。

配套：`client.go:106` 的 `forceRefresh` 加最小刷新间隔（如同一 token 5 秒内只刷一次）。

### 根因 3.2 · 中转层在拿到第一个字节之前就把状态码提交了

`internal/stream/serve.go:107-110`

```go
mr := NewMultiReader(req.Context(), provider, rg.start, rg.length, o) // 不阻塞，只起 worker
defer mr.Close()
w.WriteHeader(status)                                                  // 一个字节都没取到
io.Copy(w, mr)                                                         // 此处无法再改状态码
```

对照单流路径 `serve.go:149-158`：**先 `openUpstream` 成功了才 `WriteHeader`**，
失败能返回 502。

后果：中转模式下，上游任何失败（403 限流、配额耗尽、token 失效、超时）在客户端看来
都是同一件事——**一个承诺了 Content-Length 却提前断掉的 206**。浏览器只能报
`MEDIA_ERR_NETWORK`，前端 `frontend/src/utils/playerHost.js:307` 那句
「网络不稳或文件已不可访问」是它能说的全部，因为服务端确实没告诉它别的。

这也是这个故障难定位的原因。

**改法**：把 `WriteHeader` 挪到第 0 块拿到手之后。首块失败返回 502 加
`util.Humanize` 之后的人话原因。渐进分块已经把首块压到 512KB（`accel.go:49`），
这点延迟代价可以接受。

### 根因 3.3 · Google 侧的配额语义没接上

`internal/driver/googledrive/client.go:190-206` 的 `mapDriveError` 里，
`downloadQuotaExceeded` 和 `cannotDownloadAbusiveFile` 都**没有分支**，一律落
`ErrUpstream`。`googledrive.go:308` 的下载 URL 也没带 `acknowledgeAbuse=true`。

而 `internal/stream` 是纯机制包，不解析上游响应体，`resp.Body` 在 `accel.go:432`
直接 `Close()` 丢弃。Google 回的那串 `reason` 从头到尾没人读过。

**改法**：`mapDriveError` 补这两个 reason 并给出人话（如「该文件的下载配额已用尽，
通常 24 小时后自动恢复」）；URL 加 `acknowledgeAbuse=true`。

### 根因 3.4 · ffmpeg 的续传不认 403

`internal/media/probe.go:78`

```go
"-reconnect_on_http_error", "429,5xx",   // 不含 403
```

Google 限流会让 mkv / ts 的转码会话直接死。

**改法**：改成 `403,429,5xx`。副作用（真无权限时 ffmpeg 会重连到
`-reconnect_delay_max 30`）可以接受，因为 raw 层自己会先重试并快速失败。

### 根因 3.5 · 前端把 error 对象丢了

`frontend/src/utils/playerHost.js:303`

```js
art.on('error', (_, times) => {   // 第一个参数直接丢弃
```

`video.error.code` 从来没读过，`MEDIA_ERR_NETWORK(2)` / `MEDIA_ERR_DECODE(3)` /
`MEDIA_ERR_SRC_NOT_SUPPORTED(4)` 在当前实现里分不出来。

附带缺陷：`fail()` 在 `playerHost.js:150` 取 `art.currentTime` 当断点，
但 ArtPlayer 在报错前已经把 url 重设并重载过两次（`RECONNECT_TIME_MAX = 5`，
每次间隔 1 秒），此时 `currentTime` 已归零，「从中断处重试」实际是从头开始。

**改法**：`fail()` 带上 `art.video.error?.code`；断点位置改成在第一次 error 时就记下来。

### 需要在服务器上确认的一件事

Google 实际回的是 403 还是 429，代码里断不了。**不用开调试模式**——复现后抓服务端
日志里 `accel.go:393` 打的这行：

```
[stream] 分块 3 [3670016-7864319] 下载失败（已重试）: 直链疑似过期: HTTP 403
```

- `HTTP 403` → 根因 3.1 成立
- `HTTP 429` → 退避是生效的，另有隐情
- `context deadline exceeded` / `分块读取中断` → 是超时闸
  （`accel.go:33` 响应头 15s / `accel.go:53` 失速 20s / `accel.go:27` 单块 60s）

要更细的（每块首字节延迟、连接复用、状态码）用 `NL_STREAM_DEBUG=1` 起服务
（`internal/stream/debug.go:17`）。

**不改代码的当场验证**：把 Google Drive 挂载的加速线程数从默认 4 调到 1。
调到 1 就不断流，根因 3.1 即坐实。

### 顺带澄清（不用改）

**`optimizationguide-pa.googleapis.com` 与本项目无关。** 正因为是服务器中转，
浏览器从头到尾不和 Google 通信。这个域名是 Chrome 自身的 Optimization Guide 服务，
用来拉页面加载提示和端侧模型，由浏览器按自己的节奏发起。
前端代码里没有任何 Google 域名（`frontend/index.html` 干净，`frontend/src` 里只有
后台 OAuth 授权那几处，且只在点「授权 Google」时才开新标签）。

---

## 第 4 批 · 云盘路径缓存与数据库

**独立，可单独开会话。** 这一批是常驻内存和数据库压力的大头，改动都很局部。

### 根因 4.1 · 路径缓存的过期条目永远不删，负缓存没有上限

`internal/driver/googledrive/googledrive.go:229-235`

```go
func (d *GDrive) cacheGet(rel string) (cacheEntry, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.cache[rel]
	if !ok || d.now().Sub(e.at) > d.cacheTTL {
		return cacheEntry{}, false      // 判过期只返回 miss，不 delete
	}
	return e, true
}
```

条目原样留在 map 里。写入侧每列一次目录就把全部子项塞进去，而
`internal/index/index.go:324` 的全量索引会 BFS 整个挂载——**等于把云盘上每一条路径
都永久留在内存**，只有写操作触发 `cacheClear`（`googledrive.go:255`）才整表清空。

更麻烦的是 `cacheMissing`（`googledrive.go:243`）：每个查过的**不存在**的路径也写一条，
同样无限额、同样不删。

同型问题：`internal/driver/pikpak/pikpak.go:241-248`、
`internal/driver/onedrive/onedrive.go:78`（10 分钟 TTL，过期条目同样滞留）。

**改法**：miss 时顺手 `delete`；加条数上限走 LRU；负缓存单独限额。

### 根因 4.2 · `scanSubtree` 每行一个独立事务

`internal/index/index.go:530` 在 BFS 循环里逐条 `b.upsert(full, it)`，而 `upsert`
（`index.go:438`）是裸的 `b.db.Exec`，**没有 Begin / Commit**。
对照 `replaceAll`（`index.go:186-207`）是正确的单事务写法。

复制一个几千文件的目录就是几千次独立提交。这条改起来最简单、收益最直接。

**改法**：`scanSubtree` 里套事务批量提交（比如每 500 行 commit 一次）。

顺带：`index.go:504` 的 `scanSubtree` 每次调用无条件新起 goroutine，用的是
`context.Background()`，无并发限制、无去重、无取消。建议加一个包级信号量或复用
`startSubtree`（`index.go:369-398`）的 dedupe map。

### 根因 4.3 · 每个 HTTP 请求一次 users 查询

`internal/server/middleware.go:41` → `internal/user/user.go:103`，无缓存。

媒体库首页渲染 200 张卡片，每张封面一个鉴权请求，就是 200 多次
`SELECT * FROM users` 挤在 4 条连接上。HLS 播放期间每 4 秒一个分片请求也各查一次。

**改法**：加一个小的内存缓存（带短 TTL，比如 30 秒）。
注意权衡：禁用用户的失效会有最长 TTL 的延迟。

### 根因 4.4 · SQLite 连接池

`internal/db/db.go:24` 只设了 `SetMaxOpenConns(4)`，**`SetMaxIdleConns` 没设**
（默认 2）。并发超过 2 就反复真开真关，而 modernc 的 sqlite 每开一条要重跑 DSN 里
三条 PRAGMA。`cache_size` 也没设。

**改法**：`SetMaxIdleConns(4)` + DSN 加 `cache_size(-64000)`。

### 根因 4.5 · 首页每次进都全表扫加全排序

`internal/conf/conf.go:122` 的 `media_home_sort` 默认是 `random` →
`internal/server/handler_media.go:82-84` 走 `ORDER BY RANDOM()`，还带一个
`LEFT JOIN play_history`。

**而且即使在后台把它改成「最新在前」，`frontend/src/pages/LibraryVideo.vue:139` 和
`LibraryPhotos.vue:143` 的 Featured 轮播仍然恒发 `sort: 'random'`**，这个开关关不掉它。

**改法**：换成按 rowid 取样（`WHERE rowid >= abs(random()) % (max) LIMIT n`），
或者按 modified 取一批再在 Go 侧洗牌。

### 根因 4.6 · 搜索的索引是死的

`internal/server/handler_search.go:40-41` 用 `LIKE '%'||?||'%'`，前导通配让
`idx_files_name_lower` 完全用不上，全表扫加排序。

**改法**：上 FTS5。这条要新建表，属于改数据格式，建议单独一批做，不要和上面几条混。

### 验收

- 全量索引跑完后进程 RSS 不再按文件数线性增长（加一个 `len(d.cache)` 的调试端点观察）。
- 复制大目录时索引写入明显变快。
- 首页打开时间下降。

---

### 本批落地情况（2026-07-30，未提交）

4.1~4.5 全部落地，4.6 的 FTS5 **实测不可上**，另交付一条经量测的替代改动。全部 Go 单测通过
（`internal/server` 那个 `TestStorageCreateScansOnlyNewMount` 失败不属本批，见文末）。

#### 4.1 路径缓存 · 已落地

新增 `internal/util/ttlcache.go`：`TTLCache[V]`，TTL 过期 + LRU 上限，正条目与负条目
各一条 LRU 链、各自限额，淘汰恒 O(1)。过期条目读到即删。配套 `ttlcache_test.go` 7 个用例
（过期删除、LRU 淘汰、负条目不挤正条目、正负翻转、容量 0、并发）。

- `googledrive.go`：`cache map[string]cacheEntry` → `*util.TTLCache[gdFile]`，删掉
  `cacheEntry` / `cacheGet` / `cacheMissing`；`mu` 保留给已有的 `flight`（lookup singleflight）。
- `pikpak.go`：同上，`mu` 与 `sync` 导入一并删除。
- `onedrive.go`：`linkCache` → `*util.TTLCache[cachedLink]`，`cachedLink.exp` 字段删除
  （TTL 由缓存自己管），`linkMu` 换成 `linkOnce` + `links()` 惰性构造（测试不经 Init 直接构造驱动）。

限额：路径缓存正 20000 / 负 5000（每驱动约 8MB 上限），OneDrive 直链缓存 4096。

**刻意偏离计划两处**：

1. **没加 `len(d.cache)` 调试端点。** 验收项写的是「加端点观察 RSS 不再线性增长」，但上限
   一加，内存就由构造保证有界，端点只是观察手段而非需求；加它要动路由与鉴权，surface
   反而更大。
2. **Telegram 驱动没动。** `telegram.go:140` 的 `d.tree` 是整树快照、每次整体替换，
   本来就只占一份，不存在按路径数线性增长的问题。计划也没把它列进同型问题。

#### 4.2 scanSubtree · 已落地

`internal/index/index.go`：

- 抽出 `writeRows(tx, rows)`，`replaceAll` / `replaceSubtree` / 新增的 `upsertBatch` 共用。
- `scanSubtree` 拆成「登记 + 循环」与 `walkSubtree`：每 500 行（`subtreeScanBatch`）提交一次
  事务，不再每行一个独立事务。
- 并发去重照 `startSubtree` 的写法：同一子树在扫时只打标记，扫完再补一轮；同时在跑的扫描数
  由 `scanSubtreeLimit = 2` 约束（`runGated` 保证名额必还）。
- `walkSubtree` 内部 recover 成 error，goroutine 里不会漏掉 panic 也不会漏掉 map 键。

**刻意偏离计划一处**：计划写「套事务批量提交」，没说粒度为何不是整棵一个事务——
扫云盘要几分钟，一个长事务会把写锁攥住那么久，期间任何一次上传或播放记录都要在
`busy_timeout` 上干等。所以分批。

#### 4.3 users 查询缓存 · 已落地

`internal/user/user.go`：`GetByID` 加 30 秒 TTL 内存缓存。

- **按值存、返回副本**，调用方（`handler_user.go` 会拿 `*User` 去改字段）改不到缓存里的对象。
- 失败不缓存：删号后重建同 ID 不会被负缓存挡住。
- `Create` / `Update` / `UpdatePassword` / `Delete` 一律 `invalidate()` 整表清——用户数量小，
  整表清最省心，也不会漏掉「改了 A 只清了 B」。`Update` 出错也清（可能已部分生效）。
- 因此后台停用/降级当场生效，不存在最长 30 秒的延迟。真正会迟的只有绕过本进程直接改库。

#### 4.4 SQLite 连接池 · 已落地

`internal/db/db.go`：`SetMaxIdleConns(4)` 跟满 `SetMaxOpenConns(4)`；DSN 加
`cache_size(-64000)`（约 64MB 页缓存，默认只有 2MB）。

#### 4.5 首页取样 · 已落地，但改法与计划不同（实测推翻）

计划给的两个改法都量过，**第一个是负优化**。600k 行 / 200k 个视频、`cache_size(-64000)`：

| 写法 | LIMIT 5 | LIMIT 200 |
|---|---|---|
| `ORDER BY RANDOM()`（原样） | 657ms | 668ms |
| rowid 取样（计划的改法，200k 行时测） | 130ms（5 锚点） | 415ms（16 锚点）/ 26ms（单锚点） |
| JOIN 之后 `ORDER BY modified LIMIT/OFFSET` | — | 177~560ms |
| **取样压进子查询 + Go 侧洗牌（采用）** | — | **4.6ms（单窗）/ 34ms（8 窗）** |

要点：

- `ORDER BY RANDOM()` 的代价与 LIMIT 无关（整个候选集都要排），且随行数超线性——
  200k 行时 50ms，600k 行时 670ms。计划说它是问题，是对的。
- **计划建议的 rowid 取样反而更慢**：`rowid >=` 迫使按 rowid 扫表逐行过 `ext_type`，
  用不上 `idx_files_ext_type`；单锚点虽快（26ms）但取回的是**连续一段**，
  Featured 五连抽会全是同一部剧的相邻几集。
- 真正的原因是**取样与分页跑在 LEFT JOIN 之后**，跳过的每一行都要连一次 `play_history`。
  压进只查 `files` 的子查询、取回那几行之后才 JOIN，`OFFSET` 就只跳索引条目。

`internal/server/handler_media.go` 因此改成：

- `mediaFilter` 抽出「本用户可见的该类型媒体」条件（作用于 `files` 单表）。
- `mediaSelect` 统一走子查询形状，**随机与非随机两条路都用**——非随机的「查看全部」翻页
  同样受益（177~560ms → 4.6ms）。外层必须再写一次 `ORDER BY`：子查询行序不保证透传。
- `mediaRandom`：先数一次候选集；`total <= randomFullFetch(2000)` 就整个取回内存里洗
  （抽样绝对均匀，绝大多数库走这一支）；超过了才切 `randomSampleWindows(8)` 段随机窗口，
  撞车去重后不足则补抽，上限 `windows + randomSampleRetries`。
- `sort=random` 明确不认 `offset`（随机抽样没有下一页，前端首页本来也不翻页）。

前端那半条（后台设「最新在前」也关不掉 Featured 的 random）一并修，但**根因不止一个**：

1. `LibraryVideo.vue` / `LibraryPhotos.vue` 的 Featured 请求硬编码 `sort:'random'` → 改成跟随
   `app.mediaHomeSort`。
2. 更隐蔽的一条：`app.fetchPublic()` 在 `App.vue` 的 `onMounted` 里发，而子路由的 setup
   （`useMediaLibrary` 的 `immediate` watch）比它更早——**冷启动时网格也会先按默认值
   random 发一轮**，这个开关在刷新页面后同样形同失效。新增 `app.ensurePublic()`
   （去重的一次性请求），首页取法读设置前先 await 它。

新增 `internal/server/media_sample_test.go`：随机抽样满额/不重/全在库内 + 20 轮必须散布全库
（退化成「固定取最新一截」会被抓住）+ 候选集小于上限时整取 + 空结果 + 切窗口那支
（调小 `randomFullFetch` 覆盖）+ 分页顺序不重不漏 + 按名称升序。`-count=5` 稳定通过。

#### 4.6 搜索 FTS5 · **实测不可上，已放弃**

`modernc.org/sqlite v1.53.0`（内含 SQLite 3.53.2）的 FTS5 在批量写入下会崩，并且会写坏数据库。
证据（Windows，`-count=1` 各跑 8 次，10 万行 `files` + 外部内容 FTS5 + 三个同步触发器）：

| 配置 | 失败 | 失败形态 |
|---|---|---|
| `tokenize='trigram'`，单事务 | 2/8 | `SIGSEGV` 于 `_fts5ChunkIterate`；`database disk image is malformed (267)` |
| `tokenize='trigram'`，每 2000 行提交 | 1/8 | 同上 |
| `tokenize='unicode61'`，单事务 | 1/8 | 同上 |

崩溃栈固定在 `_sqlite3Fts5IndexSync → _fts5FlushOneHash → _fts5IndexAutomerge →
_fts5IndexMergeLevel → _fts5ChunkIterate`（空指针），是纯 Go 翻译层的缺陷，不是用法问题：
换分词器、换事务粒度都照崩。**「写坏用户数据库」是红线，不能为了搜索快而冒这个险。**

顺带确认的两件事（将来若换掉依赖可直接用）：

- FTS5 本身是编译进去的，`unicode61` 对中文**不切分**（`野生机器人` 是一个词元，
  `机器*` 命中 0），中文子串搜索**必须用 `tokenize='trigram'`**。
- trigram 对少于 3 个字的查询**静默返回 0 条**（不报错），所以必须保留 LIKE 回落，
  否则搜「电影」会一条都搜不到。
- 若改用 `INSERT ... ON CONFLICT(path) DO UPDATE` 代替 `INSERT OR REPLACE`，rowid 不变、
  AFTER UPDATE 触发器正常摘旧词元，就不必开 `recursive_triggers`（已写测试验证过，含
  `integrity-check`）。

替代方案也量过，**性价比不成立，没有采用**：覆盖索引
`(name_lower, is_dir, name, path, size, modified, ext_type)` 让 LIKE 走 covering index，
600k 行上 240ms → 200ms（快 17%），代价是库从 357MB 涨到 522MB（**+46%**）。

**实际交付的是另一条经量测的改动**：删掉 `idx_files_name_lower`。全仓唯一读 `name_lower`
的就是搜索的 `LIKE '%…%'`，前导通配让它必须整表扫（`EXPLAIN` 恒为 `SCAN files`），
这个索引一次都没被用上，只在写入侧收钱——30 万行实测索引重建 3.12s → 2.43s（**快 28%**）、
库 178.8MB → 153.2MB（**小 25.6MB**）。`migrate()` 里把 `CREATE INDEX` 换成
`DROP INDEX IF EXISTS`，`name_lower` 列保留。

搜索本身仍是 `LIKE` 整表扫，600k 行约 240ms。这是用户主动搜索时的一次性开销，不是常驻压力；
真要做快，得等 `modernc.org/sqlite` 修好 FTS5，或换 cgo 版 sqlite（会破掉 release.yml 的
纯 Go 交叉编译，代价更大）。

#### 本批改动文件

```
新增  internal/util/ttlcache.go            internal/util/ttlcache_test.go
新增  internal/server/media_sample_test.go
改    internal/db/db.go                    internal/index/index.go
改    internal/user/user.go                internal/server/handler_media.go
改    internal/driver/googledrive/googledrive.go
改    internal/driver/pikpak/pikpak.go     internal/driver/pikpak/pikpak_test.go
改    internal/driver/onedrive/onedrive.go
改    frontend/src/stores/app.js           frontend/src/composables/useMediaLibrary.js
改    frontend/src/pages/LibraryVideo.vue  frontend/src/pages/LibraryPhotos.vue
```

#### 记给别批的问题（本批没动）

`internal/server/storage_index_test.go:324` 的 `TestStorageCreateScansOnlyNewMount`
**确定性失败**（`索引条数应跟上实际行数 7, got 2`）。原因是第 2 批把 `refreshCount()` 改成了
异步（起 goroutine 数完再回填，本次先返回上一次的数字），而这个断言在一次 `Progress()`
之后立刻比对 `COUNT(*)`。`HEAD` 里的同名函数是同步的、测试是绿的，改动尚未提交。
**归第 2 批处理**：要么让测试轮询等条数收敛，要么给 `Progress` 加一个「正在数」的态。

---

## 第 5 批 · 起播慢与转码 CPU

**依赖第 3 批**（403 分类那条会影响这里的测量）。建议第 3 批之后做。

### 根因 5.1 · 多线程加速对走转码的片子完全无效（起播影响最大）

`internal/server/handler_raw.go:132` 判定内部回环后走 `stream.ServeSingle`
（`internal/stream/serve.go:119`），只有浏览器直连才走 `stream.Serve` 加分块加速。

而 `internal/media/info.go:13-15` 的 `directPlayExts` 只有 mp4 / m4v / mov / webm，
**mkv / ts / m2ts / avi / wmv 全部落到 HLS**，源读是 ffmpeg 经回环拉的，
全程一条 TCP、一个拥塞窗口、零预读缓冲——`serve.go:149` 拿到 `resp.Body` 之后
`serve.go:159` 直接 `io.Copy` 给 ffmpeg，云盘和 ffmpeg 之间没有任何解耦。

对比：direct 路径有 `window = max(32MB/4MB, 4) = 8` 块 = 32MB 预读
（`internal/stream/accel.go:110-112` + `internal/fs/fs.go:337`）；HLS 路径预读为 0，
只有内核 socket 缓冲和 `io.Copy` 的 32KB。

后果链：ffmpeg 一忙（编码、写盘），读端就停 → TCP 接收窗口收紧 → 跨国链路拥塞窗口
衰减 → ffmpeg 回来时带宽要重新爬坡。表现就是「拉流不积极、卡很久后台却没网络活动」。

**也就是说 `6a89c8b`「多线程加速终于是真的」只作用于 direct mp4。如果卡的是 mkv，
那次修复一个字节都没帮上。**

**改法**（二选一）
- 给内部读取方也走 `MultiReader`，但用大块少线程（`Threads=2`、`ChunkBytes=16MB`、
  `ReadaheadBytes=64MB`），避免 `serve.go:113-118` 注释担心的「每块一请求」风暴。
  改动集中在 `rawProxy`。
- 最小改动：在 `ServeSingle` 和 ffmpeg 之间插一个固定大小的环形预读缓冲
  （16–32MB）goroutine。

### 根因 5.2 · 服务端等 90 秒，客户端 10 秒就放弃

`frontend/src/utils/playerHost.js:247`

```js
hls = new Hls({ startPosition: resumeAt })
```

**hls.js 的加载策略一个都没配**，用的是默认值
`fragLoadPolicy.default.maxTimeToFirstByteMs = 10000`
（实读 `frontend/node_modules/hls.js/src/config.ts:513`，版本 1.6.16），
`timeoutRetry.maxNumRetry = 4`（config.ts:516），
`manifestLoadPolicy.default.maxLoadTimeMs = 20000`（config.ts:482）。

而服务端这边：
- event 模式轮询 `live.m3u8` 等第一个 `#EXTINF`，deadline **30 秒**（`internal/media/hls.go:599-618`）
- vod 模式 `init.mp4` 等 **20 秒**（`hls.go:654`）、`seg_0` 等 **90 秒**（`hls.go:684`）
- 全程不发一个字节

**服务端的耐心永远用不到，客户端 10 秒断一次、最多重试 5 轮约 50 秒，
每轮都在服务端留一个阻塞的 handler goroutine。**

**改法**：两头对齐。客户端显式设 `fragLoadPolicy.default.maxTimeToFirstByteMs`
到 30 秒以上；服务端把干等改成先回占位或分块响应保活。

**验证方法**：浏览器 Network 面板看 `seg_0.m4s` 有没有出现 10 秒整的 canceled 请求，
或者 hls.js 的 ERROR 事件里 `details: fragLoadTimeOut`。

### 根因 5.3 · 前台探测和后台预载抢同一把闸，没有优先级

`internal/media/hls.go:200` 的前台探测和 `internal/preload/preload.go:558` 的预载走的是
同一个 `Gate(2)`（`hls.go:78`），`internal/util/gate.go:41` 的 `Acquire` 是纯 FIFO
channel，不认前台后台。

单次 ffprobe 超时 45 秒（`internal/media/probe.go:87`），闸容量 2，预载并发 2。
预载正在探测两个跨国大文件时，点开视频要在闸上干等，最坏 45 秒才轮到。

**改法**：前台探测单开一把小闸，或给 `Gate` 加一条抢占通道；预载检测到有活跃播放
会话时自动让路。

注意：这条和第 1 批的「thumb 与 media 共用一把 Gate」有交互，两批不要同时改
`main.go` 的 Gate 装配。**如果第 1 批已经合了，这里在合并后的那把闸上加优先级。**

### 根因 5.4 · `media.Decide` 没有 singleflight

`internal/media/hls.go:176-213`：查内存 map → 查 `media_info` → `Acquire` → `runProbe`，
三步之间没有任何在途去重。对照 `internal/thumb/thumb.go:170` 的封面生成**是有**的。

详情卡和播放页并发请求、或者用户连点两次，就是两个 ffprobe 各占一个闸位——
闸总共才 2 个，把 5.3 的排队直接翻倍。

**改法**：照抄 `thumb.go:99-119` 的 singleflight 写法。

### 根因 5.5 · 每次播放都重做云盘 Stat，索引里现成的数据一次没用

`files` 表里 size 和 modified 都有（`internal/db/db.go:58-67`），预载已经在用这条路
（`preload.go:556` 直接用 DB 行构造 `model.FileInfo`，零网络），前台却全程走云盘：

1. `internal/fs/fs.go:383` 的 `LinkEx` 做一次 `drv.Stat`
2. `internal/server/handler_video.go:26` 的 `/video/info` 走 `fs.Get` 又打一次网络
3. `internal/media/hls.go:322` 的 `ensure` **第三次** `drv.Stat`

而且 `internal/driver/googledrive/googledrive.go:309` 的 `Link()` 返回值**本身就带
Size 和 Mod**，`fs.go:383` 那次 `Stat` 拿的是同一份数据，纯属多余。

Google Drive 的路径缓存 TTL 只有 2 分钟（`googledrive.go:112`），过期后
`googledrive.go:186` 的 `lookup` 会递归解析每一级父目录，每级一次 `listAll`
（`googledrive.go:153-183`，`pageSize=1000` 顺序翻页）。**隔 2 分钟再点一次，
就要把父目录整个重列一遍才能开始拉流。**

各驱动缓存 TTL：

| 驱动 | 路径缓存 | 直链缓存 |
|---|---|---|
| Google Drive | 2 min（`googledrive.go:112`） | 无（URL 由 ID 推导，Bearer 缓存到期前 5 min，`client.go:76`） |
| PikPak | 2 min（`pikpak.go:114`） | **无，每次 `Link()` 都发一次 API**（`pikpak.go:318`） |
| OneDrive | 按路径寻址无需解析 | 10 min（`onedrive.go:66`） |
| Telegram | 2 min 整树（`telegram.go:65`） | 本地 reader |

另外 `lookup` 没有 singleflight（`googledrive.go:186-225`），一屏封面加预载 worker
加播放请求同时打同一个目录，各列各的，N 份完整翻页。

**改法**
- `LinkEx` 去掉 `Stat`，用 `Link` 返回的 Size / Mod（需逐驱动确认都填了 Size）。
- `/video/info` 和 `/api/raw` 的文件信息优先取自 `files` 索引，索引缺失时才回落 `Stat`。
- Google Drive 路径缓存 TTL 提到 10–30 分钟（写操作已有 `cacheClear` 兜底）。
- `lookup` 套 singleflight。

### 根因 5.6 · 转码 ffmpeg 没有 `-threads`，不受任何闸限制（CPU 头号）

`internal/media/hls.go:555-557`

```go
a = append(a, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", ...)
```

**没有 `-threads`**，libx264 默认按核数开线程，**一路转码即可吃满全机**。
`hls.go:59-60` 的注释明确写了转码不受 `jobs` 闸限制，唯一约束是
`maxSessions = 2`（`hls.go:32`）。

而且**没有 `-re`**，即使只看了 1 分钟，ffmpeg 也在全速往后猛跑，
闲置 5 分钟（`hls.go:33` `idleTTL`）才被回收。

瞬时还能超：`hls.go:676-677` vod 拖动时 `killLocked` 后立刻 `startLocked`，
旧进程异步死（`hls.go:527` 最多等 10 秒）→ 短暂 4 路 x264。

全部 ffmpeg / ffprobe 调用点：

| 位置 | 二进制 | `-threads` | 闸 | 上限 | 超时 |
|---|---|---|---|---|---|
| `internal/media/probe.go:94` | ffprobe | 1（`probe.go:91`） | media 闸 | 2 | 45s（`probe.go:87`） |
| `internal/media/hls.go:158` | ffmpeg 抽帧 | 1（`hls.go:154`） | thumb 闸，云盘再叠 media 闸 | 2 | 60s×2（`hls.go:151`） |
| `internal/media/hls.go:476` | ffmpeg 转码 | **无** | **无闸** | `maxSessions=2` | **无** |
| `internal/server/handler_offline.go:314` | ffmpeg remux | **无** | `offline_workers` | 2 | **无** |

默认合计 8 个进程，其中 2 个可各自吃满全核。后台调到上限
（`media_jobs=8`、`offline_workers=32`）时是 50 个。全仓无 nice / 进程优先级控制。

**改法**：转码加 `-threads N`（N = 核数的一半），并纳入一把独立的转码闸；
vod 模式加 `-re` 或者段数上限。

### 根因 5.7 · HEVC 一律重编码（可能是 CPU 的最大浪费）

`internal/media/probe.go:111-119` 的 `playableVideoStream` 只认
h264（8bit 4:2:0）、vp9、av1，**HEVC 一律判为不可直出**，走
`libx264 -preset veryfast -crf 23` 重编码。

但 Safari 和 iOS 原生支持 fMP4 里的 HEVC（hvc1），本来可以 `-c:v copy` 纯 remux，
零编码开销。项目的主要使用场景是 iOS（锁屏、灵动岛、画中画那几个提交），
这条误判等于把本该零 CPU 的片子全部推去跑 x264。

**改法**：`/video/info` 增一个入参，前端用
`MediaSource.isTypeSupported('video/mp4; codecs="hvc1"')` 探测后带上，
Safari 侧把 hevc 判为可 copy。

**这条要改协议，风险也最高**（判错会让非 Safari 浏览器黑屏），**建议单独开一批做，
不要和上面几条混**。

### 落地情况（07-30，已改完未提交）

七条全部落地，`go build ./...` 与 `go test ./internal/media ./internal/util ./internal/stream
./internal/fs ./internal/db ./internal/driver/googledrive` 全绿。前端已 `vite build` +
`go build -o webvid.exe .` 重嵌。

| 项 | 落点 | 验证 |
|---|---|---|
| 5.1 | 新增 `internal/stream/readahead.go`（32×512KB 环形预读，接管 body 所有权），`ServeSingle` 的 `io.Copy` 前套上 | `readahead_test.go` 4 例：内容/EOF 不变、读取方不读时源侧仍被拉到缓冲上限、错误不被吞成 EOF、Close 幂等 |
| 5.2 | `playerHost.js` 加 `loadPolicy()`，manifest/playlist 45s、frag 首字节 100s | 与服务端 30s/20s/90s 三道 deadline 对齐 |
| 5.3 | `util.Gate` 加 `AcquirePriority` + `hand` 交棒通道；`media.decideNow(…, foreground)` 前台走它 | `gate_test.go` 两例：前台插在三个先排队的后台之前、插队不放大闸值 |
| 5.4 | `Decide` 拆出 `decideNow`/`runDecide` + `probeWait` 单飞 | `flight_test.go`：压闸到 1 并占住，8 个并发探测在途表恒为 1，收工清空 |
| 5.5 | `fs.LinkEx` 去掉 Stat（Size 缺失时回落）；`media` 加 `statEntry`（2min）供 `Info`→`ensure` 复用；gdrive TTL 2min→15min + `lookup` 单飞 | `lookup_flight_test.go`：8 个并发解析只列 3 次目录 |
| 5.6 | 转码加 `-threads`（核数一半）；vod 加 `maxLead=150`（领先 10 分钟即停，追上来 `-ss` 续转）；窗口外重启改成 kill→等退出→start | `pace_test.go` 两例：领先超限真停下且能续转出末尾分片、args 带 `-threads` |
| 5.7 | `Decision.VideoHEVC`(0/8/10) + `CopyWith(cap)` + `-tag:v hvc1`；`media_info` 加列 `video_hevc`（首次升级只清 `video_copy=0` 那批）；会话键带 cap；`/video/info?hevc=` 与分片 URI 一并注入；前端新增 `utils/codec.js` 实测能力 | `hevc_test.go`：真跑 libx265 样片，cap=0 走 vod 重编码、cap=8 走 event 直出且目录分开，产物拼回 ffprobe **仍是 hevc**（证明真 copy 且 hvc1 没写坏封装） |

刻意偏离计划的判断：

1. **5.1 取「最小改动」的环形预读，没给内部读取方上 `MultiReader`。** 计划列了两个方案。
   分块并发正是 `ServeSingle` 当初被造出来要避开的东西（`serve.go` 那段注释写得很重：
   顺序整读大文件的「每块一请求」会触发云盘频率限流，断流即「一直重连」）。预读缓冲
   直接解决计划里点明的机制（ffmpeg 一停就顶回 TCP 接收窗口），且不动请求形态。
   若实机测出单条 TCP 是带宽瓶颈，再上大块少线程的 `MultiReader`。
2. **5.3 用交棒而非「前台单开一把小闸」。** 单开小闸等于把「总闸 N」变成 N+1，与
   1.8/5.6 抱怨的「名不副实」自相矛盾；预留车道（后台最多占 N-1）在默认 N=2 时会让
   预载吞吐直接腰斩。交棒不损失容量、不放大闸值，代价是**前台最坏仍要等手头那件后台
   活跑完**（单次 ffprobe 45s）。这是本批唯一没被完全消掉的等待。
3. **5.6 用「段数上限」而非 `-re`。** `-re` 把产出压到 1× 实时，播放器永远贴在生成边界上，
   网络一抖就卡。段数上限保留了激进缓冲，只砍掉「看 5 分钟却转完整片」的浪费。
4. **5.6 没有另加一把「转码闸」。** `maxSessions=2` 已经是转码进程数的上限，再叠一把闸
   是同一个约束写两遍。瞬时超额的真实成因是 kill 后立刻 start，已改成等旧进程退出。
5. **5.7 额外加了一条退路**（计划没要求）：`PlayInfo.HEVC` 告诉前端「这次直出靠的是你
   自报的能力」，hls.js 报 fatal `MEDIA_ERROR` 时 `demoteHevc()` + `forgetVideoInfo()`，
   本会话此后一律转码。能力探测再准也只是探测，判错的后果是黑屏，必须有退路。
   Safari 原生 HLS 那条链的 `art.on('error')` 归第 3 批（3.5）在改，没动。
6. **`fs.LinkEx` 保留了 Size 缺失时回落 Stat。** 计划说「需逐驱动确认都填了 Size」——
   五个驱动确实都填了，但这是约定而非编译期约束，漏填会让该存储上每个文件都变成
   0 字节（Range 直接 416，测试替身就撞上了）。这种错法不能只靠约定挡。

未做的：

- **`/video/info` 与 `/api/raw` 改从 `files` 索引取文件信息**（5.5 第 2 条）。`LinkEx` 去掉
  Stat 之后 `/api/raw` 已经零 Stat，这条对它失去意义；`/video/info` 那次 Stat 若改读索引，
  索引一旦滞后（size/mtime 与真实不一致）会让 `media_info` 的缓存键全部错位、每次播放
  重探。收益（一次已被 15min 路径缓存兜住的 Stat）远小于风险。
- **event 模式（remux）没有段数上限。** 它的列表由 ffmpeg 自己写，中途停掉就再也接不上
  （不支持 `-ss` 重启）。「看 5 分钟却把整片拉完」这个带宽问题在 remux 路径上仍存在，
  就是下面「需要实机测的」第 4 条。
- `handler_offline.go:314` 的 remux ffmpeg 没加 `-threads`：那是 `-c copy`，不编码，加了
  没有意义；且该文件归第 1 批在改。

并行会话交叉说明（改完时的状态）：

- 本批与第 1 批共用 `internal/media/hls.go`（他们改 `input()` 的 ID 校验与 `SetGate`）、
  与第 3 批共用 `internal/stream/serve.go`（他们改 `Serve` 的 `WriteHeader` 时序、我只动
  `ServeSingle` 尾部）与 `playerHost.js`（他们改 `fail`/`art.on('error')`、我只动
  `new Hls(...)` 与 hls.js 的 `MEDIA_ERROR` 分支）、与第 4 批共用
  `googledrive.go`（他们把 cache 换成 `util.TTLCache`，我的 `lookup` 单飞已与之合并）
  与 `db.go`。这些文件里同时有别人的在途改动，**提交时按文件粒度会一并带走**。
- 全量 `go test ./internal/...` 尚有一处失败，在第 4 批正在重写的文件里，与本批无关：
  `TestStorageCreateScansOnlyNewMount`（`index.go` 子树扫描事务，+182 行在途）。
  `go vet ./...` 干净。

### 需要实机测的

1. **先挂 pprof**。全仓无 `net/http/pprof`（`main.go` 和 `internal/server/router.go`
   都没有），这是所有后续测量的前提。挂一个受管理员鉴权保护的端点。
2. **5.1 的量级**：播一部 mkv，在服务器上 `ss -ti` 看到 googleapis 那条连接，
   观察 `cwnd` / `bytes_acked` 是否周期性塌陷；对照播同尺寸 mp4 时的多条连接。
   `NL_STREAM_DEBUG=1` 只覆盖 direct 路径，HLS 路径没有埋点。
3. **5.6 的核数占用**：`pidstat -p <ffmpeg> 1` 看 `%CPU` 是否接近 100×核数。
   同时数 `data/transcode/*/seg_*.m4s` 的增长速度——远快于 4 秒每秒的播放消耗就坐实
   「ffmpeg 在猛跑」。改完后 `%CPU` 应封顶在 50×核数附近，且分片数停在领先 150 个。
4. **event 模式整片下载的带宽代价**：播 5 分钟就退出，看 `data/transcode/<id>/`
   是否仍在增长。这条直接关系到 Google Drive 750GB/天的上限。
5. **5.7 的真机确认**：iPhone / iPad Safari 与桌面 Chrome 各点一部 HEVC 片子，后台日志
   看会话是 event（直出）还是 vod（重编码），并确认桌面 Chrome 上没有黑屏。
   浏览器控制台跑 `MediaSource.isTypeSupported('video/mp4; codecs="hvc1.2.4.L120.B0"')`
   可直接看到本机上报的能力。
6. **升级后首轮预载会变大**：`video_hevc` 加列时清掉了 `video_copy=0` 的探测缓存
   （即原先要重编码的那批），这批要重探一次。看后台预载卡片的总数是否与该批数量相符。

---

## 第 6 批 · 打磨

**独立，改动都很小，可以放在最后一起做。**

> **状态：07-29 已全部做完（未提交，见文末「本批的提交处境」）。**
> 6.1 四处改完（`AdminIndex.vue` 只动了 `<style>`，另加一条 CSS 让「正在扫描/正在预载」
> 的路径单行截断——那行每 1.5 秒换一次，换行就长高一截）。6.2 `genImage` 解码前查
> 图片头，超 16MP 改走 ffmpeg（子进程 + YUV，内存低一个量级）。6.3 分片数钳到 24 小时。
> 6.4 终态任务保留 200 条，提交与收尾各淘汰一次。6.5 `LoginManager` 加分钟级巡检，
> 过期的 pending 连接自动停掉（首次发码才起协程）。6.6 `NewTicker` + `Close` 停表。
> 6.7 见下方「与计划不同的一处」。

### 6.1 · 会被文字撑变形的卡片

全量扫过 24 个前端源文件。项目整体是克制的——VideoCard / MediaGridCard / PhotoCard
和网格布局都做对了（`nowrap + ellipsis`、flex 子项配 `min-width: 0`）。要改的是四处：

| 位置 | 是什么 | 问题 | 改法 |
|---|---|---|---|
| `frontend/src/components/FeaturedCarousel.vue:64` `.feat-title` | 主页顶部大横幅，进站第一眼 | 34px 标题无任何行数限制，外层 `.feat` 固定 420px 且 `overflow:hidden`。长文件名换 2、3 行会把上方 kicker 顶掉或被裁 | 加 `-webkit-line-clamp: 2` |
| `frontend/src/components/VideoDetailCard.vue:288` `.vdc-title` | 视频详情弹窗，几乎每次交互都开 | 24px 标题无行数限制，弹窗宽度固定 720px 但高度纯 auto。同一批文件打开时高矮不一，hero 转场的目标尺寸也跟着不稳 | 加 `-webkit-line-clamp: 2`（详情信息已有原生 tooltip 兜底全名） |
| `frontend/src/assets/media-library.css:14` `.lib-title` | 视频库/照片墙二级页大标题 | flex 子项，无 `min-width:0`、无 ellipsis，长目录名换行撑高整个页头 | `white-space:nowrap + overflow:hidden + text-overflow:ellipsis`，并补 `min-width:0` |
| `frontend/src/pages/admin/AdminIndex.vue:225-253` `.index-card` | 后台索引管理两张状态卡 | 无 `min-height`，卡内段落数随状态分支变化，还每 1.5 秒轮询一次，两张卡忽高忽低 | 给一个 `min-height`（按最多行数估，约 200px），或把不确定的文案行也用现有的 `.ph` 占位块占位化 |

**以下三类是合理设计，不要动**：`VideoDetailCard.vue:293` 的 `.vdc-badge`、
`Play.vue:154` 的转码状态 `.badge`（已正确配了 `flex:none` 加标题侧
`min-width:0 + ellipsis`）、后台各处的 `el-tag`（Element Plus 默认按文字自适应，
移动端已加了 `min-width:0 + ellipsis` 兜底）。

### 6.2 · 本地图片缩略图没有像素上限

`internal/thumb/thumb.go:442` 的 `imaging.Open` 直接全解码，解完才判宽度
（`thumb.go:446-448`），用的还是 Lanczos。一张 8000×6000 的 JPEG 解成 NRGBA 是 192MB，
`AutoOrientation` 可能再复制一份，抽帧闸 2 并发，峰值几百 MB。

云盘走的是下载云端缩略图那条路（`thumb.go:427` 有 20MB 上限），不受影响。

**改法**：解码前先读图片头查像素数，超阈值改走 ffmpeg 缩放。

### 6.3 · `nSegs` 没有上限校验

`internal/media/hls.go:347` 直接用 ffprobe 报的 `Duration` 算分片数。
畸形文件报出一个荒谬的时长，`vodPlaylist`（`hls.go:623-637`）单次请求就会构建一个
几 GB 的字符串。

**改法**：钳制到 24 小时。

### 6.4 · 任务表没有自动淘汰

`internal/task/task.go:307` 的 `tasks` map 无自动淘汰，每个 Task 还扣着完整的
`files []*FileProgress`（`task.go:117`）。一次 10 万文件的转存任务完成后仍占约 20MB，
直到用户手动点「清除已完成」。

**改法**：终态任务保留上限（200 条或 24 小时），或者完成后释放 `files` 清单。

### 6.5 · Telegram 登录会话滞留

`internal/driver/telegram/login.go:207` 的 `bg.Connect` 会起一条常驻 MTProto 连接存进
`pending`。管理员点了「发送验证码」之后不管，这条连接会一直活着——只有同一个存储再
发起一次登录（`login.go:227`）或者登录成功（`login.go:270`）才释放，**没有按时间清理**。

规模上是每个存储最多滞留一条，不是无界增长，所以不严重。

**改法**：加一个 janitor，超过一定时间（如 10 分钟）没完成的 pending 自动清掉。

### 6.6 · `janitor` 用了不可停的 `time.Tick`

`internal/media/hls.go:402`

```go
for range time.Tick(time.Minute) {
```

底层 Ticker 无法被 GC 回收，循环没有退出条件，`hls.go:424` 的 `Close()` 也不停它。
生产上只有一个实例，影响可控，但写法该改成 `NewTicker` + `Stop`。

### 6.7 · 分块 buffer 没有复用

`internal/stream/accel.go:464` 每块 `make([]byte, size)`，**全仓 0 处 `sync.Pool`**。
25 Mbit/s 的流约等于每秒 3MB 的 4MB 大对象垃圾。

同时 `accel.go:110-111` 的 `window()` 是 `max(预读/分块, 线程数)`，
**线程数会架空预读上限**。按 `internal/fs/fs.go:339-345` 允许的极值
（线程 32、分块 64MB），单条流就是 32 × 64MB = **2GB**。

默认值是 `max(32/4, 4) × 4MB = 32MB` 每条流，且路由层对并发流数没有任何闸。

**改法**：`window()` 改成 `min(max(预读/分块, 1), 线程数×2)`；
加一个全局在途拉流字节预算闸。

**与计划不同的一处（07-29 实做）**：`window()` 落成 `max(预读/分块, 1)`，
**没有**加 `线程数×2` 那道上限。原因是那道上限会把预读按线程数砍下去：单线程配
32MB 预读的流，窗口会从 8 块掉到 2 块。`accel_test.go` 的 `TestReadaheadWidensWindow`
正是奔着「worker 得以跑在读端前面，某块慢不再让整条流停摆」写的，加上限就直接判它错。
去掉 max(线程数) 已经足够堵住原来的漏洞——窗口只认预读，32 线程 × 64MB 那个 2GB 的
极值配置现在等于用户填的预读值。

全局预算落在新文件 `internal/stream/budget.go`：默认 1GiB，设了 `GOMEMLIMIT` 则取其
1/4。预算不够时新流退化为最小窗口（一块）而不是排队等——一条读不出数据的流对用户就是
卡住了。`NewMultiReader` 申请、`Close` 归还，降级时打一行日志（不静默截断）。

分块 buffer 池化按本节原话「风险大」未做。

**buffer 池化风险大**（归还时机跨 `Read` 调用，容易 use-after-free），
如果做要格外小心，或者干脆先只做 `GOMEMLIMIT`（部署侧环境变量即可）。

---

## 待实机确认的清单

这些代码里断不了，需要在服务器上看：

1. **Google Drive 返回的实际状态码**。复现后抓日志里 `accel.go:393` 那行。见第 3 批。
2. **「2000 连接」的构成**。`ss -s`、`ss -tan | awk '{print $1}' | sort | uniq -c`、
   `ls /proc/$(pidof webvid)/fd | wc -l`。CLOSE_WAIT 占大头 → 根因 1.5 坐实；
   TIME_WAIT 占大头 → 根因 1.7 坐实。
3. **「服务直接崩掉」的死因**。`journalctl -u webvid -n 200` 与
   `journalctl -k | grep -i "killed process"`。判断是 OOM 还是别的。
   注意 `install.sh:216` 的 `Restart=on-failure` 会掩盖现场。
4. **后台预载卡片上有没有红字失败数**。有 → 根因 1.1 坐实；
   显示「封面 N / N 已缓存」却看不到封面 → 根因 1.2 坐实。
5. **`data/thumbs/` 里的文件数**与卡片上的总数对比。接近 → 根因 1.2；远小于 → 根因 1.1。
6. **当前的 `preload_workers` / `media_jobs` 实际值**。
   `sqlite3 data/newlist.db "select key,value from settings where key in ('preload_workers','media_jobs')"`。
   若被调到 16/8，上面所有并发数字都要乘上去。
7. **挂的是哪些驱动**。后台「存储」列表，或 `storages.driver` 列。决定哪条路径先修。

---

## 更新日志文风

v2.0.0 到 v2.2.0 用的是同一套骨架，效果好，继续用：

```
一句话说清这版的主题。

## 新增

• 每条动词打头，说清行为变化

## 优化

• 同上

## 修复

• 同上
```

v2.3.0 的写法是失败的，四个问题：

1. **结构自创**。放弃了已经稳定三个版本的「新增 / 优化 / 修复」分类，换成自拟的加粗
   抒情小标题，读者无法快速定位「这版修了什么」。
2. **「过去…现在…」对比叙事**。每段都先铺垫旧行为再讲新行为。Apple 的写法是直接讲
   现在是什么样。
3. **拟人化与文学化用词**。「榨干机器」「CPU 与带宽反复空转」「失败即终结」
   「一眼看出还差多少」——形容词在替事实说话。
4. **破折号铺陈 + 泄露内部实现**。「什么也没留下——每次打开主页都要从头再来」；
   「生成过程与网页请求分开」「限制为单线程」「共用一个可调的并发上限」这类是给
   开发者看的，不该出现在用户侧。

规则：禁 emoji、禁编号符号、禁破折号铺陈、禁营销词。是什么就写什么。

---

## 附：验证与构建

```bash
# 前端改动后必须重新嵌入
cd frontend && npx vite build
cd .. && go build -o webvid.exe .

# 后端单测
go test ./internal/...

# 相关 e2e（在 frontend/e2e 或仓库根，视脚本位置）
# upload-conflict-check.mjs、player-check（需 NL_BASE）
```

提交时显式列文件，先清暂存区：

```bash
git reset
git add <本次真正改的文件>
git commit
```

---

## 本批的提交处境（第 6 批，07-29）

做第 6 批时第 1/2/3/4 批都在并行开着，工作区里几个文件是混着的：
`thumb.go`（第 1 批）、`hls.go`（第 1 批）、`accel.go`（第 3 批）、
`AdminIndex.vue`（第 2 批）里同时有别人和第 6 批的改动。**第 6 批没有提交**，
以免把别人没写完的活一并 commit 走。

只属于第 6 批、可以单独提交的：

```
frontend/src/assets/media-library.css
frontend/src/components/FeaturedCarousel.vue
frontend/src/components/VideoDetailCard.vue
internal/task/task.go
internal/driver/telegram/login.go
internal/stream/budget.go            # 新文件
internal/stream/budget_test.go       # 新文件
```

后两个新文件依赖 `accel.go` 里那两行调用，单独提交会让 `budget_test.go` 挂，
要么和 `accel.go` 一起走（连带第 3 批），要么等第 3 批提交完再补。

混在别人文件里的第 6 批改动（由拿到该文件的会话一并带走即可，内容都是要的）：
`thumb.go` 的 `maxDecodePixels/genImage/hugeImage`、`hls.go` 的 `maxDuration` 与
janitor 三处、`accel.go` 的 `window()`/预算申请归还两行、`AdminIndex.vue` 的 `<style>` 块。

顺带：`go build ./...` 此刻停在 `internal/preload`（第 1 批在改，`coverTimeout`/
`probeTimeout` 还没定义）；`internal/server` 的 `TestStorageCreateScansOnlyNewMount`
挂在第 2 批把 `refreshCount` 改成后台异步之后（条数晚一轮才跟上，那条断言要跟着改）。
两者都与第 6 批无关。

---

## 本批的提交处境（第 1 批，07-30）

第 1 批做完时第 2/3/4/5/6 批都在并行开着。**没有提交**——六个文件里混着别人没写完的活，
`git add` 会一并 commit 走。

### 问题：第 1 批没有能单飞的子集

只属于第 1 批、内容完整的文件：

```
main.go
internal/util/httpclient.go          # 新文件
internal/preload/preload_test.go
internal/server/handler_offline.go
internal/driver/onedrive/graph.go
internal/driver/pikpak/client.go
```

**但这几个单独提交编译不过**：`main.go` 调的 `th.SetGate` / `md.SetGate` 定义在
`thumb.go` 与 `hls.go` 里，这两个文件是混的。所以第 1 批要么整批走，要么等混合文件的
另一半落定——没有中间选项。

### 混在别人文件里的第 1 批改动

拿到该文件的会话一并带走即可，内容都是要的：

| 文件 | 第 1 批的部分 | 同文件里别人的部分 |
|---|---|---|
| `internal/preload/preload.go` | 除 `StartedAt` 外的全部改动 | 第 2 批的 `StartedAt` |
| `internal/thumb/thumb.go` | `normWidth` 与宽度档位、`dl` 闸与 `dlLimit`、`dlClient`、`SetGate`、`Cover` 的 `\|vframe` 分支 | 第 6 批的 `maxDecodePixels` / `hugeImage` / `genImage` 加 ctx |
| `internal/media/hls.go` | `input` 的 `u.ID <= 0` 防呆、`SetGate` | 第 5/6 批的 `maxDuration`、`probeWait`、`stats`、janitor 的 stop 通道 |
| `internal/media/info.go` | `ProbedSet` / `Probed()` / `ProbeStatus` 改签名 | 第 5 批给 `Info` 加 `hevcCap` |
| `internal/server/handler_video.go` | `coverWait` / `coverBuild` 与整个 `thumbHandler` | 第 5 批的 `hevcCap` |
| `internal/driver/googledrive/client.go` | `httpClient` 补 Transport | 第 3 批的 `refreshedAt` |

### 解决办法：等六批落定后一次走完

按 hunk 切文件分批提交在技术上做得到（`git apply --cached` 喂只含本批 hunk 的 patch），
但这一轮不该那么做，两个理由：

1. **六批之间有真实的接口依赖**，切开就有编译不过的中间态，出问题时找不到一个干净的
   回滚点：1.8 的单闸 ↔ 5.3 在闸上加的优先级；1.2 的宽度档位 ↔ 6.2 改签名的 `genImage`；
   1.1 的预载身份 ↔ 5.5 的 stat 复用。
2. **这一轮六批本来就是同一件事**（重发 v2.3.0 前的整改），一次提交在历史上更好读。

提交前必做，顺序不能颠倒：

```bash
git reset                        # 清暂存区：别人 git add 过的不能带走
cd frontend && npx vite build    # 第 2/6 批改了前端，不重新嵌入运行时看到的还是旧页面
cd .. && go build -o webvid.exe .
go test ./internal/...           # 六批的测试要全绿，尤其第 5 批那几处签名变更
```

### 本批的测试状态

- `go build ./...` 通过。
- `internal/preload`、`internal/util`、四个驱动包全绿。
- `internal/media`、`internal/server` 编译不过：第 5 批给 `Segment` / `Playlist` / `Info`
  加了参数而测试还没跟上。**与第 1 批无关**，已在 `HEAD` 的干净副本上跑同样的用例全绿
  （`git worktree add <临时目录> HEAD --detach`，把 `public/dist` 拷进去即可复现这个对照）。

---

## 六批合并后的验收（2026-07-30）

六批全部落地后的一次统一验收。手段：全量 `go vet` + `go test`、逐条落点对照源码、
以及**起一套隔离实例跑真实请求**（`NL_PORT=5299` / `NL_DATA_DIR` / `NL_FILES_DIR` 指向
临时目录，样本用 ffmpeg 现造：h264 mp4 / h264 mkv / HEVC mp4 / HEVC mkv / mpeg4 avi /
5000×4000 JPEG / 1280×720 JPEG / 带 EXIF 方向的大图与小图）。

### 结论

`go build ./...`、`go vet ./...` 干净，`go test ./internal/...` **全绿**（修完下面两条之后）。
二进制与 `public/dist` 均为最新（`find -newer` 双向核对）。

### 实机测到的效果

| 项 | 观测 |
|---|---|
| 1.2 宽度归档 | `size=320` 与 `size=480` 命中**同一份**缓存（字节数一致、亚毫秒返回），`size=1200` 首次现场生成后落盘、二次 0.9ms 命中。产物实测 640×512 / 1280×1024 |
| 1.4 失败不算完成 | 有失败时日志写「预载停下：1 / 9 项没做成」，`finished_at` 为空、`will_retry: true`、`fail_note` 带原因；修完之后同一套样本 `failed: 0` 且 `finished_at` 正常写入 |
| 1.5 封面不再钉住连接 | 生成失败的封面 28ms 内返回 404（此前这条连接要占 5 分钟）；`coverWait = 8s` 的上限本次未在真实争抢下复现，仅代码核对 |
| 2 索引进度 | 两个进度接口均 <1ms 返回；`rebuild` 后条数在下一轮轮询内收敛到真实行数 |
| 4.5 首页取样 | `sort=random` 与按名分页均 <1ms（本地库样本小，绝对值不说明问题，形状正确） |
| 5.7 HEVC 直出 | HEVC in mkv：`hevc=0` → `transcode`；`hevc=8/10` → `remux` + `hevc: true`。HEVC in mp4 仍走 `direct`（扩展名直出的既有设计，不经 HLS） |
| 6.2 巨图分支 | 5000×4000 JPEG 三档全部出图；与 imaging 分支的 EXIF 方向**一致**（同为 640×800，ffmpeg 8.1 会按 EXIF 校正，此前担心的方向丢失不成立） |

第 3 批（Google Drive 403 / 首块 502）与 1.1（预载身份）只在真实云盘上显形，本地盘不走
回环，验收止于单测与代码核对，仍需实机。

### 验收中发现并修掉的两条

**A · 6.2 的巨图分支恒失败**（`internal/media/hls.go`、`internal/thumb/thumb.go`）

`genImage` 对超过 `maxDecodePixels` 的图片改走 `media.FrameAt(..., "0")`。**单帧输入上任何
定位参数都会把唯一那一帧丢掉，而 ffmpeg 退出码仍是 0**，只是什么也没输出——失败只表现为
「产物为空」。结果：所有超过 16MP 的图片**彻底没有封面**，且每轮预载都算一次失败、按
2 分钟起翻倍退避重试到 6 小时。等于把「内存峰值高」换成了「没有封面 + 永久重试」。

改法：`FrameAt` 的偏移传空串时一个定位参数都不加；新增 `media.FrameFirst` 作为静态图的
自解释入口（`FrameAt(..., "", "")` 这种两个空串的写法太容易看错——落地过程中就先错了一次，
把空串喂给了 `internalToken`）。新增 `internal/media/frame_test.go` 两例：静态图必须出帧、
视频仍按秒点定位且首帧回退可用。**已反证是真回归测试**（把偏移换回 `"0"` 立刻失败）。

**B · `TestStorageCreateScansOnlyNewMount` 确定性失败**（`internal/server/storage_index_test.go`）

即第 4 批「记给别批的问题」那条，第 2 批没有处理。第 2 批把 `refreshCount()` 改成后台异步
之后，「读一次进度就该拿到新条数」的断言不再成立。按计划给的两个选项取「测试轮询等收敛」：
抽出 `waitScanned`，要求**有界时间（10s）内必须收敛到真实行数**——一直不收敛（回填被代际号
误判作废、goroutine 没起来）依然是失败，断言强度没有降低。`-count=3` 稳定通过。

### 还没被验证过的（需实机）

计划末尾「待实机确认的清单」与第 5 批「需要实机测的」两节仍然有效，逐条未动。另外补两条：

- `coverWait = 8s` 的上限没在真实争抢下复现过（本地样本抽帧太快，压不出排队）。
- 转码的 `-threads` 只由 `pace_test.go` 的参数断言和源码核对保证，没抓到活着的进程命令行
  （本机 `wmic` 已不可用，`Get-CimInstance` 轮询没赶上短命的转码进程）。服务器上按第 5 批
  「需要实机测的」第 3 条用 `pidstat` 看即可。
