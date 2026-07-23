# 方案：离线下载支持 HLS(m3u8) 拉取合并 + 自定义文件名

## Context（为什么做）

`newlist` 是类 Alist 的自建网盘（Go + Gin，部署在 VPS 自用）。现有"离线下载"
（`POST /api/fs/offline`）只做**一次普通 HTTP GET → 流式写入存储**。当用户粘贴一个
HLS 视频的 `.m3u8` 链接时，服务器只会把那几 KB 的**播放列表文本**当成文件存下来，
拿不到真正的视频——因为 HLS 视频被切成了成百上千个 `.ts` 分片，`.m3u8` 只是分片目录。

本次改动：让离线下载识别 HLS 链接，用项目里**已有的 ffmpeg 依赖**把全部分片拉取并
无损合并成单个 `mp4` 存进网盘；同时允许**自定义下载后的文件名**（现在 HLS 链接末段
常是 `video.m3u8`/`playlist.m3u8`，派生出的名字没意义，所以自定义名尤其必要）。

定位是**自用**，因此不做防盗链 UI、不做 SSRF 深度隔离、不做清晰度选择——最小实现，
"粘贴链接 → 视频落到网盘"。

---

## 目标（做什么）

1. 离线下载框粘贴 `.m3u8` 链接 → 后台任务用 ffmpeg 拉全部分片、remux 成单个 mp4 存入目标目录。
2. 离线下载框新增可选"文件名"输入，可自定义落地文件名（HLS 会自动补 `.mp4`）。
3. 普通直链下载行为**完全不变**（仅额外支持可选自定义文件名）。

---

## 集成点与可复用资产（关键文件）

| 用途 | 位置 | 说明 |
|---|---|---|
| 离线下载 handler | `internal/server/handler_offline.go` | `fsOffline`（建任务）+ `offlineFetch`（普通 GET 拉流）在此 |
| ffmpeg 二进制探测 | `internal/media/probe.go` | `media.LookTool("ffmpeg"/"ffprobe") string`（"" = 不可用）；`media.ErrNoFFmpeg` |
| ffmpeg 断流续传旗标参考 | `internal/media/probe.go` `httpInputArgs`（**未导出，抄参数即可**） | `-reconnect 1 -reconnect_streamed 1 -reconnect_delay_max 30 -reconnect_on_http_error 429,5xx` |
| 任务进度 | `internal/task/task.go` | `t.SetFile(name)` / `t.SetTotal(n int64)` / `t.Add(n int64)` |
| 写入存储 | `internal/fs/fs.go:482` | `func (f *FS) Put(ctx, u, dstDir, name string, r io.Reader, size int64, overwrite bool) error` |
| 读回文件/建索引 | `handler_offline.go` 现有收尾 | `s.fs.Get(...)` + `s.index.Upsert(target, fi)`；`util.JoinLogical(dstDir, name)` |
| Server 字段 | `internal/server/server.go:19` | 已持有 `media *media.Service`、`fs`、`tasks`、`limDown`、`index`（无需改构造签名） |
| 数据目录 | `main.go:53` | `dataDir := env("NL_DATA_DIR", "./data")`——临时文件目录按同规则取，避免改 `server.New` 签名波及 6 处测试 |
| 前端离线对话框 | `frontend/src/pages/Files.vue`（约 107-190 行） | `offlineVisible` / `offlineUrls` / `submitOffline()` |
| 前端 API | `frontend/src/utils/api.js:25` | `offline: (urls, dst_dir) => http.post('/fs/offline', { urls, dst_dir })` |

> 注意：`server.New` 签名有 6 个调用点（`main.go` + 5 个 `internal/server/*_test.go`），
> **不要动它的签名**；临时目录用 `os.Getenv("NL_DATA_DIR")`（缺省 `./data`）就地取。

---

## 怎么做（改动清单）

### 后端 A — `internal/server/handler_offline.go`

**A1. 扩展请求结构 `fsOffline`**，加可选 `name`：

```go
var req struct {
    URLs   []string `json:"urls"`
    DstDir string   `json:"dst_dir"`
    Name   string   `json:"name"` // 可选：自定义文件名，仅单链接时生效
}
```

建任务前统计有效 URL 数；**仅当有效 URL 恰为 1 个时**才把 `req.Name` 作为自定义名，
多链接时忽略（防同名互相覆盖）：

```go
// 先把有效 url 收集到 valid []string（复用现有校验逻辑：trim、http/https、host 非空）
customName := ""
if len(valid) == 1 { customName = req.Name }
```

任务闭包多传一个参数：
```go
return s.offlineFetch(ctx, u, t, srcURL, dst, customName)
```

**A2. `offlineFetch` 增加 HLS 分支**（保持现有 SSRF 守护的 `offlineClient` 先行 GET，
以此校验入口 host 是公网，再决定走哪条路）：

