# NewList 任务分解与详细规格

> 总体架构与里程碑见 ../PLAN.md。本文件是每个任务的可执行细则（写给任何执行者，包括低阶模型：
> 明确文件路径、接口签名、SQL、验证命令与常见坑）。
> 当前状态：M0 完成；Demo（含 M1/M2 + 界面全套）进行中；云盘/转存/加速/转码在 Demo 验收后展开。

## 执行顺序（Demo 优先策略）

```
M0 环境 ✅ → Demo 本地预览版（进行中）→ 用户验收/改界面 →
M3 OneDrive 双驱动 → M4 PikPak → M5 任务+跨存储转存 → M6 多线程加速 →
M10 转码播放 → 收尾完善（索引/媒体库按反馈迭代）→ M12 单二进制+Docker
```

- 环境事实：Go 1.26.4、Node 24.15、npm 11.12、Docker 29.4.3 已装；ffmpeg 8.1.2 经 winget 装于
  `%LOCALAPPDATA%\Microsoft\WinGet\Packages\Gyan.FFmpeg_*\ffmpeg-*-full_build\bin\ffmpeg.exe`
  （当前会话 PATH 未刷新，代码需自动探测：env `NL_FFMPEG` → `exec.LookPath("ffmpeg")` → 上述 winget 通配路径）。

---

## Demo 本地预览版（当前最优先）

【目标】用户可在浏览器预览的本地 Demo：本地磁盘存储 + 完整界面（液态玻璃主题、登录、
Apple TV 媒体库、文件管理、图片灯箱、视频直连播放、缩略图、搜索）。用户看效果提修改意见，
确认后再做云盘/转存/加速/转码/Docker。

【访问方式】构建前端到 public/dist → `go run .` → http://localhost:5244，
管理员 admin/admin123（启动时以 `NL_ADMIN_PASSWORD=admin123` 指定；admin123 仅本地演示密码，
生产/公网部署务必换强密码）。

### Demo·后端（= 下方 M1 + M2 全部内容，另加以下四块）

1. `internal/thumb/thumb.go` 缩略图服务：GET `/api/thumb/*path?size=`（size 默认 400，为像素宽）。
   - 逻辑：fs.Resolve → 驱动实现 Thumber → 302 到云端缩略图 URL；否则要求驱动实现 LocalPather 拿绝对路径：
     - 图片(jpg/jpeg/png/gif/webp/bmp)：disintegration/imaging 打开→`imaging.Resize(size,0,Lanczos)`→存 jpeg q80；
     - 视频(model.ExtType==video)：ffmpeg `-ss 3 -i <绝对路径> -frames:v 1 -vf scale=<size>:-2 -q:v 5 -y <cache>.jpg`，
       失败重试一次 `-ss 0`。
   - 缓存键 = sha1(逻辑路径|mtime|size|宽)，存 `NL_DATA_DIR/thumbs/`；命中直接 ServeFile，
     响应头 `Cache-Control: public, max-age=86400`。
   - 并发信号量 2 + 同 key singleflight（map[string]chan struct{} 即可）。
   - ffmpeg 路径探测（见上环境事实）；探测不到 → 视频缩略图 404，图片照常，进程不崩。
2. `internal/index/index.go` 索引构建器（files 表）：
   - `Rebuild(ctx)`：清表 → 对每个 enabled 存储从挂载点 BFS（用 fs.List + 管理员身份 `&user.User{Role:"admin",BasePath:"/"}`），
     批量 500 行/事务 `INSERT OR REPLACE`：path(挂载全路径)/parent/name/name_lower/is_dir/size/
     modified(RFC3339)/ext_type(model.ExtType)。
   - 进度：struct{Running bool; Scanned int64; Current string; Err string}；
     GET `/api/admin/index/progress` 返回它；POST `/api/admin/index/rebuild` 触发（已在跑→409）。
   - 启动时若 files 表为空且有存储 → 自动后台构建。
   - 写操作同步钩子（handler 成功后调用）：Put/Mkdir→UpsertRow；Remove→DeleteByPathPrefix；
     Rename/Move→RenamePrefix(old,new)。**前缀 SQL 不要用 LIKE**（路径可含 % _），
     用 `WHERE path=? OR substr(path,1,length(?)+1)=?||'/'`。
3. `handler_search.go`：GET `/api/fs/search?q=&type=&limit=`（limit 默认 100）：
   `WHERE name_lower LIKE '%'||?||'%' ESCAPE '\'`（先转义 q 中的 %/_/\），
   可选 `AND ext_type=?`；base_path 过滤 `AND (?='/' OR path=? OR substr(path,1,length(?)+1)=?||'/')`；
   `ORDER BY is_dir DESC, name LIMIT ?`。
4. `handler_media.go`：
   - GET `/api/media/list?kind=video|image&sort=modified|name&order=asc|desc&limit=&offset=&parent=`
     → {items:[{path,name,size,modified}]}（is_dir=0 AND ext_type=kind + base_path 过滤 + 可选 parent 前缀过滤）。
   - GET `/api/media/groups?kind=` → 按 parent 分组：{dir, name=最后一段, count, cover=组内 modified 最新文件的 path,
     latest} ORDER BY latest DESC LIMIT 40。
5. `handler_video.go`（demo 桩，接口形状与最终一致）：GET `/api/video/info?path=` →
   ext∈{mp4,webm,mov,m4v,mkv} 返回 `{"strategy":"direct"}`，否则
   `{"strategy":"unsupported","message":"该格式的转码播放将在下一阶段提供"}`。

### Demo·前端（frontend/，Vite+Vue3+JS，禁 TypeScript）

- package.json deps：vue vue-router pinia axios element-plus @element-plus/icons-vue artplayer photoswipe
  marked dompurify highlight.js github-markdown-css；devDeps：vite @vitejs/plugin-vue unplugin-auto-import
  unplugin-vue-components。
- vite.config.js：plugin-vue + AutoImport/Components(ElementPlusResolver)；
  `build.outDir='../public/dist'`，`emptyOutDir=true`；`server.proxy['/api']='http://127.0.0.1:5244'`。
- `src/assets/glass.css` 液态玻璃设计令牌：
  - :root：`--glass-bg: rgba(255,255,255,.07); --glass-border: rgba(255,255,255,.14); --glass-blur: 28px;
    --radius-card: 16px; --radius-panel: 24px`；
  - body 深底 `#0a0a12` + 固定极光层（3-4 个 position:fixed 大 radial-gradient 圆斑，
    filter:blur(120px)，低饱和 蓝/紫/青/粉，z-index:-1，30s 缓慢漂移动画）；
  - `.glass` = 半透明底 + `backdrop-filter: blur(var(--glass-blur)) saturate(1.7)` + 1px 内描边 +
    ::before 顶部镜面高光 + 投影；`.glass-hover`：hover `transform:scale(1.05)` + 辉光；
  - `@supports not (backdrop-filter)` 降级 `rgba(20,20,30,.92)`；
  - html.dark + 覆写 `--el-bg-color` 系列让 Element Plus 融入暗色玻璃。
- `src/utils/file.js`：extType(name)→video/image/audio/pdf/markdown/text/other；typeIcon；formatSize；formatTime。
  `src/utils/path.js`：encodePath(逐段 encodeURIComponent)、join、parent、segments。
  `rawUrl(path)=/api/raw+encodePath+?token=`、`thumbUrl(path,size)` 同理（img/video 标签带不了 Header）。
- `src/api/http.js`：axios baseURL '/api'；请求拦截注入 Bearer；响应拦截 code!==200 reject，401→清 token→/login。
- stores：auth(token localStorage 'nl_token'、user、isAdmin、canWrite、login/logout/fetchMe)；
  app(siteTitle、viewMode、sort)。
- 路由（全懒加载；除 /login 外 meta.requiresAuth，守卫：无 token→/login，有 token 无 user→fetchMe）：
  `/login`、`/` MediaHome、`/library/video`、`/library/photos`、`/play/:path(.*)*`、
  `/files/:path(.*)*`、`/search`、`/@admin`。
- 页面：
  - Login：居中玻璃卡片；
  - MediaHome：Hero 轮播(最近5个视频 thumb 大图+渐变遮罩+播放按钮) +「最近视频」横向架 +
    「视频合集」架(groups) +「最近图片」架 +「相册」架(groups)；空态引导；
  - LibraryVideo：?dir= 海报网格 auto-fill minmax(200px,1fr)，16:9 封面；
  - LibraryPhotos：?dir= 方格图片墙 + PhotoSwipe 灯箱（open 前 new Image 预取真实尺寸）；
  - Play：video/info→direct 用 ArtPlayer(url=rawUrl)，unsupported 显示玻璃提示卡+下载按钮；卸载 destroy；
  - Files：面包屑+工具栏(排序/视图/刷新/上传/新建/批量 删除·移动·复制)+列表(el-table)或网格(thumb 卡)；
    点击分发：目录进入、图片灯箱、视频→/play、md 抽屉渲染(marked+DOMPurify+github-markdown-css)、
    文本/代码抽屉(highlight.js)、pdf 新窗口、其他下载(&dl=1)；拖拽上传→UploadDrawer(2 并发+进度+重试)；
    MoveCopyDialog(el-tree lazy 只列目录)；NameDialog(校验与后端一致的非法字符/保留名)；删除 confirm；
  - Search：输入+类型 segmented(全部/视频/图片)+结果列表(点击按类型分发)；
  - Admin：el-tabs：站点设置(site_title)/存储管理(表格+动态表单：按 /api/admin/drivers 的 FieldSpec 渲染，
    secret 字段编辑时显示 *** 留空不改)/用户管理(CRUD+重置密码+启停；前端也拦最后一个 admin)/
    索引管理(进度轮询+重建按钮)。
- index.html：`<title>NewList</title>`，body 背景先写死 #0a0a12 防白闪。

### Demo·样例数据（ffmpeg 生成到 ./files）

电影/（3 个 testsrc2 不同滤镜 mp4，10-30s，中文名）、剧集/第一季/（2 个 mp4）、
图片/风景/（5 张 1920x1080 渐变 png/jpg）、图片/壁纸/（3 张）、文档/说明.md、
文档/示例代码.py、音乐/测试音频.mp3(sine)。

### Demo·验证清单

1. `go build ./...` 零错误；按 M1/M2 的 curl 清单全过。
2. `cd frontend && npm install && npm run build` 产物落 public/dist。
3. `NL_ADMIN_PASSWORD=admin123 go run .`（admin123 仅本地演示密码）→ 浏览器 http://localhost:5244：
   未登录任意路由跳 /login；首页 Hero+视频架+图片架有真实缩略图；视频能播能拖动；
   图片灯箱可切换；/files 上传/重命名/移动/删除全流程；搜索"电影"命中；
   /@admin 四个 tab 可操作；深层路由 F5 不 404。
4. 汇报用户：地址+账号+功能清单+已知限制（云盘/转存/加速/转码未含）。

---

## M1 后端骨架+DB+多用户认证（已并入 Demo，规格如下）

【目标】可运行的 Go 后端骨架：SQLite 建库、首启初始化管理员、JWT 登录、用户 CRUD、
三级权限中间件、占位前端。工作目录 E:\桌面\newlist，模块名 newlist。

1. `go mod init newlist`；`go get github.com/gin-gonic/gin github.com/golang-jwt/jwt/v5
   golang.org/x/crypto/bcrypt modernc.org/sqlite github.com/google/uuid
   github.com/disintegration/imaging golang.org/x/image`
2. `public/public.go`：`package public` + `//go:embed all:dist` 导出 `var Dist embed.FS`；
   `public/dist/index.html` 占位页（保证 go:embed 不因空目录报错）。
3. `internal/db/db.go`：`Open(dataDir)`——DSN
   `file:<dir>/newlist.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)`
   （driver 名 "sqlite"，`import _ "modernc.org/sqlite"`）；`SetMaxOpenConns(4)`；建表：
   - `settings(key TEXT PRIMARY KEY, value TEXT NOT NULL)`
   - `users(id INTEGER PK AUTOINCREMENT, username TEXT UNIQUE NOT NULL, password_hash TEXT NOT NULL,
     role TEXT DEFAULT 'user', base_path TEXT DEFAULT '/', can_write INTEGER DEFAULT 0,
     enabled INTEGER DEFAULT 1, created_at TEXT)`
   - `storages(id INTEGER PK AUTOINCREMENT, mount_path TEXT UNIQUE, driver TEXT, config TEXT DEFAULT '{}',
     ord INTEGER DEFAULT 0, enabled INTEGER DEFAULT 1, status TEXT DEFAULT '', created_at TEXT)`
   - `files(path TEXT PRIMARY KEY, parent TEXT, name TEXT, name_lower TEXT, is_dir INTEGER,
     size INTEGER DEFAULT 0, modified TEXT DEFAULT '', ext_type TEXT DEFAULT 'other')`
     + idx_files_parent(parent)、idx_files_name_lower(name_lower)、idx_files_ext_type(ext_type,is_dir,modified)
4. `internal/conf/conf.go`：settings 带缓存读写；键 site_title/jwt_secret；`JWTSecret()` 首调生成随机
   32 字节 base64 持久化；`Version="0.1.0-demo"`。
5. `internal/auth/auth.go`：bcrypt cost10 Hash/Verify；`SignToken(userID,secret)` HS256
   claims={sub:strconv(id),iat,exp=+48h}；`ParseToken` **jwt.WithValidMethods(["HS256"])** 防算法混淆；
   `RandomPassword(12)` crypto/rand。
6. `internal/auth/limiter.go`：全局计数：连续失败≥5 → 锁 5 分钟（Allow/Fail/Success，sync.Mutex）。
7. `internal/user/user.go`：User{ID,Username,Role,BasePath,CanWrite,Enabled,CreatedAt}(json 小写下划线，
   PasswordHash json:"-")；CRUD：Create/GetByID/GetByUsername/List/Update/UpdatePassword/Delete；
   Update/Delete 拦**最后一个 enabled admin**；base_path 存前 `path.Clean("/"+p)`。