```go
func (s *Server) offlineFetch(ctx context.Context, u *user.User, t *task.Task, srcURL, dstDir, customName string) error {
    // ...现有 GET 到 resp...
    if isHLS(resp, srcURL) {
        resp.Body.Close() // 不把播放列表文本当文件；交给 ffmpeg 重新拉
        return s.offlineFetchHLS(ctx, u, t, srcURL, dstDir, customName)
    }
    // 现有普通逻辑，仅文件名改为：customName 优先，否则 offlineFilename(resp, srcURL)
    name := sanitizeName(customName)
    if name == "" { name = offlineFilename(resp, srcURL) }
    // ...其余不变...
}
```

**A3. HLS 判定辅助**：

```go
func isHLS(resp *http.Response, srcURL string) bool {
    if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "mpegurl") {
        return true // application/vnd.apple.mpegurl / application/x-mpegurl / audio/x-mpegurl
    }
    if pu, err := url.Parse(srcURL); err == nil {
        return strings.HasSuffix(strings.ToLower(pu.Path), ".m3u8")
    }
    return false
}
```

**A4. 输出文件名辅助**（自定义优先，强制 `.mp4`；否则 URL 末段去扩展名，兜底 `video`）：

```go
func hlsOutName(custom, srcURL string) string {
    if c := sanitizeName(custom); c != "" {
        if !strings.HasSuffix(strings.ToLower(c), ".mp4") { c += ".mp4" }
        return c
    }
    name := "video"
    if pu, err := url.Parse(srcURL); err == nil {
        b := path.Base(pu.Path)
        if dec, e := url.PathUnescape(b); e == nil { b = dec }
        b = strings.TrimSuffix(b, path.Ext(b)) // 去掉 .m3u8
        if s := sanitizeName(b); s != "" { name = s }
    }
    return name + ".mp4"
}
```

**A5. 新增 `offlineFetchHLS`**（核心）：

```go
func (s *Server) offlineFetchHLS(ctx context.Context, u *user.User, t *task.Task, srcURL, dstDir, customName string) error {
    ffmpeg := media.LookTool("ffmpeg")
    if ffmpeg == "" { return media.ErrNoFFmpeg }

    name := hlsOutName(customName, srcURL)
    t.SetFile(name)

    // 临时文件放数据盘（勿用 /tmp，可能是 tmpfs），完成即删
    dataDir := os.Getenv("NL_DATA_DIR")
    if dataDir == "" { dataDir = "./data" }
    tmpDir := filepath.Join(dataDir, "offline-tmp")
    if err := os.MkdirAll(tmpDir, 0o755); err != nil { return err }
    tmp, err := os.CreateTemp(tmpDir, "hls-*.mp4")
    if err != nil { return err }
    tmpPath := tmp.Name()
    tmp.Close()
    defer os.Remove(tmpPath)

    args := []string{
        "-hide_banner", "-loglevel", "error",
        "-reconnect", "1", "-reconnect_streamed", "1",
        "-reconnect_delay_max", "30", "-reconnect_on_http_error", "429,5xx",
        "-protocol_whitelist", "crypto,data,http,https,tcp,tls", // 不含 file，防恶意清单读本地文件
        "-i", srcURL,
        "-c", "copy", "-bsf:a", "aac_adtstoasc",
        "-progress", "pipe:1", "-nostats",
        "-y", tmpPath,
    }
    cmd := exec.CommandContext(ctx, ffmpeg, args...) // ctx 取消即杀 ffmpeg
    stdout, err := cmd.StdoutPipe()
    if err != nil { return err }
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Start(); err != nil { return err }

    // 解析 -progress 的 total_size= 增量上报（未知总量，跟现有"源未报大小"一致，进度只涨字节）
    sc := bufio.NewScanner(stdout)
    var last int64
    for sc.Scan() {
        if v, ok := strings.CutPrefix(sc.Text(), "total_size="); ok {
            if n, e := strconv.ParseInt(strings.TrimSpace(v), 10, 64); e == nil && n > last {
                t.Add(n - last); last = n
            }
        }
    }
    if err := cmd.Wait(); err != nil {
        return fmt.Errorf("ffmpeg 拉取 HLS 失败: %v: %s", err, strings.TrimSpace(stderr.String()))
    }

    // 落地：临时 mp4 → fs.Put（云盘驱动也走同一路径）
    f, err := os.Open(tmpPath)
    if err != nil { return err }
    defer f.Close()
    fi, err := f.Stat()
    if err != nil { return err }
    t.SetTotal(fi.Size()) // 收尾把总量补齐 → 进度条到 100%
    if err := s.fs.Put(ctx, u, dstDir, name, f, fi.Size(), false); err != nil { return err }

    target := util.JoinLogical(dstDir, name)
    if gi, err := s.fs.Get(ctx, u, target); err == nil {
        s.index.Upsert(target, gi)
    }
    return nil
}
```