8. `internal/server/`：
   - resp.go：OK(c,data)→200{"code":200,"message":"success","data":..}；Fail(c,status,msg)。
   - middleware.go：Authed（`Authorization: Bearer` 或 `?token=` → ParseToken → 查用户须 enabled →
     c.Set("user",u)；失败 401）；CanWrite（admin 或 can_write，否则 403）；AdminOnly（role==admin）。
   - handler_auth.go：POST /api/auth/login {username,password}→429/401/200{token,expires_at,user}；
     GET /api/auth/me。
   - handler_user.go：GET/POST /api/admin/users、PUT/DELETE /api/admin/users/:id、
     PUT /api/user/password{old_password,new_password}（先验旧密码）。
   - router.go：gin release + Recovery；分组：公开(ping/login/public settings)、Authed、CanWrite、AdminOnly。
   - static.go：embed 的 dist 子树；/assets/* `Cache-Control: public,max-age=31536000,immutable`；
     NoRoute：/api 前缀→404 JSON，否则 index.html（no-cache）——SPA 深链刷新关键。
9. `main.go`：env NL_PORT(5244)/NL_DATA_DIR(./data)/NL_FILES_DIR(./files)/NL_ADMIN_USER/NL_ADMIN_PASSWORD；
   MkdirAll；db.Open；首启 users 空→建 admin（密码取 env 否则随机并**显眼打印一次**）；
   http.Server{ReadHeaderTimeout:10s, IdleTimeout:60s，**不设 Read/WriteTimeout**（大文件长连接）}；
   优雅关闭（signal.NotifyContext+Shutdown）。

【验证】go run 后：ping→pong；日志有密码；登录 200/401/连错5次→429；me 带 token 200 不带 401；
建子用户能登录、其访问 /api/admin/* → 403；data/newlist.db 的 password_hash 是 $2a$ 开头。

---

## M2 驱动框架+本地驱动+文件读写 API（已并入 Demo，规格如下）

1. `internal/model/file.go`：FileInfo{Name,Size,IsDir,Modified time.Time}(json RFC3339 UTC)；
   Ext(name)、ExtType(name)→video/image/other（视频集 mp4,mkv,avi,mov,wmv,flv,webm,m4v,ts,m2ts,rmvb,rm,
   mpg,mpeg,vob,3gp；图片集 jpg,jpeg,png,gif,webp,bmp,avif,heic）。
2. `internal/driver/driver.go`：Config=map[string]string；Link{URL,Header,Local io.ReadSeekCloser,Size,Mod}；
   Driver 接口 Init/Drop/List/Stat/Link；可选 Writer{MakeDir,Rename,Remove,Move,Copy}、
   Uploader{Put(dstDir,name,r,size)}、Thumber{Thumb→URL}、LocalPather{AbsPath}；
   哨兵错误 ErrNotFound/ErrExist/ErrNotSupported/ErrBadName（handler 映射 404/409/501/400）。
3. `internal/driver/registry.go`：FieldSpec{Name,Label,Type(string|password|number|bool|select),Required,
   Default,Options,Secret,Help}；Meta{Name,Label,Remote,Fields}；Register/Get/Metas/MetaOf；
   Remote=true 的驱动自动追加通用字段 proxy/threads/chunk_mb。
4. `internal/driver/local/local.go`：注册 "local"（字段 root_path 必填）。Init：filepath.Abs→MkdirAll→
   **os.OpenRoot** 持有 *os.Root；全部操作走 root.*；rel=""→"."；
   - checkName：拒 `<>:"/\|?*`、码点<0x20、结尾点/空格、NTFS 保留名(CON/PRN/AUX/NUL/COM1-9/LPT1-9 含带扩展名)、
     "."/".."；MakeDir 校验整条 rel 每段；Rename/Put 校验新名。
   - List=Open+ReadDir(-1)（entry.Info() 失败跳过）；Stat；Link=Open 文件句柄+Size/Mod（目录→ErrNotFound）；
   - MakeDir=Stat 查重→MkdirAll；Rename=同目录 join 新名，目标存在→ErrExist，root.Rename；
   - Remove：拒绝 rel==""（根）；Stat→RemoveAll；
   - Move=root.Rename(src, dstDir/base)；自身内部→报错；目标存在→ErrExist；
   - Copy=递归（dir：MkdirAll+子项递归；file：Open→OpenFile(O_TRUNC)→io.Copy，失败清理半截）；
   - Put=**临时文件 `.{name}.uploading-<hex4>` O_EXCL 写→成功 root.Rename 覆盖目标**，失败删临时；
   - AbsPath=filepath.Join(rootAbs, FromSlash(rel)) 后强校验仍在 rootAbs 前缀内；
   - 错误映射：fs.ErrNotExist→ErrNotFound；"escapes from parent"→ErrNotFound（不暴露细节）。
5. `internal/fs/fs.go`（虚拟挂载树）：
   - NormPath：含 `\` 或 NUL→ErrBadPath；path.Clean("/"+p)。
   - accessOK(u,p)=base=="/"||p==base||HasPrefix(p,base+"/")；
     navOK=accessOK||p=="/"||HasPrefix(base,p+"/")（允许"路过"通往视野的祖先）。
   - Reload：读 storages(enabled 排序)→factory→Init(30s 超时)→失败记 status 不中断；
     mounts 按路径长度降序（最长前缀匹配）；旧实例 Drop。
   - Resolve(u,p)：norm→accessOK 否则 ErrNotFound→findMount 最长前缀→(mount,rel)。
   - Get：挂载内→drv.Stat；否则 isVirtualDir（p 是某挂载的祖先或 "/"）→合成目录项。
   - List：挂载内容 + 本层挂载点虚拟目录合并（同名挂载优先），每项过 navOK 过滤；排序 目录先+名称。
   - Link：Resolve→Stat（目录拒）→drv.Link。
   - Caps(u,p)={write,upload}（AllowWrite && 驱动断言）。
   - 写操作：MakeDir/Rename/Remove/MoveCopy/Put——Resolve+能力断言；跨存储 MoveCopy 明确报
     "跨存储转存功能将在后续版本提供"；Put 先按 overwrite 查重（ErrExist）。
6. handlers：
   - handler_fs.go：GET fs/get、GET fs/list（返回 {items,write,upload}）；POST fs/mkdir{path}、
     fs/rename{path,name}、fs/remove{paths[]}、fs/move{paths[],dst_dir}、fs/copy 同（挂 CanWrite）；
     PUT fs/upload?path=&overwrite=（**原始字节流 body**，io.Copy 直落盘）。
     错误映射统一函数：ErrNotFound→404、ErrExist→409、ErrNotSupported→501、ErrBadName/ErrBadPath→400。
   - handler_raw.go：GET/HEAD /api/raw/*path：Local→`http.ServeContent`（自动 Range/206/If-Modified-Since）；
     URL→302；`?dl=1`→`Content-Disposition: attachment; filename*=UTF-8''<enc>`；
     Content-Type：mime.TypeByExtension+自定义表(.mkv→video/x-matroska、.flac→audio/flac、
     .md→text/plain;charset=utf-8、.ts→video/mp2t)，未知→application/octet-stream。
   - handler_storage.go（AdminOnly）：GET /api/admin/drivers=Metas()；GET /api/admin/storages
     （secret 字段值→"***"）；POST（mount_path 归一化查重）；PUT（"***" 值→保留旧值）；DELETE；
     POST :id/reload；增删改后 fs.Reload+触发重建索引。
7. main.go 首启：storages 空→自动插入 {mount_path:"/本地存储", driver:"local",
   config:{root_path:NL_FILES_DIR}}。

【验证】./files 造中文/特殊字符文件后：list "/"→本地存储；list 子目录中文正确；raw 下载+`-r 0-9`→206；
穿越集（/../、/本地存储/../../x、..%2F、含\、C:/）全 400/404；子用户(base=/onedrive) list "/" 空、
访问本地存储 404；mkdir/rename/upload/remove 全流程+无 token 401+can_write=false 403+
重名 409+保留名 CON→400。

---

## M3 OneDrive + OneDrive APP 双驱动（Demo 已验收，2026-07-06 动工）

【目标】`internal/driver/onedrive` 一个包注册两个驱动：`onedrive`（委托授权 refresh_token，
个人/商业账号）与 `onedrive_app`（应用授权 client_credentials，访问指定用户的 OneDrive）。
全能力：List/Stat/Link(302)/Thumb/MakeDir/Rename/Remove/Move/Copy/Put。Remote=true
（registry 自动追加 proxy/threads/chunk_mb，本里程碑不消费）。**不复制 AGPL 代码。**

1. **driver.go 加可选接口**（config 运行期变化的持久化通道，refresh_token 轮换用）：
   ```go
   // ConfigPersister 可选：驱动配置运行期会变（如 OAuth refresh_token 轮换），注入保存回调。
   type ConfigPersister interface{ SetPersist(func(cfg Config) error) }
   ```
   fs.Reload 在 `d.Init` **之前**：若断言成功则注入
   `func(cfg){ json.Marshal → UPDATE storages SET config=? WHERE id=m.ID }`（闭包捕获 m.ID）。
2. **endpoints**：region=global → login `https://login.microsoftonline.com`、
   graph `https://graph.microsoft.com/v1.0`；region=cn → `https://login.partner.microsoftonline.cn`、
   `https://microsoftgraph.chinacloudapi.cn/v1.0`。
3. **配置字段**
   - onedrive：region(select global|cn, default global)、client_id(必填)、client_secret(password,
     secret, 非必填——公共客户端可无)、refresh_token(password, secret, 必填, help"可直接粘贴 AList
     已有值")、root_folder_path(default /)
   - onedrive_app：region、tenant_id(必填)、client_id(必填)、client_secret(password, secret, 必填)、
     user_email(必填, help"应用访问该用户的 OneDrive")、root_folder_path(default /)
4. **graph.go — 客户端**：结构 {http *http.Client(共享、无全局 Timeout), loginBase, graphBase,
   driveBase, mode, 凭据字段, mu, accessToken, expiresAt, persist func}
   - driveBase：onedrive=`{graph}/me/drive`；onedrive_app=`{graph}/users/{email}/drive`
   - `token(ctx)`：expiresAt-5min 内直接用缓存；否则 POST 表单到
     onedrive=`{login}/common/oauth2/v2.0/token`(grant_type=refresh_token, client_id,
     client_secret 可空, refresh_token)；onedrive_app=`{login}/{tenant}/oauth2/v2.0/token`
     (grant_type=client_credentials, scope=`{graphHost}/.default`)。**失败重试 1 次**；
     响应含新 refresh_token 且≠旧值 → 更新内存 cfg 并调 persist（失败仅 log 不中断）。
   - `req(ctx, method, url, body, out)`：带 Bearer；**401 → 强制刷新 token 重试 1 次**；
     429 → 按 Retry-After（上限 5s）等待重试 1 次；错误体解析 `{"error":{"code","message"}}`：
     itemNotFound→ErrNotFound、nameAlreadyExists→ErrExist、invalidRequest(改名/建目录时)→ErrBadName，
     其余包装 "onedrive: <code>: <message>"。
   - `itemURL(rel, suffix)`：rel 先与 root_folder_path 拼接（path.Join，全程 POSIX）；
     rel=="" → `{driveBase}/root{suffix}`；否则 `{driveBase}/root:/{每段 url.PathEscape 后以 / 连接}:{suffix}`
     （suffix 形如 ""、"/children"、"/content"、"/createUploadSession"、"/thumbnails/0/large"、"/copy"）。
5. **onedrive.go — 驱动实现**（item JSON 字段：name,size,folder{},file{},lastModifiedDateTime,
   id,parentReference{driveId},@microsoft.graph.downloadUrl）
   - checkName：拒空、`"*:<>?/\|`、码点<0x20、首尾空格、结尾点 → ErrBadName
   - Init：解析 cfg（region 默认 global、root_folder_path Clean）→ 构造客户端 → **验证一次**
     token(ctx)+GET root（错误原样返回，让存储状态可见）
   - List：GET `{item}/children?$top=1000&$select=name,size,folder,file,lastModifiedDateTime`
     循环 @odata.nextLink 直到空；FileInfo{Name, Size, IsDir: folder!=nil, Modified: 解析 RFC3339}
   - Stat：GET `{item}?$select=同上`；根("")合成 {IsDir:true}避免多余请求？——不：root 也走 GET（Init 已验）。
   - Link：GET `{item}?$select=id,size,lastModifiedDateTime,content.downloadUrl`（响应字段名
     `@microsoft.graph.downloadUrl`，预签名 ~1h、免 Authorization、支持 Range）→ Link{URL}；
     目录或无 downloadUrl → ErrNotFound
   - Thumb：GET `{item}/thumbnails/0/large` → {url}；404/无 url → ErrNotFound
   - MakeDir：整条 rel 逐段（父可能不存在时 Graph 不自动建）？——Graph 不支持递归建；
     实现：对 rel 每一级从浅到深 POST `{parent}/children`
     body `{"name":seg,"folder":{},"@microsoft.graph.conflictBehavior":"fail"}`，
     nameAlreadyExists 视为该级已存在继续（但**最后一级**已存在 → ErrExist）
   - Rename：PATCH `{item}` body `{"name":new}`（先 checkName）
   - Remove：DELETE `{item}`；根 rel=="" 拒绝（ErrNotSupported）
   - Move：PATCH `{srcItem}` body `{"parentReference":{"path":"/drive/root:{/root_folder/dstDir}"},
     "name":basename}`；目标重名 nameAlreadyExists→ErrExist
   - Copy：GET dst 目录 item（$select=id,parentReference）拿 {driveId,id} →
     POST `{srcItem}/copy` body `{"parentReference":{"driveId","id"},"name":basename}` →
     202 + Location 监控 URL → 每 500ms GET（**裸 http，无 Bearer**）直到
     status=completed / failed（failed→error.message）；ctx 取消即停
   - Put(dstDirRel, name, r, size)：checkName；size<0 → 先落 os.CreateTemp 临时文件测长（用后删）；
     size < 4MiB → PUT `{dir:/name}:/content?@microsoft.graph.conflictBehavior=replace`
     （ContentLength=size 流式）；否则 POST `{dir:/name}:/createUploadSession`
     body `{"item":{"@microsoft.graph.conflictBehavior":"replace"}}` → uploadUrl →
     按 **chunkSize=10MiB**（结构字段，测试可改小；须为 320KiB 倍数）循环 PUT（裸 http，
     头 `Content-Range: bytes s-e/total`），**每块失败重试 2 次**（body 需可重读：块先读入
     10MiB 缓冲区 bytes.Reader——内存有界）；最后一块 200/201 即完成
   - Drop：返回 nil
6. **注册**：init() 两次 driver.Register；main.go 匿名 import `newlist/internal/driver/onedrive`。
7. **单测 onedrive_test.go**（同包，httptest.Server 同时充当 login+graph，驱动字段直接指到测试服务器）：
   token 刷新表单正确+轮换回写 persist 被调；401→刷新重试成功；list 两页翻页合并+目录/文件区分；
   stat itemNotFound→ErrNotFound；link 返回 downloadUrl；mkdir 最后一级已存在→ErrExist；
   小文件 PUT content 收到完整 body；大文件(chunkSize 调成 640KiB)分块序列 Content-Range 正确+
   中途一块 500 一次后重试成功；copy 202→轮询 completed。
8. **验证**（无真实账号时到 7 为止；真实账号联调由用户提供凭据后做）：
   go build/vet/test 全绿；起服务 GET /api/admin/drivers 含 onedrive/onedrive_app 完整 schema
   （secret 字段 secret=true；remote 通用字段已追加）；后台添加存储表单动态渲染两驱动字段正确；
   配错 token 的存储 status 显示错误且不影响其他挂载。
   【真实账号】浏览含中文/空格路径、下载 302+Range 拖动、上传 <4MB 与 >10MB、重命名/移动/复制/删除、
   缩略图 302、token 过期自动刷新（等 1h+ 再操作）、refresh_token 轮换后重启仍可用（回写生效）。

---

## M4 PikPak 驱动（2026-07-06 完成实现，mock 单测+冒烟通过，待真实账号联调；协议事实源自公开实现的接口行为，不复制 AGPL 代码）

【目标】`internal/driver/pikpak` 注册驱动 `pikpak`：账密登录 + refresh_token 维持 +
captcha 签名（md5 盐链）。能力：List/Stat/Link(302)/Thumb/MakeDir/Rename/Remove/Move/Copy；
**不实现 Uploader**（fs 层自动 can_upload=false、上传按钮隐藏、Put 被拒）。Remote=true。
**与 OneDrive 的关键差异：PikPak 是 ID 寻址**（parent_id/file id），驱动内需做
路径→ID 解析（逐级 List 匹配名字）+ 带 TTL 的解析缓存。

1. **协议常量（consts.go，集中常量便于失效时更新；2026-07 自 OpenList main 分支核对）**
   - 主机：auth/captcha=`https://user.mypikpak.net`、drive=`https://api-drive.mypikpak.net`
     （注意：已从 .com 迁到 .net；结构体字段 authBase/driveBase 可被单测覆盖指向 httptest）
   - 三平台常量表（platform → client_id/client_secret/client_version/package_name/盐表/UA）：
     - android：`YNxT9w7GMdWvEOKa` / `dbw2OtmVEeuUvIptb1Coyg` / `1.53.2` / `com.pikcloud.pikpak`
       盐表 8 条：`SOP04dGzk0TNO7t7t9ekDbAmx+eq0OI1ovEx`、`nVBjhYiND4hZ2NCGyV5beamIr7k6ifAsAbl`、
       `Ddjpt5B/Cit6EDq2a6cXgxY9lkEIOw4yC1GDF28KrA`、`VVCogcmSNIVvgV6U+AochorydiSymi68YVNGiz`、
       `u5ujk5sM62gpJOsB/1Gu/zsfgfZO`、`dXYIiBOAHZgzSruaQ2Nhrqc2im`、
       `z5jUTBSIpBN9g4qSJGlidNAutX6`、`KJE2oveZ34du/g1tiimm`
     - web（默认）：`YUMx5nI8ZU8Ap8pm` / `dbw2OtmVEeuUvIptb1Coyg` / `2.0.0` / `mypikpak.com`
       盐表 15 条：`C9qPpZLN8ucRTaTiUMWYS9cQvWOE`、`+r6CQVxjzJV6LCV`、`F`、`pFJRC`、
       `9WXYIDGrwTCz2OiVlgZa90qpECPD6olt`、`/750aCr4lm/Sly/c`、`RB+DT/gZCrbV`、``（空串）、
       `CyLsf7hdkIRxRm215hl`、`7xHvLi2tOYP0Y92b`、`ZGTXXxu8E/MIWaEDB+Sm/`、`1UI3`、
       `E7fP5Pfijd+7K+t6Tg/NhuLq0eEUVChpJSkrKxpO`、`ihtqpG6FMt65+Xk+tWUH2`、`NhXXU9rg4XXdzo7u5o`
     - pc：`YvtoWO6GNHiuCl7x` / `1NIH5R1IEe2pAxZE3hv3uA` / `undefined` / `mypikpak.com`
       盐表 10 条：`KHBJ07an7ROXDoK7Db`、`G6n399rSWkl7WcQmw5rpQInurc1DkLmLJqE`、
       `JZD1A3M4x+jBFN62hkr7VDhkkZxb9g3rWqRZqFAAb`、`fQnw/AmSlbbI91Ik15gpddGgyU7U`、
       `/Dv9JdPYSj3sHiWjouR95NTQff`、`yGx2zuTjbWENZqecNI+edrQgqmZKP`、`ljrbSzdHLwbqcRn`、
       `lSHAsqCkGDGxQqqwrVu`、`TsWXI81fD1`、`vk7hBjawK/rOSrSWajtbMk95nfgf3`
   - UA：web=Chrome 桌面 UA；pc=`MainWindow Mozilla/5.0 … PikPak/2.6.11.4955 … Electron/18.3.15 …`；
     android 可用官方拼接格式（`ANDROID-com.pikcloud.pikpak/1.53.2 protocolVersion/200 …`），
     v1 简化：android 也用固定串（devicesign 拼接非必须——服务端只校验 captcha_sign）
2. **captcha 签名（sign.go）**：
   `captchaSign(clientID, clientVersion, packageName, deviceID string, tsMillis int64, salts []string)`：
   `str = clientID + clientVersion + packageName + deviceID + fmt.Sprint(tsMillis)`；
   逐条 salt：`str = hex(md5(str + salt))`；返回 `"1." + str`。时间源做成结构体字段
   `now func() time.Time`（单测注入固定值出黄金向量）。
3. **配置字段（Fields）**：platform(select android|web|pc, default web)、
   username(必填, help"邮箱/手机号/用户名")、password(password, secret, 必填)、
   refresh_token(password, secret, 非必填, help"留空则用账密登录自动获取；失效自动重登")、
   device_id(非必填, help"留空自动生成并保存")、root_folder_id(默认空=根, help"填 PikPak 文件夹 ID
   可挂载子目录")。Remote=true（registry 自动追加 proxy/threads/chunk_mb）。
4. **client（client.go）**：结构 {http(共享无 Timeout), authBase, driveBase, platform 常量组,
   username, password, deviceID, mu, accessToken, refreshToken, expiresAt, userID, captchaToken,
   persist, cfg, now}
   - `refreshAuth(ctx)`（持锁）：POST `{auth}/v1/auth/token?client_id={cid}`
     JSON `{client_id, client_secret, grant_type:"refresh_token", refresh_token}`；
     响应 `{access_token, refresh_token, sub, expires_in}` → 缓存+expiresAt(提前5min)+userID；
     refresh_token 变化 → cfg 回写 persist（失败仅 log）。**error_code==4126（refresh_token 失效）
     → 自动转 login()**；其他非零 error_code → 错误含 error_description。
   - `login(ctx)`：先 captchaInit(action=`POST:/v1/auth/signin`, meta=按 username 形态选
     {email:…}|{phone_number:…}|{username:…}——含@=email、11≤len≤18 纯数字=phone)
     → POST `{auth}/v1/auth/signin?client_id={cid}`
     JSON `{captcha_token, client_id, client_secret, username, password}` →
     同样缓存三值+回写 refresh_token
   - `captchaInit(ctx, action, meta)`：POST `{auth}/v1/shield/captcha/init?client_id={cid}`
     JSON `{action, captcha_token:旧值, client_id, device_id, meta, redirect_uri:
     "xlaccsdk01://xbase.cloud/callback?state=harbor"}`；登录后场景 meta =
     `{client_version, package_name, user_id, timestamp, captcha_sign}`（sign 见 2）；
     响应 `{captcha_token, expires_in, url}`：url≠"" → 错误"PikPak 要求人机验证，请在官方客户端
     登录一次后重试"；否则缓存 captchaToken
   - `token(ctx)`：expiresAt-5min 内用缓存，否则 refreshAuth
   - `req(ctx, method, url, query, body, out)`：头 `Authorization: Bearer`、`X-Device-ID`、
     `X-Captcha-Token`、`User-Agent`；PikPak 业务错误在 JSON body `{error_code, error,
     error_description}`（HTTP 可能 200 也可能 4xx）：error_code==0 → 解析 out；
     **4122/4121/16（access_token 过期）→ forceRefresh+重试 1 次**；
     **9（captcha_token 过期）→ captchaInit(登录后 meta, action=`{method}:{url去host路径}`)+重试 1 次**；
     10 → 错误"pikpak: 操作频繁，请稍后再试"；`file_not_found` 类 error →
     driver.ErrNotFound；其余 → `pikpak: <error_code> <error_description>`
5. **路径解析（ID 寻址适配，pikpak.go）**：
   - `lookup(ctx, rel)` → `pkFile{id, name, size(字符串转int64), isDir(kind=="drive#folder"),
     modified(RFC3339), thumb}`：rel=="" → 合成 {id: root_folder_id, isDir:true}；
     否则取 parent=lookup(dir(rel)) → listAll(parent.id) 内找 base 名 → 无 → driver.ErrNotFound
   - 缓存：`map[rel]cacheEntry{f, at}`，TTL 2min（结构字段可调），mu 保护；
     **任何写操作成功后整表清空**（简单正确优先）
   - `listAll(ctx, id)`：GET `{drive}/drive/v1/files` query：parent_id、limit=100、
     thumbnail_size=SIZE_LARGE、with_audit=true、
     filters=`{"phase":{"eq":"PHASE_TYPE_COMPLETE"},"trashed":{"eq":false}}`、page_token 循环
     `next_page_token` 直到空；响应 `{files:[{id,kind,name,size,modified_time,thumbnail_link,
     web_content_link,medias}], next_page_token}`
6. **驱动方法（pikpak.go）**
   - Init：解析 cfg（platform 默认 web→常量组；root_folder_id 默认""）；device_id 空 →
     `hex(md5(username+password))` 回写 persist；refresh_token 有 → refreshAuth（4126 转 login），
     无 → login；成功后 captchaInit(登录后 meta, action=`GET:/drive/v1/files`)；错误原样返回
     （存储 status 可见）
   - List：lookup(rel) 非目录→ErrNotFound；listAll(id) → []FileInfo（顺带把子项写入缓存）
   - Stat：lookup → FileInfo
   - Link：lookup；目录→ErrNotFound；GET `{drive}/drive/v1/files/{id}`
     query `_magic=2021&usage=FETCH&thumbnail_size=SIZE_LARGE` →
     取 `web_content_link`；为空则 `medias[0].link.url`；仍空→ErrNotFound。
     **直链 ~有效期短且绑定 UA**：Link.Header 带同款 User-Agent；不缓存直链
   - Thumb：lookup → thumbnail_link；空→ErrNotFound
   - MakeDir：逐级 lookup，缺失级 POST `{drive}/drive/v1/files`
     JSON `{kind:"drive#folder", parent_id, name}`（响应 file.id 继续下一级）；
     **最后一级已存在 → ErrExist**；checkName 先行
   - Rename：checkName；lookup(rel) → PATCH `{drive}/drive/v1/files/{id}` JSON `{name}`；清缓存
   - Remove：rel==""→ErrNotSupported；lookup → POST `{drive}/drive/v1/files:batchTrash`
     JSON `{ids:[id]}`（回收站语义，与 AList 一致）；清缓存
   - Move：lookup(src)；**预检查** lookup(dstDir+"/"+basename) 已存在→ErrExist；
     POST `{drive}/drive/v1/files:batchMove` JSON `{ids:[srcID], to:{parent_id:dstDirID}}`；清缓存
   - Copy：同 Move 但 `files:batchCopy`
   - checkName：拒空、含 `/` 或 `\`、码点<0x20、名字>250 字节 → ErrBadName
   - Drop：nil；SetPersist：存 persist
7. **注册**：init() driver.Register；main.go 追加匿名 import `newlist/internal/driver/pikpak`。
8. **单测 pikpak_test.go**（httptest.Server 同时充当 auth+drive 两主机；authBase/driveBase 指过去；
   now 注入固定时间）：
   1) captchaSign 黄金向量（固定输入+web 盐表 → 预期串，实现前先用独立 md5 链算出写死）；
   2) 账密登录链：captcha/init(action/meta.email/client_id query 正确) → signin(body 五字段) →
      persist 回写含 refresh_token+device_id；
   3) refreshAuth 轮换回写；4126 → 自动转 login 成功；
   4) drive 请求 error_code=16 → 刷 token 重试成功（Authorization 换新值）；
   5) error_code=9 → captcha 重刷（meta 含 user_id/timestamp/captcha_sign）重试成功；
   6) listAll 两页翻页合并 + kind/size(字符串)/modified_time 映射；
   7) lookup 中文多级路径解析 + 命中缓存（计数第二次无新 HTTP）+ 不存在→ErrNotFound；
   8) Link 取 web_content_link；wcl 空回退 medias[0].link.url；
   9) MakeDir 逐级创建 + 最后一级已存在→ErrExist；
   10) Rename PATCH body；写后缓存失效（改名后 lookup 旧名→ErrNotFound）；
   11) Move/Copy batchMove/batchCopy body 正确 + 目标重名预检→ErrExist；
   12) Remove batchTrash body；根→ErrNotSupported；
   13) drive 请求头齐全（Bearer/X-Device-ID/X-Captcha-Token/User-Agent）；
   14) 操作频繁 error_code=10 → 明确错误；captcha url≠"" → "人机验证"错误。
9. **验证**（无真实账号到 8 为止）：go build/vet/test 全绿；GET /api/admin/drivers 含 pikpak
   完整 schema（password/refresh_token secret=true；remote 通用字段追加）；后台添加存储选 pikpak
   动态表单正确；假凭据存储 status 记录 PikPak 真实错误且不影响其他挂载；挂载点 list 响应
   can_upload=false（前端上传按钮隐藏）。
   【真实账号】浏览含中文路径、302 直链播放（注意 UA 头）、缩略图、改名/移动/复制/删除、
   refresh_token 失效自动重登、上传被拒提示明确。

---

## M5 任务管理 + 跨存储转存（✅ 2026-07-06 完成；实测记录见 PROGRESS.md「M5」段）

【目标】服务器端把文件/目录从存储 A 转存到存储 B（如 PikPak→OneDrive）：后台任务带进度/
速度/可取消；move=copy 成功后删源。同存储 move/copy 仍走驱动原生（同步）。全链路流式、内存恒定。
**关键约束**：目标存储必须实现 Uploader（PikPak 无 → 作目标时明确报"不支持写入"，只能作源）。

1. **internal/task/task.go —— 任务管理器（内存态，v1 重启丢失）**
   - 状态：`State` = pending|running|done|error|canceled
   - `Task`（JSON 暴露给前端）字段：ID(string, uuid)、Name(string, 如"移动 a.mkv → /onedrive/x")、
     Owner(int64 用户 id)、State、Total(int64 字节)、Done(int64 字节)、Speed(int64 B/s)、
     CurFile(string 当前文件名)、Err(string)、CreatedAt(RFC3339)；
     内部：cancel(context.CancelFunc)、fn(func(ctx, *Task) error 供 retry 重跑)、
     mu、lastT(time.Time)、lastDone(int64)（算速度用）
   - **Progress 方法**（供 fs.Transfer 调用，结构化满足 fs.Progress 接口，无需包间 import）：
     `SetTotal(n int64)`、`SetFile(name string)`、`Add(n int64)`（Done+=n，按 lastT/lastDone
     每 ≥500ms 重算 Speed=Δbytes/Δt）；均加锁
   - `Manager`：mu、tasks(map[string]*Task)、queue(chan *Task, 带缓冲如 256)、workers int
   - `New(workers int) *Manager`：起 workers 个 goroutine 循环 `for t := range queue { run(t) }`
   - `run(t)`：置 running，`ctx,cancel := context.WithCancel(context.Background())`；存 cancel；
     执行 t.fn(ctx, t)；据返回：nil→done(Done=Total)、ctx.Err()==Canceled→canceled、
     其余→error 存 t.Err
   - `Submit(owner int64, name string, fn) *Task`：建 Task(pending)、存 map、入 queue、返回
   - `List(owner int64, isAdmin bool) []*Task`：admin 全量，否则仅 owner==自己；按 CreatedAt 倒序
   - `Get(id) (*Task, bool)`
   - `Cancel(id, owner, isAdmin) error`：找不到/无权→err；调 t.cancel()（running 时）或直接置 canceled
     （pending 时——worker 取到后 fn 首次 ctx 检查即退出）
   - `Retry(id, owner, isAdmin) error`：仅 error|canceled 可重试；重置 Done/Err/Speed=0、置 pending、
     重新入 queue（复用 t.fn）
   - `ClearDone(owner, isAdmin)`：删除 owner 名下 done|error|canceled 的任务
   - 线程安全：所有 map 与 Task 字段读写持锁；List 返回深拷贝快照避免并发读写竞态
2. **internal/fs/transfer.go —— 跨存储转存执行体**
   - `type Progress interface{ SetTotal(int64); SetFile(string); Add(int64) }`（task.Task 满足）
   - `func (f *FS) SameStorage(u, src, dstDir string) (same bool, dstUploadable bool, err error)`：
     Resolve 两端；same=(sm.ID==dm.ID)；dstUploadable=dm.drv 实现 Uploader；错误上抛
     （handler 用它决定同步/异步/拒绝）
   - `func (f *FS) Transfer(ctx, u *user.User, src, dstDir string, isMove bool, pr Progress) error`：
     - Resolve 源 sm/srcRel、目标 dm/dstRel（视野已由 handler 的 CanWrite+Resolve 保证）
     - dm.drv 断言 Writer(dw)+Uploader(up)，缺 → `errors.New("目标存储不支持写入")`
     - `sfi := sm.drv.Stat(srcRel)`；base=path.Base(src)
     - **规划**：files []fileJob{srcRel,dstDirRel,name,size}、dirs []string（目标待建目录，浅→深）
       - 文件：files={{srcRel, dstRel, base, sfi.Size}}
       - 目录：planDir 递归——把目标根 join(dstRel,base) 入 dirs，List(srcRelDir) 逐项：
         子目录递归、文件入 files（dstDirRel=当前目标目录 rel）
     - Total=Σfiles.size；pr.SetTotal(Total)
     - 逐个 dw.MakeDir(dir)，忽略 driver.ErrExist（其余错误上抛）
     - 逐文件 copyOne：pr.SetFile(name)；**文件级重试 2 次**（共 3 次尝试）：
       lk=sm.drv.Link(srcRel)；reader = lk.Local(本地句柄) 或 http GET lk.URL（带 lk.Header，
       如 PikPak 的 UA）的 resp.Body；包 countingReader(每次 Read 后 pr.Add(delta))；
       up.Put(ctx, dstDirRel, name, cr, size)；成功 break；失败 close+若 ctx 取消立即返回，
       否则 pr.Add(-本次已计字节) 回退进度再重试
     - 全部成功且 isMove → sm.drv 断言 Writer 调 Remove(srcRel)（删整棵源子树）
   - join(dir,name)：dir==""→name 否则 dir+"/"+name（rel 路径，全 POSIX）
   - countingReader：{r io.Reader; pr Progress; n int64}；Read 累加并 pr.Add
3. **internal/server/handler_task.go**
   - `taskDTO(t)`：拷 Task 公开字段
   - GET `/api/tasks` → `s.tasks.List(uid, isAdmin)` map 成 DTO 数组
   - POST `/api/tasks/:id/cancel`、`/api/tasks/:id/retry`：调管理器同名方法，404/403 映射
   - DELETE `/api/tasks/done`：ClearDone
4. **fsMoveCopy 分流改造（handler_fs.go）**：对每个 path：
   - `same, upOK, err := s.fs.SameStorage(u, p, dst)`；err→fsError
   - same → 原同步逻辑（MoveCopy + 索引 RenamePrefix/ScanSubtree）
   - 跨存储且 !upOK → 收集错误"目标存储不支持写入（如 PikPak 只能作转存源）"，本条跳过
   - 跨存储且 upOK → `s.tasks.Submit(uid, name, func(ctx, t){ err:=s.fs.Transfer(ctx,u,p,dst,isMove,t);
     if err==nil { isMove?index.RenamePrefix(p,target):index.ScanSubtree(target) }; return err })`
     收集 t.ID
   - 返回 `{task_ids: [...], errors: [...]}`（同步条目无 id）；前端据 task_ids 弹提示+开抽屉
   - 注意闭包捕获 u(*user.User 快照)、p、dst、target——循环变量要在闭包外取局部副本
5. **接线**：Server 加 `tasks *task.Manager` 字段；server.New 增参或 main.go 构造
   `task.New(2)` 注入；router：authed 组加 tasks 四路由（普通用户可访问，管理器内部按 owner 过滤）。
6. **前端（frontend/src）**
   - `api` 无需改；新增 `components/TasksDrawer.vue`：el-drawer(append-to-body)，
     onOpen 起 setInterval 每 1.5s GET /api/tasks；每项显示 name、el-progress(Done/Total*100)、
     速度(格式化 B/s→MB/s)、状态标签；running 显"取消"按钮、error/canceled 显"重试"、
     底部"清除已完成"；关抽屉清 interval
   - `pages/Files.vue`：顶栏加任务图标按钮(el-badge 显示进行中数量)→打开 TasksDrawer
   - `MoveCopyDialog.vue`：submit 后读返回 data.task_ids，非空→ElMessage"已创建 N 个转存任务"
     并 emit('tasks') 让 Files 打开抽屉；有 errors 也提示
7. **测试**
   - `internal/task/task_test.go`：Submit→done 进度到 Total；Cancel 使长任务 fn(轮询 ctx)→canceled；
     Retry error 任务重跑成功；List owner 过滤；ClearDone 只清自己的终态任务；并发 Submit 无竞态
     （-race）
   - `internal/fs/transfer_test.go`：**两个 local 挂载**（t.TempDir 各一，真实驱动）+ 假 User(admin)；
     - 单文件跨存储 copy：目标出现同名文件、内容一致、pr.Total/Done 相符、源仍在
     - 目录树 copy：子目录与文件全部复制、目录结构正确
     - move：成功后源被删
     - 目标不可上传：手动构造一个只读驱动或用 SameStorage 断言 upOK=false 分支（可用 mock 驱动）
     - 文件级重试：mock 源驱动首次 Link/读失败一次后成功（用一个包内 fake Uploader/Writer 驱动，
       或注入 http 失败——本地句柄不易注入失败，故用一个最小 fake driver 覆盖重试路径）
8. **验证**：go build/vet/test（含 -race）全绿；起服务，后台加**第二个本地存储**（挂 /本地存储2，
   指向另一目录）→ curl `/api/fs/copy {paths:[/本地存储/说明.md], dst_dir:/本地存储2}` 返回 task_ids →
   轮询 `/api/tasks` 看 running→done、Done==Total → 目标目录 list 出现该文件；
   move 后源 list 不再有；cancel 大文件任务立即停；对 PikPak（若已挂）作目标 → errors 提示不支持写。
   前端 e2e 补一张 TasksDrawer 截图。

---

## M6 多线程加速（✅ 2026-07-06 完成；实测记录见 PROGRESS.md「M6」段）

目标：远端存储直链的**服务器中转下载/播放**与**跨存储转存拉源**支持并发 Range 分块加速。
依赖事实：只依赖"支持 Range 的 HTTP 直链"（OneDrive downloadUrl / PikPak medias link 均支持），
开发/测试全程 httptest mock，无真实账号不阻塞（PROGRESS.md 已确认）；真实账号只影响最后调参。
配置入口已就位：driver.CommonRemoteFields 的 proxy/threads/chunk_mb（M4 已进后台动态表单），
本地驱动无这些字段 → 行为完全不变。前端零改动。

1. **internal/stream/accel.go —— 并发 Range 下载器（新包，纯标准库，不依赖项目其他包）**
   - `type LinkProvider func(ctx) (url string, header http.Header, err error)`：
     返回当前可用直链；首个分块前惰性调用一次并缓存；分块遇"过期状态码"强制重取
   - `func NewMultiReader(ctx, provider LinkProvider, offset, length int64, threads int, chunkBytes int64) io.ReadCloser`
     - 输出 [offset, offset+length) 共 length 字节，length 必须 >0（size 未知走不了加速，调用方退化单流）
     - 分块表：chunks=ceil(length/chunkBytes)，第 i 块 = [offset+i*chunk, min(offset+(i+1)*chunk, offset+length))
     - worker 数 = min(threads, chunks)；内部 context.WithCancel，Close()=cancel+等 worker 退出（WaitGroup）
     - **滑动窗口**：共享计数发块序号；worker 领到序号 i 后先等 `i < nextRead+window`（sync.Cond），
       window=threads → 内存上限 ≈ (threads+1)×chunkBytes（默认 4×4MB≈16MB/流）
     - 结果存 `map[int]chunkResult{buf,err}`+Cond 广播；Read() 等 map 中出现 nextRead 块→取走删除→
       广播（放行 worker 领新块）→从当前块切片逐段拷出；块 err → Read 返回该 err（之后恒返回）
     - **分块抓取**（每块最多 3 次尝试，重试间 backoff 100ms×attempt，select ctx.Done 可中断）：
       GET + `Range: bytes=start-end`（end 含）+ provider 的 Header 逐个 Add；
       - 206 → io.ReadFull 读满块长；短读/断流(ErrUnexpectedEOF/网络错) → 重试
       - 200（服务器不认 Range）→ 仅当"单块且 offset==0"可接受（读 length 字节后弃余量），
         否则报错"源不支持 Range 分块"（调用方保证远端直链支持，此为防御）
       - 401/403/404/410 → 判为直链过期：**强制刷新后重试**
       - 其余 4xx/5xx → 直接重试（不换链）
     - **换链单飞**：缓存 {url,header,gen}+互斥锁；worker 带着自己用的 gen 请求刷新，
       gen 未变才真调 provider 并 gen++（防 N 个 worker 同时刷、防新链被旧失败覆盖）
     - 每块请求挂 2min 超时子 ctx（防单块卡死堵住全窗口）
   - `func Serve(w, req, name string, modtime time.Time, size int64, ctype string, provider LinkProvider, threads int, chunkBytes int64)`
     —— 代理响应组装（Range 解析+头+MultiReader 拷贝），与 gin 解耦便于单测：
     - 解析客户端 Range（只支持单区间，播放器均为单区间）：
       无 Range→200 全量；`bytes=a-`→[a,size)；`bytes=a-b`→[a,min(b,size-1)]；`bytes=-n`→末 n 字节；
       语法错/多区间/a≥size → 416 + `Content-Range: bytes */size`
     - 响应头：Accept-Ranges: bytes、Content-Type、Content-Length=length、
       206 时 Content-Range: bytes a-b/size、Last-Modified；HEAD 到此返回不拉流
     - size 未知(<0) → **单流透传**：直接 GET 直链并原样转发客户端 Range 头，
       镜像上游 status/Content-Length/Content-Range 后 io.Copy
     - io.Copy(w, mr)；客户端断开→req.Context 取消→MultiReader 内部退出
2. **internal/fs —— 暴露挂载加速配置与刷新回调（fs.go 增量）**
   - `type AccelOpts struct{ Proxy bool; Threads int; ChunkBytes int64 }`
   - `(m *Mount) accelOpts()`：解析 m.Cfg——proxy=="true"；threads Atoi 缺省 4 钳 [1,32]；
     chunk_mb 缺省 4 钳 [1,64] 转字节（local 等无字段的驱动天然得到 Proxy=false）
   - `type LinkResult struct{ Link *driver.Link; Info model.FileInfo; Accel AccelOpts;
     Refresh func(ctx) (*driver.Link, error) }`
   - `func (f *FS) LinkEx(ctx, u, p) (*LinkResult, error)`：现 Link 逻辑迁入，
     Refresh=闭包再调 m.drv.Link(ctx, rel)；原 `Link()` 改为 LinkEx 的薄包装（签名不动，调用方不改）
3. **internal/server/handler_raw.go —— 代理模式接入**
   - rawHandler 改用 LinkEx；lk.Local 路径 ServeContent 不变；lk.URL：
     - `!Accel.Proxy` → 302（现行为，回归不变）
     - `Accel.Proxy` → 先设 Content-Disposition(dl=1) → provider=首链缓存+Refresh 适配
       （驱动刷新后仍返回 Local 或空 URL → 报错）→
       `stream.Serve(c.Writer, c.Request, fi.Name, fi.Modified, fi.Size, ctype, provider, Threads, ChunkBytes)`
   - 语义：客户端 Range 起点透传（播放器 seek 直达）、多线程只在服务器↔云盘一侧，对客户端仍是单流
4. **internal/fs/transfer.go —— 转存源复用**
   - copyOne 拿到 lk 后分流：`lk.URL!="" && sm.accelOpts().Threads>1 && fj.size>ChunkBytes`
     → `stream.NewMultiReader(ctx, provider, 0, fj.size, Threads, ChunkBytes)`（provider=
     首链复用本次 lk+Refresh 再调 sm.drv.Link）；否则维持原单 GET / Local 句柄
   - countingReader 仍包在最外层 → 进度/重试回退语义不变（accel 内部块级重试对进度透明，
     因为字节只在按序输出后才计数）
5. **测试**
   - `internal/stream/accel_test.go`（httptest mock Range 源）：
     顺序正确性（数 MB 确定性内容 chunk 256KB threads 4 全量比对+offset/length 边界）/
     乱序完成（按块注入不同延迟）/慢分块不破序/滑动窗口上限（消费端暂停时服务端收到的
     最大 start ≤ 已消费+window×chunk 容差）/403 过期换链（URL 带 gen，旧 gen 一律 403，
     断言 provider 被调 ≥2 且内容对）/断流注入（某块首个尝试只写半块即断连→重试成功）/
     200 不认 Range 报错与单块 offset0 例外/ctx 取消 Read 立返+Close 不悬挂/threads=1 退化
   - `internal/stream/serve_test.go`：200 全量/`bytes=a-`/`bytes=a-b`/`bytes=-n`/416/HEAD 无 body/
     size 未知透传（断言上游收到原样 Range、状态镜像）
   - `internal/fs`：accelOpts 解析默认/钳制；transfer_test.go 增 fake URL 驱动
     （Link 返回 httptest Range 服务 URL）跨存储 copy——内容一致+服务端收到多个 Range 请求
     （证实真的分块并发）+过期换链一次后成功
   - `internal/server/raw_proxy_test.go`（全链路）：_test.go 内 driver.Register 测试驱动
     （Link 返回 mock URL，cfg 含 proxy/threads/chunk_mb）→ 临时 sqlite INSERT storages →
     fs.Reload → server.New → httptest 起 Router → 登录 admin → GET /api/raw/... 断言
     200 全量一致、Range 206 切片一致、HEAD、proxy=false 时 302（若组装过重则退化为
     直调 rawHandler 的 gin 测试上下文，全链路留给 curl 冒烟）
6. **验证**：go build/vet/test 全绿（无 gcc，-race 不可用 → -count=3）；重建 exe 起服务：
   curl 回归本地存储 raw 206 Range 不变；`node frontend/e2e-check.mjs` 全流程截图无回归。
   真实账号联调时补：threads 默认值调参、真实直链过期时序、PikPak UA 绑定实测
   （并入 M3/M4 联调批次）。

---

## 后续里程碑（届时按此粒度补充细则）
- **M10 转码**：internal/media/probe.go ffprobe -show_streams -show_format JSON→三档决策
  （direct：mp4/webm 容器且 h264/vp9/av1+aac/mp3/opus/flac；remux：编码可播容器不认 `-c copy`；
  transcode：视频 libx264 veryfast（音频兼容时视频尽量 copy 只转音频→aac））；
  media/hls.go 会话管理：HLS fMP4 分片到 data/transcode/<id>/、m3u8 轮询等待生成、
  seek 越界→-ss 重启会话、空闲 5min 回收、并发≤2；/play 页 hls.js 接入。
- **M12 Docker**：三阶段 node:22-alpine→golang:1.25-alpine(CGO=0,-s -w)→alpine+apk ffmpeg；
  compose ./data:/data ./files:/files 5244 healthcheck=/api/ping；README（挂载教程/token 获取/
  忘记密码=删 data/newlist.db/转码与加速说明/AGPL 说明）。