**A6. 新增 imports**：`bufio`、`bytes`、`os`、`os/exec`、`path/filepath`、`strconv`、
`newlist/internal/media`。（`path`/`net/url`/`strings`/`fmt` 已在。）

### 前端 B

**B1. `frontend/src/utils/api.js:25`** 传 name：
```js
offline: (urls, dst_dir, name) => http.post('/fs/offline', { urls, dst_dir, name }),
```

**B2. `frontend/src/pages/Files.vue`** 离线对话框加一个可选文件名输入：
```vue
<el-input v-model="offlineUrls" type="textarea" :rows="5" placeholder="每行一个 http/https 链接（支持 m3u8）" />
<el-input v-model="offlineName" class="offline-name" placeholder="文件名（可选，仅单个链接时生效）" clearable />
<div class="dim offline-dst">将下载到：{{ current || '/' }}</div>
```
声明 `const offlineName = ref('')`；`submitOffline()` 调用改为
`api.fs.offline(urls, current.value || '/', offlineName.value.trim())`，成功后
`offlineName.value = ''` 一并清空。

---

## 效果

- 粘贴 `.../1080p/video.m3u8` → 任务抽屉出现离线任务，进度随下载字节增长 → 完成后
  目标目录出现单个可播放 mp4（H.264 + AAC，有声音），能直接用网页播放器播放。
- 填了"文件名 abc" → 落地 `abc.mp4`；不填 → URL 末段名或 `video.mp4`。
- 普通直链（.zip/.jpg 等）下载与旧行为一致；填了文件名则用该名。
- 服务器没装 ffmpeg → HLS 任务失败并提示"未安装 ffmpeg"（`media.ErrNoFFmpeg`），
  普通下载不受影响。

---

## 验收标准

**功能**
- [ ] 粘贴真实 HLS m3u8 → 任务完成，目标目录得到可播放 mp4，时长/画面正常、**有声音**（验证 `aac_adtstoasc` 生效）。
- [ ] 填"文件名 abc" → 产物 `abc.mp4`；不填 → URL 末段名或 `video.mp4`。
- [ ] 普通 http 直链（如 .zip/.jpg）下载行为不变，文件名与旧逻辑一致；填 name 时用 name。
- [ ] 多链接 + 填 name：name 被忽略，各按各自 URL 命名，不互相覆盖。

**健壮性**
- [ ] 下载中途取消任务 → ffmpeg 进程被杀、临时文件清理、任务标记失败/取消。
- [ ] 源站中途 429/5xx → reconnect 自动续传，不产出截断文件。
- [ ] 无 ffmpeg → HLS 任务明确报错，普通下载正常。
- [ ] 畸形 m3u8 含 `file://` 引用 → 因 protocol_whitelist 不含 file 被拒。

**存储兼容**
- [ ] 本地存储驱动落地正常。
- [ ] 任一云盘驱动（onedrive/pikpak/telegram）落地正常（临时文件 → `fs.Put` 通用路径）。

**回归 / 构建**
- [ ] `go build ./...` 通过。
- [ ] `go test ./internal/server/ ./internal/media/` 通过（`offline_test.go` 等不回归）。
- [ ] 前端 `npm run build`（在 `frontend/`）通过。

### 端到端验证步骤
1. 后端：`go build ./...` && 启动（`NL_DATA_DIR` 默认 `./data`），确保 PATH 有 ffmpeg。
2. 前端：`cd frontend && npm run build`（或 dev），打开"离线下载"。
3. 粘贴一个真实 1080p `video.m3u8`，填文件名 `test`，开始 → 任务抽屉看进度 →
   完成后目录出现 `test.mp4`，播放器打开验证画面+声音。
4. 再测：普通直链、多链接+name、取消任务、无 ffmpeg（临时改 PATH）各一遍对照验收项。

---

## 非目标（本期不做）

- 防盗链 Referer / 自定义请求头 UI（如遇需要 Referer 的站点再加；可预留 `referer` 字段不接 UI）。
- Go 侧逐分片解析下载 / SSRF 深度隔离（自用定位，ffmpeg 直拉 + protocol_whitelist 足够）。
- master 多清晰度选择（交给 ffmpeg 默认挑选）。
- 云盘上传阶段的细粒度进度（以下载阶段进度为主；本地驱动上传瞬时可忽略）。

## 风险与注意

- 临时文件占数据盘：大小≈成片，完成即删——数据盘需有足够空间。
- `fs.Put` 用 `overwrite=false`：**实现时确认重名行为**（报错 or 自动改名），与普通离线下载保持一致即可。
- `-progress` 的 `total_size` 在纯 remux 下≈成片大小，作为进度近似足够；无 duration 依赖。
