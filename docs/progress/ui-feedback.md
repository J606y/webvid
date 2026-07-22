# UI 迭代 / 用户反馈存档（从 PROGRESS.md 归档，2026-07-23）

> 用户反馈 #1~#52 的迭代记录与踩坑明细。历史归档，只读参考。

## UI 迭代记录（用户反馈）
- 2026-07-19 反馈#52「给这个项目设计一个图标」（此前项目完全没有图标：无 favicon、
  manifest 无 icons、无 apple-touch-icon，iOS 加主屏用网页截图兜底）：
  设计 = 产品自身视觉语言的浓缩——暗底 #0a0a12 + 四色极光光斑（取 glass.css .aurora
  同源色蓝/紫/青/粉，透明度加档补小尺寸失色、光斑放大推向中心让颜色从玻璃后透出，
  v1 教训：光斑守角落会中心发灰没毛玻璃感）+ 液态玻璃圆（上亮下暗白渐变模拟磨砂 +
  顶亮底弱描边）+ 白色圆角三角（stroke-linejoin:round 圆角法，重心右移视觉居中），
  即「播放器暂停态图标坐在极光上」。母版 frontend/icon-lab/icon-master.svg（512 全出血
  方版，手写 SVG 纯渐变无 filter），生成管线 icon-lab/render-icons.mjs（Playwright/Edge
  栅格化）产 public/ 全套：favicon.svg（包 rx 22.4% 圆角裁切成 app 瓦片，标签页/顶栏/
  登录页共用）+ icon-{192,512}.png（manifest any+maskable，玻璃圆 d300 在 maskable 安全
  区内）+ apple-touch-icon.png 180（全出血，圆角 iOS 自己裁）+ favicon-32.png +
  favicon.ico（PNG-in-ICO 容器手拼 22 字节头，兜老 UA 硬编码 /favicon.ico）；
  icon-lab/preview.html 预览板（iOS 圆角/Android 圆形/32/16/亮底形态一屏自查）。
  接线 = manifest.webmanifest 加 icons 数组 4 条、index.html 加三条 link（svg 优先 +
  png 兜 Safari 桌面 + apple-touch-icon）、App.vue 顶栏与 Login.vue 登录卡的通用
  Platform 显示器图标换成 img /favicon.svg（22px/64px，Login 原渐变底框删掉——瓦片
  自带底；两处 Platform import 同步移除防 lint）。验证 = 隔离实例 curl 7 资产全 200
  且 MIME 正确（.ico 自动 image/x-icon）、manifest icons 解析 4 条、登录页/顶栏截图
  _shots/icon-{login,topbar}.png 复核、mobile-check 37/37 零控制台错误；npm run build
  + go build 已重嵌。注意：iOS 已加主屏的用户需重启实例后重新「添加到主屏幕」才换新图。
  二轮（用户：「有点丑，用首字母 W 做一个艺术的 icon」）：图标重设计为 W 字标——两条
  V 形圆头彩带交叠成 W（左 V 蓝#4664ff→紫#9646ff、右 V 青#00b9be→粉#ff5fa5，仍取
  .aurora 极光四色），右 V mix-blend-mode:screen 让交叠处提亮成半透明「玻璃彩带」呼应
  液态玻璃语言；暗底 #0a0a12 + 顶部淡蓝紫雾光；中间两顶点(y208)压低于外侧(y172)保 W
  字形层级（一轮草稿几乎同高读作 X 交叉——教训）；字形含圆头端帽落在 maskable 圆形
  安全区(r≈205)内。对比过的方案 A（一笔 W + 极光渐变 + 宽笔画光晕垫底）因光晕显脏
  淘汰，探索稿留 icon-lab/w-{a,b}.svg + compare.html。母版重写后跑 render-icons.mjs
  再生全套资产（管线/接线不变，favicon.svg 1.4KB），二进制已重编、5243 已带新图标
  重启。坑：playwright setContent 页面里 file:// 子资源被 Edge 拦（about:blank 上下文），
  预览板须落盘 html 再 goto file:// 打开。
- 2026-07-19 反馈#51「播放器 iOS 暂停三角消失 + 手机底栏控件
  选项排列太挤」（均 Play.vue，改 YouTube 液态玻璃后的遗留）：
  ① 真因 = f3be8b5 把 state 图标换成纯三角时 svg 只写了 viewBox 没带 width/height 属性
  （ArtPlayer 自带图标全都带），iOS WebKit 对无宽高属性的 svg 在 flex+百分比尺寸链
  （.art-icon 42% → svg 100%）里解析成 0 高——玻璃圆是 CSS 画的所以还在、三角 glyph
  消失；桌面 Blink 解析正常故只 iPhone 复现。修 = svg 串补显式 width/height="32"、
  .art-state .art-icon 由 42%/4% 百分比改定像素 32px/3px，顺手摘掉其上的
  filter:drop-shadow（iOS 光栅化雷区，同 #35 极光教训；对比度交给玻璃圆底色+描边）。
  ② 两笔账：a) 桌面尺度的胶囊（min-width 42 + gap 8 + 时间胶囊两侧 12px 内边距）在
  390px 屏减页边距后 ~350px 的控件行里七颗排不下（合计 ~390px）；b) ArtPlayer 检测到
  手机 UA 加 .art-mobile 给左右控件组负边距让图标贴边——我们的 margin:0 与它平级
  （(0,3,0) 打平），而 ArtPlayer 样式是运行时注入排在产物 CSS 之后会赢，所以真机上
  负边距其实一直生效、玻璃胶囊贴边。修 = ≤768px 媒体查询整体收一号（胶囊 36/34、
  圆角 12、图标 20px、gap 5、--art-padding 8、时间字号 12px，整行 ~320px 落进一行留
  呼吸空间；--art-control-height 回 44 别用 .art-mobile 压的 38 太矮）+ 基础样式区
  用更高特异性 .art-video-player.art-mobile .art-controls-left/right 把负边距压回 0。
  回归：新增 frontend/player-check.mjs 12 项（iPhone UA 触发 .art-mobile 断言 svg 渲染
  尺寸非零/带宽高属性/无 filter/七颗控件不越界/负边距归零 + 桌面尺度不回归，NL_BASE
  可指隔离实例）+ mobile-check 37 + detect-check 3 + progress-check 15 + zoom-check 30
  全绿零控制台错误；npm run build + go build 重嵌完成，_shots/player-fix-{mobile,desktop}.png
  复核。①的最终确认需用户 iPhone 真机复测（本机无 iOS WebKit，修法为消除机制）。
- 2026-07-11 反馈#43「远端视频抽帧失败 exit status 0xcecfcb08 + telegram 读取消息失败
  rpcDoRequest: context canceled 是什么问题」：诊断+修复两件。
  ① 0xCECFCB08 按 32 位补码 = FFmpeg AVERROR_HTTP_UNAUTHORIZED（FFERRTAG(0xF8,'4','0','1')，
  新版 ffmpeg 直接把负错误码当进程退出码）= 抽帧途中收到 HTTP 401。本机侧已排除（回环
  token 现签 JWT、fsError 只产 400/404/409/500/501），唯一 401 通道 = 未开代理模式的云盘
  挂载 /api/raw 302 到直链、OneDrive tempauth 直链提前作废回 401；而 ffmpeg 的
  -reconnect_on_http_error 只认 429/5xx，401 一发即死（重试同一条死链也无用）。
  修 = rawHandler 对内部读取方（X-Internal-Auth，ffmpeg/ffprobe 探测/抽帧/转码）一律
  不 302、进 rawProxy→stream.ServeSingle 单流透传：openUpstream 首字节前对 401/403/404/410
  经 RefreshLink 绕直链缓存换链重试（#38 现成设施），彻底失败回 502（5xx，ffmpeg 会退避
  重连、每次重连都拿新鲜链）；外部请求行为不变（非代理挂载仍 302）。
  ② telegram 那条 = 伴随噪音非 TG 故障：ffmpeg/ffprobe 对 http 输入频繁开-关连接（初始
  探测/跳尾部 moov/-ss seek 各开一条、旧连接直接掐断），每个 /api/raw 命中 TG 挂载都触发
  一次 message() RPC（刷新 file_reference），被掐连接的在途 RPC 报 context canceled，经
  fsError→Fail500 落 [500] 日志。修 = Fail500 检测请求 ctx 已取消（客户端已挂断，响应
  无人可见）时不记日志、回 499（nginx 风格 client closed request）。
  回归：raw_proxy_test.go 新增 TestRawDirectLinkInternalProxied（内部头+直链盘 → 206 单流
  且上游恰 1 请求 + 无内部头仍 302 到上游；坑：断言客户端必须禁跟随重定向，否则 302 被
  跟随后 body 也对得上、区分不出新旧行为）；go test ./... 11 包全绿；webvid.exe 已重编
  （用户运行中实例需重启才吃到）。改动已单独提交推送、未发版（并行会话 #42 WIP 未混入）。
- 2026-07-11 反馈#41「视频库点视频弹出的二级页面加 iOS 那种从哪来回哪去的动画」
  （#39=存储密钥明文回显 f625d6c、#40=文件页目录点击反馈 3bb8e8a，均另一会话）：
  VideoDetailCard 加 hero 转场。LibraryVideo 四处 openDetail 传 $event，来源矩形取点击卡
  的 16:9 封面框 .art（Featured 横幅取整幅）；开场在 open() 的 nextTick（首帧 paint 前）
  用内联 animation:none 接管 EP 的 modal-fade/dialog-fade（overlay 是 v-show 持久节点，
  内联样式常驻，此后每次开合全由 JS 驱动），FLIP：transform-origin=顶边中点、缩放取宽度
  比 → 卡片顶部 16:9 封面区与来源缩略图严丝合缝重合、信息区从封面下方长出，transform
  0.42s cubic-bezier(0.32,0.72,0,1)（iOS Sheet 曲线），遮罩（含 blur(8px) 磨砂）由 JS 驱动
  opacity 0→1 淡入；关闭（ESC/点遮罩走 :before-close(done)，右上关闭钮共用 animatedClose）
  反向缩回来源矩形 0.34s+遮罩滞后 0.1s 淡出，360ms 收尾先清 overlay 的内联 transition 再
  done()，EP leave 立即完成、节点马上 detached（检查脚本等的就是 detached）。iOS 安全
  （反馈#35 教训）：全程只动 transform+overlay opacity 纯合成器属性，动画期 .vdc-zooming
  临时停掉卡片及内部控件 backdrop-filter（WebKit 对磨砂层做缩放动画时采样区域不随
  transform 走会错位闪烁）换近实底 rgba(17,19,28,.97)，落定移除类后 background 0.25s 过渡
  回玻璃；prefers-reduced-motion 退化为整层快速淡入。细节：封面加 480 低清底图
  .vdc-art-lo 垫底（列表缩略图已在浏览器缓存，起飞瞬间就有画面，1200 高清加载后盖上）；
  开场中途被关时布局矩形用开场量好的 cardRect 反推（此刻 gBCR 含中途 transform 不可用）；
  el-overlay-dialog 动画期 overflow:hidden 防起飞姿态探出视口闪滚动条；play/goDir/路由
  离开仍直接关、不缩回（导航场景缩回反而拖沓）。
  顺修三件：① Play.vue 的 path 由 computed 冻结为进页时的 ref——离开时路由先变、组件后
  卸载，onBeforeUnmount 末次进度与迟到的 loadedmetadata 补报会拿 "/" POST /media/played
  404（控制台噪音+末次进度实际丢失）；② detail-check/scroll-check 的立即播放按钮定位改
  稳定类 .vdc-play（有续播进度时文案变「继续观看」失配，mobile-check 同款前科）；
  ③ 检查脚本环境陈旧修复：scroll-check 深滚目录原写死 /test/ 深层路径（07-10 起 /test 换
  TG 收藏夹后 404 卡死整套）改 /本地存储/电影、深滚阈值 1500→200（效力在 ±50 位置对比不在
  绝对深度），history-check 播放历史种子换现存样本（暗夜迷城/落日之城已不在样本集，
  上报 404）。
  回归：新增 frontend/zoom-check.mjs 13 项（开/关均入 vdc-zooming+磨砂暂停恢复+落定样式
  清理+overlay 二次复用+点遮罩+reduced-motion）；detail-check 15 + mobile-check 37 +
  progress-check 15 + detect-check 3 + scroll-check 15 + history-check 12 全绿且零控制台
  错误；转场中间帧截图 _shots/zoom-t1-early/t2-mid/t3-settled.png 确认卡片确实从点击封面
  处长出；webvid.exe 已重编嵌新前端。
  二轮（用户反馈「随机推荐大图弹出像从屏幕外飞进来、关闭像飞出去」）：Featured 横幅比
  卡片还宽（1400 视口下 ratio≈1.85），按宽度比算的起飞态比落定态更大、四边探出屏幕=
  飞入/飞出观感。修=normalizedOrigin：来源宽 ≥ 卡片 92% 时收缩成「贴在来源中心、92%
  卡片大小」的虚拟矩形，开/关共用——横幅变成从中心浮出/缩回中心淡出的 iOS 弹出手感，
  普通缩略图（恒小于卡片）不受影响仍严丝合缝 morph；zoom-check 增 3 项（先起 rAF 采样
  循环再点击，记录转场期最大缩放断言 ≤1，防回归）至 16 项全绿，横幅中间帧
  _shots/zoom-hero-t1/t2/close.png 复核。**坑：验证时 5243 上是用户自己跑的旧二进制实例
  （taskkill 被用户拒绝勿动），zoom-check 打过去横幅断言假阴性——先
  powershell Get-Process StartTime 对比 exe LastWriteTime 确认新旧，再 NL_PORT=5299 起
  隔离实例 + NL_BASE 指过去验证（16/16）；用户实例需重启才吃到新二进制。**
- 2026-07-11 反馈#44「随机推荐的二级弹窗动画改成：打开时大窗口向前缩小到二级弹窗，关闭时
  由小窗口向后放大到大窗口」（#43=另一会话的远端抽帧 401/telegram 噪音修复，编号撞车后本条
  让号改 #44）：**推翻 #41 二轮的 92% 虚拟矩形中心浮出方案**（当时用户嫌真实
  矩形起飞"像从屏幕外飞入"，现在明确点名要大小窗互变）——删 normalizedOrigin，Featured 横幅
  恢复按真实矩形 morph：开场起始态=横幅矩形（缩放≈1.85 的"大窗口"）向前缩小落定成卡片，
  关场由卡片向后放大回横幅。为免重蹈"凭空砸出巨大卡片"的飞入观感，超大来源（from.width >
  cardRect.width，只有横幅命中）配快速 opacity 淡入（0.22s，与 0.42s 缩小并行=从横幅里凝聚
  出来）/淡出（0.26s 延 0.08s，放大同时消融回横幅）；opacity 纯合成器属性 iOS 安全（#35 教训
  只针对 filter），普通缩略图（恒小于卡片）不带淡入淡出、仍走严丝合缝封面重合 morph。内联
  opacity 在 zoomTimer/closeTimer/cancelClose 三处收尾全清（横幅关场淡出中途被 cancelClose
  截断须复原不透明）。回归：zoom-check 第 4 节重写（采样循环记录转场期 maxScale+minOpacity：
  开/关均断言缩放 >1.1 且不透明度 <0.9，方向与旧断言"≤1 不从屏幕外飞入"正好相反）22→25 项
  全绿；顺修 mobile-check「详情操作按钮可见」has-text("立即播放")→稳定类 .vdc-play（首页
  网格 sort=random，抽到有续播进度的视频文案变「继续观看」假红，同 #29/#41 前科）；
  detail 15 + mobile 37 + scroll 15 + history 12 + progress 15 全绿零控制台错误，转场中间帧
  frontend/_shots/zoom44-open-t1/t2、zoom44-close-t1/t2 目检确认大窗口缩小落定/放大消融回
  横幅；webvid.exe 已重编嵌新前端。
  **二轮（速度）**：用户要求整体调慢——开场 0.42s→0.6s、关场 0.34s→0.48s，遮罩/淡入淡出
  同比拉长（开 0.3s、关淡出 0.36s 延 0.1s、遮罩 0.32s 延 0.14s），收尾定时器 480→660、
  360→500ms。**三轮（桌面端贴四边）**：等比大窗口高度=卡片高×宽度比、远高过横幅上下探出
  屏幕，用户仍觉"像飞出来"→新增 coverTransform（非等比 scale(sx,sy)，起始态精确贴合横幅
  四边、从大图向内收缩落定，途中纵横比渐复原叠加淡入几乎无感）；**仅桌面端（>768px）启用，
  移动端横幅与卡片几乎同宽（scale≈1.06 本无飞入感）保持等比第一版手感（用户确认）**；
  关场后经反馈"没回原位"也改为对称的贴四边放大回横幅（桌面）+ **回位锚点改轮播容器**
  （.el-carousel——弹窗开着的工夫轮播 6s 自动切走原 item，被 translate 挪出画面，按其实时
  矩形回位会飞向屏幕外；容器=横幅在页面的固定位置，item 恒填满容器）。回归 zoom-check
  第 4 节采样加 minScaleY/贴四边断言、横幅点击一律 .el-carousel__item.is-active（轮播自转
  后点非激活 item 会被 .page 拦截，探针实测踩过）。
- 2026-07-11 反馈#45「所有动画关闭回原位后都闪一下（先只报移动端后确认全平台）」：
  closeTimer 收尾先 done() 再同步清 dlg 内联 transform/opacity——done() 后节点由 Vue **异步**
  卸载（destroy-on-close 下次打开是全新节点），若在移除前抢先复原样式，卸载前被绘制的那一帧
  = 全尺寸、全不透明、居中的卡片闪现（缩略图关场无淡出对比更强，桌面也可见）。修=收尾
  **不再复原 dlg 内联样式**（节点即将销毁本就无需清理；overlay 的 pointerEvents/ovd overflow
  等持久节点样式照旧清）；cancelClose 路径（关场中途重开）仍复原样式不受影响。逐帧采样探针
  验证：桌面/移动缩略图与横幅关场淡出/缩小后均无回弹帧。
- 2026-07-11 反馈#46「二级弹窗莫奈取色要弹窗打开约 1s 后才见效」：取色一直挂在 1200 高清图
  onload（服务端出图+下载≈1s）；修=480 低清底图 .vdc-art-lo 的 @load 也挂 onArtLoad（24×24
  缩样取色对源分辨率不敏感，480 列表页已缓存基本秒着色），高清到货后再精修一次（同图主色
  几乎一致，肉眼无跳变）。
- 2026-07-11 反馈#47「移动端+桌面端 dock 栏点击切换加动画」：桌面顶栏 .nav 与移动底部
  .tabbar 加 iOS 式滑动指示丸——一枚胶囊背景（.nav-pill/.tab-pill，absolute+translateX+width
  过渡 0.35s iOS 曲线）在选中项之间滑动，选中项自身 background 移交指示丸（项加 z-index:1
  垫层）；App.vue movePill 量 active 项 offsetLeft/offsetWidth（tabbar 的 offsetLeft 已含 5px
  内边距故 pill left:0 起算），watch route.path+resize 同步；**从隐藏态出现（刷新进页/后台/
  播放页回来）直接就位淡入不滑动**（style.opacity!==1 判 appearing，transition 只留 opacity），
  无选中项（后台/播放页）淡出。只动 transform/width 小元素，无 filter（iOS 安全）。
- 2026-07-11 反馈#48「弹窗玻璃磨砂 0.5~1s 后才变液态玻璃→下方玻璃区闪一下→背景渐渐变深」
  **三轮定稿：详情卡自身永不挂 backdrop-filter**。背景：#41 起 iOS 安全要求转场期停卡片磨砂
  （WebKit 对 backdrop-filter 层缩放动画采样区域不随 transform 走会错位），而"动画期配方"与
  "落定配方"之间**无论怎么切换用户都看得见**：一轮近实底→玻璃（0.66s 后+0.25s 过渡=磨砂迟到）；
  二轮改遮罩 blur(8px) 垫底+玻璃底色、落定瞬间恢复深磨砂 saturate(1.7)=闪一下；三轮把深磨砂
  搬 ::after 层 opacity 0.3s 淡入（滤镜先在 opacity:0 静默启用）=平滑但"背景渐渐变深"仍被看出。
  定稿（用户点名"保持刚打开的透明效果"）：压掉 glass.css 全局 .el-dialog 深磨砂，观感恒由
  **遮罩整页 blur(8px)+卡片 var(--glass-bg) 半透明白**提供，动画期与落定后完全同一配方；
  卡内按钮/关闭钮的小磨砂也永久关闭（背后是平滑渐变，模糊与否无差，原先落定恢复=又一处
  半秒变一下）。验证=开卡逐帧采样 195 帧，卡片+按钮 filter/背景配方签名全程唯一；落定前后
  信息区截图肉眼一致。zoom-check 磨砂断言改 noOwnFilter（任何阶段卡片含 ::after 不得有 blur）
  +新增「遮罩整页磨砂在场」，25→26 项。**坑：::after 方案曾因基础 transition 在"进转场"方向
  也生效（磨砂 0.3s 才关完）触发 iOS 风险，须 .vdc-zooming::after 加 transition:none 瞬时关——
  该方案已整体废弃，仅留此教训**。
- 2026-07-11 反馈#49「打开图片也要 iOS 同款从哪来回哪去动画」：照片墙三入口（Featured 横幅/
  最近查看货架/照片网格）接 PhotoSwipe 原生 zoom 转场——utils/lightbox.js openLightbox 加第 4
  参 getEl(i)（第 i 张图对应的缩略图元素），有则 showHideAnimationType:'zoom'+addFilter('thumbEl')
  （无则保持 fade，文件管理页沿用）；items 全部标 thumbCropped:true（列表缩略图均为 cover
  裁切，转场按裁切对齐画框不变形）；元素不在/被 hideImg 隐藏（offsetWidth=0）交回默认退化
  fade。LibraryPhotos openList 加 sel 参（querySelectorAll 文档序与 v-for 同序）：横幅
  '.feat .el-carousel__item .feat-img'、货架 '.shelf-row .p-card .art img'、网格默认
  '.photo-grid .cell img'。**关场缩回"当前张"缩略图处（灯箱翻页后回新位置），iOS 相册同款**。
  顺修检查脚本环境陈旧两处：photos-check 目录样本 /本地存储/图片/风景（已不存在）→
  /本地存储/照片、photos-history-check 种子照片换现存样张 1~5（原种子 404 刷控制台）；
  photos-check 19/19 + photos-history-check 11/11 零错误。
- 2026-07-11 反馈#50「点击用户的弹窗改透明一点」：glass.css 给 el-dropdown 弹层
  （html.dark .el-dropdown__popper.el-popper）单独降底色 rgba(26,28,46,0.6)→0.38，
  blur(24px) 磨砂保留保证可读性；其他 popper（选择器/气泡）不动。
- 2026-07-11 检查脚本基建：全部 9 个 e2e 脚本（zoom/detail/mobile/scroll/history/progress/
  detect/photos/photos-history）统一支持 NL_BASE 环境变量指隔离实例（此前 detail/mobile/
  scroll/history/progress/detect 写死 5243，跑检查会误打用户生产实例+污染播放历史）。
  #44~#50 完成后全量回归：zoom 26 + detail 15 + mobile 37 + scroll 15 + history 12 +
  progress 15 + detect 3 + photos 19 + photos-history 11 全绿零控制台错误（NL_PORT=5299
  隔离实例）。
- 2026-07-11 反馈#42「随机推荐点开二级弹窗关闭后约半秒无法上下滑动页面（弹出后立刻关闭
  必现）」：#41 hero 关场的连带账——animatedClose 要等 360ms 缩回动画完才调 done()，EP 的
  body 滚动锁（el-popup-parent--hidden，overflow:hidden 传播到视口）done() 后其 useLockscreen
  cleanup 还再延 200ms 才摘，期间全屏 .el-overlay 又一直拦触摸/滚轮 ≈ 560ms 页面完全失聪。
  修：关场一开始就 ①手动摘掉 body 的锁类（**body 上 EP 加的滚动条宽度补偿 inline width 不
  能动**——留给 EP 稍后的幂等 cleanup 恢复，摘类瞬间滚动条回归、宽度补偿还在=零跳版；EP
  cleanup removeClass 幂等无副作用）②overlay pointer-events:none 输入穿透（v-show 持久节点，
  收尾/取消时必须清回，否则漏到下次打开），缩回动画降级为纯收尾演出、输入立即还给页面。
  连锁防护：遮罩放行后关场途中能直接点到下面的卡 → open() 里 closing 时先 cancelClose()：
  清 closeTimer（否则 360ms 后迟到的 done() 会把新开的卡关掉）、复原转场内联样式
  （transition:'none' 防 transform 复位走 0.34s 缩放）、**把锁类补回**（visible 全程未翻
  false，EP 不会自己再加）。顺修一处 #41 死分支：「开场中途被关」判断原写
  `dlg.style.transform ? cardRect : gBCR`，但 zoomIn 同步就把内联 transform 清空靠
  transition 补间 → 补间期该值恒为空串，中途关闭实际拿的是含中途缩放的 gBCR 当布局矩形、
  缩回目标算错（用户复现步骤恰好踩中）；改判 `classList.contains('vdc-zooming')`（补间期
  布局盒没变恒等于 cardRect）。回归：zoom-check.mjs 13→22 项（关场期锁已解/遮罩穿透/页面
  立即可滚——scrollBy 在锁未解时是空操作天然可判、关场途中重开新卡存活+锁类恢复、开场补间
  中途 ESC 正常缩回；ESC 后单次 evaluate 一把采完防 360ms 窗口内多次往返 flake）+
  detail 15 + scroll 15 + history 12 + progress 15 + mobile 37 全绿零控制台错误；
  webvid.exe 已重编嵌新前端。
- 2026-07-11 反馈#38「OneDrive 开代理模式后部分视频一直重连播放不出（另一部分正常）」：
  真因三层，核心=代理分块加速（stream.MultiReader）对"顺序整读大文件"的访问画像是
  **每 4MB 一个新 HTTP 请求**——mkv/ts 等需转码片 event-remux 全速拉源，4GB≈千次请求、
  持续 5~15 req/s，OneDrive/SharePoint 按请求频率限流（429/503 + Retry-After 常达数十秒），
  旧重试仅 3 次×100ms 退避，限流窗口必然掐死整条流→ffmpeg 把断流当 EOF、remux 出
  "列表合法但截断"的片子或 probe 直接失败→hls.js/播放器反复重试又加剧限流=「一直重连」；
  直连 mp4 浏览器按缓冲节奏慢读、请求稀疏不易触发=「一部分正常」（按格式二分的表象）。
  次因①换链无强制通道：分块 401/403 后 Refresh 走 drv.Link 命中 10min 缓存拿回同一条死链，
  重连在 TTL 内持续失败；②stream 包零日志，线上静默无从诊断。修复五件套：
  ① internal/stream/accel.go：429/503 按 Retry-After 等待后重试（**不消耗尝试次数**，
  单块累计预算 2min、单次封顶 30s、无头缺省 2s），硬错误 3→4 次退避 100→200ms 基数；
  分块最终失败落 `[stream]` 日志（ctx 取消不算）；
  ② stream.ServeSingle 单连接透传 + rawProxy 按 X-Internal-Auth 分流（isInternal 从
  downloadWriter 抽出共用）：内部读取方（ffmpeg/ffprobe 转码/探测/抽帧）整个响应只向云盘
  发 **1 个请求**（回归 302 直连时代的温和画像），打开期 429 等待/401 换链都在首字节前，
  开始写响应后的断流交给读取方续传；浏览器/下载仍走多线程分块加速不受影响；
  ③ ffmpeg/ffprobe http 输入恒挂 `-reconnect 1 -reconnect_streamed 1 -reconnect_delay_max 30
  -reconnect_on_http_error 429,5xx`（media/probe.go httpInputArgs 三处共用；本机 ffmpeg 8.1
  行为矩阵实测：不挂时半途断流被当 EOF 静默出短片、429 起播即死；挂上自动带
  `Range: bytes=<断点>-` 续传、429 重试后完整读完；404 不在列，删除的文件仍快速失败不空转）；
  ④ driver.LinkRefresher 可选接口 + OneDrive.RefreshLink（删缓存条目再 Link）——fs.LinkEx
  的 Refresh 回调优先走它，换链真正拿到新直链；
  ⑤ OneDrive.Stat 复用直链缓存（Link 同一响应本就带 size/mtime，name=path.Base）——代理/
  转码每次打开 /raw 都 Stat+Link，原每开必打一轮 Graph，probe+seek 连开数次放大请求量，
  现 TTL 内零 Graph 调用。
  回归：stream（限流恢复/ServeSingle 单请求/换链/200 全量退化）+ server
  （TestRawProxyInternalSingleStream：内部头=1 个上游请求、普通≥3）+ onedrive
  （RefreshLink 绕缓存/Stat 免 Graph）新单测；**server e2e TestProxyLoopbackTranscodeE2E**
  =刁难上游（首请求 429+每段只发 60% 即掐断）→ 真 ffmpeg 经代理回环 remux 出完整 ENDLIST
  （EXTINF 合计≥5.5s），反向禁用修复必红（`分块 0 下载失败: unexpected EOF`+probe 失败）；
  go test ./... 全绿 + hls-check 30/30 + detect-check 3/3 + progress-check 15/15 零控制台错误。
  杂项：转码样片目录曾被清空，已按 hls-check 清单 ffmpeg 重新生成 8 个样片 + rebuild 入索引；
  Chrome 顶层 exe 又 EACCES，检查脚本全部 sed 换 Edge。真实 OneDrive 复测待用户线上验证
  （本地开发库无 OD 凭据）。
- 2026-07-11 反馈#37「挂载 TG 后看不到收藏夹里的文件」：真因=listSaved 用
  messages.search + InputMessagesFilterDocument 拉列表——该 filter 只命中「以文件发送」
  的消息（TG 客户端「文件」页签同款语义），普通发送/转发的视频、音乐、GIF 底层虽同为
  Document 却不被命中；收藏夹以转发视频为主时列表接近全空（#36 当晚索引仅入库 4 项
  即此故——那 4 个恰是「以文件发送」的）。修（telegram.go）：改 messages.getHistory
  (InputPeerSelf) 全量翻页 + 客户端 docOf 挑文件，转发视频（常无文件名属性）走 entryName
  的 mime 兜底命名 file_<日期>.mp4；顺带①分页偏移改 m.GetID() 对所有消息类推进（防整页
  服务消息误判尾页截断）②docOf 跳过贴纸（DocumentAttributeSticker，非网盘意义的文件）。
  照片（MessageMediaPhoto 非 Document）仍不支持——要挂照片需在 TG 里「以文件发送」。
  单测 fakeSearch→fakeHistory + 新增转发视频/贴纸/照片/服务消息用例，go test ./... 全绿；
  e2e：NL_PORT=5299 隔离实例用库内真实会话列 /test 返回 7 个视频（修前同收藏夹只见
  4 个「以文件发送」项），验毕 taskkill //PID 收尾。注意 List 有 2min 缓存，刚转发的
  视频最迟 2 分钟后可见。**✅ 用户实测确认能看到文件了（07-11）**，TG 联调剩播放/复制到
  OneDrive 实测。
- 2026-07-10 反馈#36「TG 发送验证码后收不到」：真因=Telegram 对第三方 api_id 基本不发短信——
  账号在别处登录过时验证码走 App 内服务消息（官方账号「Telegram」/777000 的对话），坐等短信
  自然「收不到」；且旧「重新发送」只是再来一次全新 sendCode，同通道重发一遍照样收不到。
  修复（internal/driver/telegram/login.go）：
  ① SendCode 返回 CodeSent{sent_to,resend,timeout}——把 AuthSentCode.Type 译成人话（App 内
  消息/短信/语音电话/未接来电/邮箱/Fragment/FirebaseSMS）随 /send_code 响应透出，Admin.vue
  弹窗验证码框下常驻 .tg-hint 显示「码发到哪了 + 收不到点重发改用 XX」；
  ② 已有未过期登录会话时「重新发送」改走 auth.resendCode（同连接同 phone_code_hash）切换
  投递通道 App→短信→电话，成功更新 codeHash 并续期，失败（无备选通道/码失效）拆旧会话落回
  全新 sendCode；
  ③ SentCodeTypeSetUpEmailRequired（TG 强制先在官方客户端设登录邮箱的账号）直接报错说明，
  不再让用户干等一个永远不会来的码；
  ④ send_code 前端请求单独放宽超时 90s——后端 MTProto 握手预算 60s 与 axios 全局超时 60s
  同值，慢握手时前端会先断连、gin 的 Request.Context 跟着取消把发码带崩。
  冒烟：隔离实例（NL_PORT=5299+临时 NL_DATA_DIR）+假 api_id 存储打 /send_code，1.5s 拿到
  Telegram 真实 RPC 错 API_ID_INVALID 透传为 502 JSON（连接/发码/错误链路全通）；真实切通道
  待用户真机验证（sendCode 有频控，FLOOD_WAIT 别连点）。
  **坑：清理冒烟进程用了 `taskkill //IM webvid.exe`，按映像名把用户正跑着的 5243 实例一并
  杀了——杀进程一律 `taskkill //PID <pid> //F`**；已用新二进制重新拉起 5243。
  **二轮（同日晚）：用户确认查的就是官方对话（777000）但没消息**。日志实锤 19:32/19:38 两次
  发码均成功且 `type=*tg.AuthSentCodeTypeApp, next=<nil>`——无备选通道（第三方 api_id 常态），
  短信/电话切换根本不可用；type=App 同时证明该 +852 号确有活跃官方会话（Telegram 仅在有
  会话时才选 App 通道）、号码格式正确、未挂代理直连可达 DC。**真凶指向 gotd 默认设备指纹**：
  Options.Device 缺省时 initConnection 报 device_model=runtime.Version()（形如 "go1.26"）+
  system="windows"+langpack 空——第三方 api_id 配脚本指纹是 Telegram 风控「send_code 成功但
  静默不投递」的已知诱因。修（client.go newClient）：挂 **telegram.DeviceTDesktopWindows()**
  （gotd 内置 tdesktop 伪装：Desktop/Windows 10/6.9.1 x64/langpack tdesktop/时区参数）；直连
  Resolver=telegram.TDesktopResolver()（混淆+abridged 传输层同样不可分辨），socks5 路径
  PlainOptions 补 Protocol:transport.Abridged+Obfuscated:true 对齐。另 loginTTL 5→10min
  （App 通道投递偶迟数分钟，迟到的码仍能提交），App 通道且无备选时提示语补排查方向
  （多账号勿看错 / 该号需仍登录在某设备 / 频繁请求被静默、隔几小时再试）。
  **若换指纹后仍不投递的二分法**：web.telegram.org 官方登录同号——官方码能到 777000 而
  本应用不到=第三方 api_id 被风控（等几小时冷却再试）；官方也不到=账号/设备侧问题
  （核对多账号、设备列表里该号是否在线）。今天已发过多次码，重试前先歇一会儿防 FLOOD。
  **✅ 真凶实锤（用户实测确认修复）**：换 tdesktop 指纹后同一 App 通道 19:51:13 发码
  33 秒内收到码→登录成功→挂载重载→索引完成 20 条（TG 收藏 4 项入库）。此前 "go1.26"
  指纹下同通道两次均被静默丢弃——**gotd 接 Telegram 用户账号必须设 Device 伪装**，
  否则发码成功也收不到，此坑记死。Telegram 驱动真实联调登录环节打通。
- 2026-07-10 反馈#35「iOS 弹窗卡顿」**真因实锤（深夜，/debug/freeze 四轮真机二分）：极光背景的
  `filter: blur(120px)`**。机制：body 层出现 fixed+overflow:auto 弹层（EP 弹窗结构，#18 无类名
  复刻结构也卡）或带 backdrop-filter 的层（#19）→ iOS WebKit 把被 filter 模糊的极光 4 个大圆
  踢出 GPU 合成路径、按 3 倍屏用 CPU 重新光栅化 → 主线程/合成器停摆 10~12 秒。证据链：
  #15 复刻 EP 类名的纯 div 卡 12049ms（EP JS 洗清）、#16 不 teleport 主线程 37ms 但画面仍僵
  （合成器侧同一工作量）、#17 API 捕获模式抓不到任何 >100ms JS 调用（阻塞在样式/栅格化阶段）、
  **#20/#21 藏掉极光后同样弹窗（含全套磨砂）立即流畅**。安卓 Blink 在 GPU 算模糊且缓存结果，
  865 弱芯也不卡；iOS 全部浏览器同为 WebKit 所以都卡。**修复：极光禁用 filter，改多段
  radial-gradient 色阶直接画柔光圆斑（尺寸放大 ~25% 补偿模糊扩散），backdrop-filter 玻璃磨砂
  全保留不受影响**（glass.css .aurora 区块有「严禁 filter」注释）。
  **✅ 用户 iPhone 真机复测确认修复（「还真是这个问题现在已经不卡了」）。收尾已完成**：删
  FreezeTest.vue+临时路由、main.js 调试块整段还原（?debug/?console/?no-* 全撤）、glass.css
  dbg 规则删除、eruda 依赖卸载；最终改动面=glass.css 极光重写(+16/-10)+Admin.vue（autocomplete
  语义保留+存储表收窄）+Files.vue（导航竞态，与本反馈无关）；重编三平台二进制
  （webvid.exe / webvid-linux-amd64 / arm64，-trimpath -s -w 同 release 流水线）；回归
  mobile-check 37/37 + detail-check 15/15 全绿（**坑：Chrome 自动更新弄丢顶层 chrome.exe
  致 EACCES，sed 把检查脚本 executablePath 换成 Edge msedge.exe 即可跑**）。诊断过程记录：
  **⚠️ 状态更新（同日晚些）：用户 iPhone 真机复测「还是卡死几秒」→ 下述两个"治疗"全部无效已还原**
  （glass.css 的 iOS @supports 块与极光暂停、VideoDetailCard 的 decoding=async+取色延迟）。
  用户明确：磨砂/渲染方向此前已用 ?no-blur 实测排除（顶栏/底栏 .glass 也在开关覆盖内，排除可信）；
  `autocomplete="new-password"` 保留在树上但**未解决**编辑用户卡顿。两个卡顿的真因至今未定。
  **改走隔离诊断路线：新增临时页 `/debug/freeze`（FreezeTest.vue + router 临时路由，定位后整页删）**——
  把"打开一个弹窗"拆成 9 个独立变量逐项自动测量（点击后 3.6s 内最长 RAF 帧间隔/累计阻塞/首帧延迟）：
  #0 空白基线、#1 纯 div 遮罩（无 EP）、#2 空 el-dialog、#3 空 el-dialog lock-scroll=false、
  #4 +文本框、#5 +密码框(new-password,同编辑用户现状)、#6 +伪密码框(type=text+-webkit-text-security:disc,
  绕钥匙串的候选方案)、#7 +真实 1200 大图、#8 真实 VideoDetailCard；页顶常驻合成器指示方块
  （CSS transform 动画走合成器：卡顿时方块仍转=主线程卡[JS/自动填充/布局]，方块也停=渲染管线卡）+
  「复制全部结果」输出全部数字+UA+standalone。**等用户 iPhone 报数后按矩阵定位**：
  #2 大→EP 弹窗机制；#2 大#3 小→body 滚动锁整页重排；#5 大#4 小→密码框钥匙串扫描（#6 小则伪装方案可行）；
  #7 大→图片解码；#8 独大→详情卡特有逻辑；全都小但真实页面卡→与底下页面内容相关（库页海报墙）。
  chromium 冒烟：9 行全部出数（6-36ms 量级）、报告生成、零控制台错误，_shots/freeze-test-page.png。
  本条目下方原「真因分析/修复」段**保留作过程记录**，结论不成立勿再照抄：
  - ① 后台「编辑用户」卡 = **iOS Safari 密码自动填充扫描**：弹窗内 type=password 无 autocomplete
    提示 → iOS 当登录框去扫钥匙串/备填充建议，主线程被按住数秒。真机 A/B 证据：添加用户卡、
    添加网盘不卡，两窗同套玻璃样式 → 与磨砂/渲染无关。修：密码框 `autocomplete="new-password"`
    + name，用户名 `autocomplete="off"`（Admin.vue，上次会话已验并落地，本次保留）。
  - ② 视频详情卡卡 = **iOS WebKit 磨砂栅化风暴**：打开弹窗一次性栅化遮罩 blur(8px)（采样整页，
    视频库页几十张海报+轮播最重）+ 弹窗 blur(28px) + 内部按钮/标签各自磨砂，入场缩放期逐帧重算；
    且极光 blur(120px) 漂移动画令磨砂结果无法缓存 → 弹窗停留期间持续重栅化。修（本次重写）：
    - glass.css 末尾 **`@supports (-webkit-touch-callout: none)` iOS 专属块**（该属性 iOS/iPadOS
      WebKit 独有，iOS 上全部浏览器同内核都命中；**不能按屏宽一刀切——安卓同宽不卡，砍它的磨砂
      纯属误伤，上一版 ≤768px 方案因此被用户要求还原重做**）：模态层级（.el-overlay/dialog/drawer/
      message-box/loading-mask/popper/message/button/tag/carousel__arrow/.vdc-close）
      backdrop-filter:none!important，--el-bg-color 0.94 / --el-bg-color-overlay 0.96 实底顶上、
      --el-mask-color 0.72 加深补偿、.vdc 实底 rgba(20,20,30,.95)；页面级玻璃（顶栏/面板）不动。
    - **极光漂移在任何模态打开时暂停**：`body.el-popup-parent--hidden .aurora span
      { animation-play-state: paused }`（EP 锁滚动时挂的类）；背景被遮罩压暗、暂停肉眼不可辨，
      桌面 Safari 的磨砂弹窗也顺带受益。
    - VideoDetailCard.vue：封面 img 加 `decoding="async"`；莫奈取色挪 requestIdleCallback
      （软件 canvas 的 drawImage/getImageData 强制同步解码 1200px 封面，原在 load 事件里做正撞
      入场动画帧预算；旧 Safari 无 rIC 退化 setTimeout 120ms）。onArtLoad 拆成投递 + extractTint。
  - 顺修 mobile-check 唯一红「存储表格无内部横向溢出」：telegram 存储行让移动端存储表溢出——
    ①有 🔑 钮时 ops 列 124 令列合计 358 > 可用 336（实测 4 个 link 图标钮各 18px + 2px 间距×3 +
    单元格内边距 16 = 94，`storageOpsWidth` 移动端一律 96）；②「未登录：请点击钥匙按钮登录」长
    状态 tag 实宽 172 撑出 64px 列 → Admin.vue 移动端 :deep 给 `.el-table .el-tag` max-width:100%
    + `__content` min-width:0+ellipsis（完整文案桌面端看）。
  - **诊断器保留**（用户真机复测用，确认修好后下个会话删）：`?debug` 卡顿计（pointerdown 清零、
    RAF 帧间隔取最大≈本次点击卡了多久）+ `?no-blur/?no-shadow/?no-anim/?no-radius` 二分开关
    （main.js + glass.css；本次补了缺失的 no-radius 规则，原来是空操作会误导二分）。
  - 回归：mobile-check **37/37** + detail-check **15/15**（含「封面莫奈取色染上玻璃」证明延迟取色
    不破断言）零控制台 JS 错误（detail-check 一个 thumb 资源 404 已知良性）；npm run build +
    go build 重嵌 webvid.exe 与 webvid-linux-amd64/arm64（`-trimpath -ldflags "-s -w"` 同 release
    流水线）。**部署注意：上次会话的 linux 二进制(02:46)比 glass.css 终稿(02:58)旧，用户若测的是它
    等于没测到修复——本次三产物均为终稿后编译。**
  - 环境坑（本次踩）：①开发库 admin 密码被上次 reset-password e2e 改掉 →
    `./webvid.exe reset-password admin123` 恢复；②开发库是 07-09 重建的精简库，files/ 无照片、
    无「电影」目录 → 补 files/电影/（5 样片副本）+ files/照片/（6 张 _shots 截图副本）+
    POST /api/admin/index/rebuild，photos/detail 检查环境已恢复；③/test telegram 存储「未登录」
    初始化失败属预期（等用户手机验证码联调）。
  - **待用户 iPhone 真机复测**：部署新二进制后测两处弹窗。若仍卡：带 `?debug` 读卡顿计毫秒数，
    再逐个叠 `&no-shadow`/`&no-anim` 二分；若「编辑用户」独卡 → 备选方案=密码框改点按后再渲染。
- 2026-07-09 反馈#34「传输任务监控 + 只清已成功、单条删除」：#32 的传输任务抽屉本已完整
  （TasksDrawer.vue：1.5s 轮询 /api/tasks，逐条显示状态标签/进度条/已传·总·速度·当前文件、
  失败显错误、running/pending 可取消、error/canceled 可重试，工具栏🚚按钮带活动数角标）。
  用户点：原「清除已结束」一键把 done+error+canceled **全部**删掉，与「重试失败」冲突
  （清列表=失败任务也没了）。改为**只清已成功**：
  - 后端 task.go：`ClearDone` 收窄为只删 `StateDone`（失败/取消刻意保留便于重试）；
    新增 `Remove(id,owner,isAdmin)` 删单个终态任务（running/pending 返回 ErrBadState 须先取消，
    带 find() 权限校验）。handler_task.go 加 `taskRemove` + router `POST /tasks/:id/remove`
    （用 POST 而非 DELETE /tasks/:id，避免与静态 DELETE /tasks/done 在 gin DELETE 树冲突）。
  - 前端 TasksDrawer.vue：底部「清除已结束」→「清除已成功」（仍打 DELETE /tasks/done，语义已收窄）；
    每条终态任务（done/error/canceled）加「删除」按钮 → POST /tasks/:id/remove，可单独移除
    不想重试的失败/取消任务。
  - 回归：`go test ./internal/task/ ./internal/server/` 全绿（TestClearDone 扩：加失败任务断言其
    「清除已成功」后仍在；新增 TestRemove：失败可删/重复删 ErrNotFound/他人 ErrForbidden/运行中
    ErrBadState）；go vet 净；npm run build + go build 重嵌 webvid.exe（已核 dist 含「清除已成功」
    与 /tasks/${id}/remove）。e2e-check/offline-check 直打 DELETE /tasks/done 仍有效、mobile-check
    按🚚 title 定位抽屉不受影响（本环境无跑服，未跑三 mjs）。
- 2026-07-09 反馈#32「AList 式任务设置：复制/离线下载线程数 + 离线下载功能 + 下载/上传/复制限速」：
  - **task.Manager 分组队列+动态线程数**：GroupCopy/GroupOffline 各自独立队列与 worker 池，
    Task.Group 进 JSON；SetWorkers(group,n) 运行时扩缩（钳 1..32）——每 worker 一个 quit chan、
    worker 循环双 select **先查 quit 再取队列**（否则队列积压时收缩不生效）；收缩只关多余
    worker、不打断在跑任务；New(copyWorkers) 保旧签名，Submit 走 copy 组、SubmitIn 指定组、
    Retry 按 t.Group 回原队列。
  - **internal/limiter 全站限速**：令牌桶允许透支（tokens 减成负→按欠额睡）、闲置累积上限 1s
    配额、SetKBps 热调（对在途流立即生效）、ctx 取消打断等待；Reader/Writer 限速时按 64KB
    分块。三个限速器 server.New 内建：limUp 包 /fs/upload 请求体、limCopy 经 fs.SetCopyLimiter
    注入 transfer.copyOne 源流、limDown 包 /raw 下行+离线下载拉流。**坑①**：包
    http.ResponseWriter 必须只内嵌接口（limitedResponseWriter），刻意不提升底层 ReadFrom——
    否则 io.Copy 走 sendfile 直通绕过限速 Write。**坑②**：回环请求豁免下载限速（本机
    ffprobe/HLS/预载都走 127.0.0.1 的 /raw 回环，不豁免会把自己的转码探测限死）。
  - **离线下载** POST /api/fs/offline {urls[],dst_dir}（CanWrite 组）：仅 http/https，逐 URL 建
    offline 组任务：拉流→fs.Put(不覆盖)→index.Upsert；文件名 Content-Disposition 优先、次 URL
    末段（sanitize 去分隔符/控制符，兜底 download.bin）；源无 Content-Length 时完成后
    SetTotal(实收字节)——否则 run() 收尾 Done=Total 会把进度归零。前端 Files 工具栏
    「离线下载」钮（caps.upload 才显示）+弹窗（textarea 每行一 URL，显示目标目录），
    提交成功自动开传输任务抽屉。
  - **任务设置持久化+热生效**：settings 表 6 键 copy/offline/upload_workers +
    copy/upload/download_speed_kb（KB/s，0 不限），conf 钳位 getter；PUT /admin/settings
    指针字段缺省=保持原值，保存即 SetWorkers+SetKBps 热应用（免重启）；main.go
    task.New(cf.CopyWorkers())；/public/settings 透出 upload_workers → stores/app →
    UploadDrawer 并发替换写死的 CONCURRENCY=2；Admin.vue 站点设置卡加「任务设置」
    「速度限制」两组表单（el-input-number + field-help 说明）。
  - 坑③：server 集成测试挂 local 驱动时 os.OpenRoot 持根目录句柄挡 Windows TempDir 清理
    → t.Cleanup 里 DELETE storages + f.Reload 触发 Drop 释放；坑④：check 脚本后台路由是
    /@admin 不是 /admin（守卫静默重定向回首页，.el-tabs 等不到超时）。
  - 验证：go test 全绿（task 分组/扩缩 2 项、limiter 4 项、offline+设置热生效集成 2 项）；
    新增 frontend/offline-check.mjs **19 项**（表单 6 字段/保存热生效 API 读回/还原/离线下载
    全链路落盘校验+清理）；mobile-check 37 项回归全绿；npm build + 重编 webvid.exe 重启实测。
- 2026-07-09 新驱动「Telegram 收藏夹」（需求：TG 视频转发到收藏夹→本站复制到 OneDrive=离线下载）：
  只读驱动 internal/driver/telegram（gotd/td v0.160.0 MTProto **用户账号**，Bot 读不到收藏夹且
  Bot API 无拉历史接口）。收藏夹平铺成文件：`messages.Search`(InputPeerSelf+FilterDocument)
  分页拉全量、2min 缓存（防索引重建反复打 API），命名 `<消息ID>_<清洗文件名>`（O(1) 反解析，
  无名按 mime 兜底 file_日期.mp4）；Stat/Link 走 `messages.getMessages` 按 ID 单取（file_reference
  最新鲜）。下载=自实现 io.ReadSeekCloser（reader.go）：512KiB 块 `upload.getFile`（4096 对齐+
  整除 1MiB+不跨 1MiB 边界三约束一次满足）、Seek 纯指针运算（ServeContent 先 SeekEnd 探大小
  不打网络）；docSource 兜 FILE_MIGRATE_x（换 DC 重试并记住，client.DC 建异地池——**池不经
  客户端中间件，FLOOD_WAIT 要手动包 floodwait.SimpleWaiter.Handle**）与 FILE_REFERENCE 过期
  （重取消息刷新重试）。会话持久化走 ConfigPersister（同 pikpak refresh_token 套路）：
  sessionStore 桥接 gotd session.Storage↔cfg["session"](base64)。登录流程（收藏夹必须用户账号=
  手机号+验证码+可选两步密码）：LoginManager 在 send_code/sign_in 两个 HTTP 请求间**保持同一
  bg.Connect 连接**（phone_code_hash 绑定会话，5min 过期）；POST /api/admin/telegram/:id/
  {send_code,sign_in}，sign_in 遇 SESSION_PASSWORD_NEEDED 回 {need_password:true} 前端补密码框，
  成功把 session 写回 storages.config 并 afterStorageChange 重载。Admin.vue：telegram 行操作列
  多🔑按钮开登录弹窗（有 tg 存储时操作列 170→200/移动 100→124 防 #29 的表格内溢出复发）；
  配置表单零改动（FieldSpec 动态渲染）。**字段名坑：网络代理字段叫 `socks5` 不能叫 `proxy`**
  （与 CommonRemoteFields 服务器中转开关撞名）；Remote:false（无直链，Link.Local 恒走
  ServeContent/转存流，media 的 ffprobe/HLS 自动走 /api/raw 回环无需适配）。
  验证：单测 telegram_test.go 10 项（命名映射回环/reader 对齐+跨块+SeekEnd 零网络+取消/
  FILE_MIGRATE 换 DC 记忆/引用过期刷新/分页翻页+防死循环/非文档消息跳过），go vet+全套 go test 过、
  npm run build 过；**真实账号联调待用户提供 api_id/api_hash（my.telegram.org）+手机号+SOCKS5 代理**：
  添加存储→状态「未登录」→🔑发码→登录→文件页复制到 OneDrive 走既有跨存储转存任务。
  已知边界：单流下载 ~1-2MB/s（转存后台任务可接受，提速留 reader 内并发预取）；TG Photo
  类型（非文件图片）不映射；show_video 默认开会触发预载对每个视频回环 ffprobe（偏慢，可关）。
- 2026-07-08 反馈#29「移动端后台管理 UI 有的显示不全需要滑动」：后台存储/用户**表格列宽写死**
  （存储 4 列 min-160+110+140+170=580px、用户 6 列合计 640px），390px 窄屏内 el-table 内部横向
  溢出——页面级无溢出（mobile-check 老断言据此仍绿），但**操作列（编辑/重载/删除）被挤出屏外**，
  必须在表格里横向滑动才能点。另 el-tabs 头 4 项 344px 略超 336px 触发左右滚动箭头、「索引管理」
  被切。修复照抄 Files.vue 的 `isMobile` 列显隐模式（Admin.vue）：①两表移动端缩列宽，存储保留
  4 列全进屏（驱动 82 让「OneDrive」不断词换行）、用户**隐可见根路径+可写**（移交编辑弹窗查看，
  与 Files 隐修改时间同思路），角色列 72 防「管理员」标签被 `.cell` 省略号截成「管理员..」；
  ②scoped `@media(max-width:768px)` 收 `.el-tabs__item` padding 0 13px+font 13px（nav 344→286）、
  `.el-table .cell` padding 0 8px、操作列相邻按钮 margin-left 2px（默认 12px 过宽）。
  **坑：mobile-check 老「后台页无横向溢出」只测 documentElement.scrollWidth，el-table 内部溢出照样
  绿**——本轮给 mobile-check 补 3 断言（Tab 头 nav≤wrap、存储/用户表 table.scrollWidth≤clientWidth）。
  顺手修了 mobile-check 两处早坏（反馈#28 已察觉未修）：播放按钮 `has-text("立即播放")` 在有续播进度
  视频上失配→改用稳定类 `.vdc-play`（#25 文案继续观看/立即播放）；播放页沉浸模式隐顶栏后直接点
  `.user-chip` 进后台点不到→先 goto /library/video 让 user-chip 复现。回归 frontend/mobile-check.mjs
  **26→37 项全绿、零控制台错误**；截图 _shots/diag-admin-{storage,users}.png（前后对比：操作列由屏外→屏内）。
- 2026-07-08 反馈#28「移动端后台管理二级卡片（弹窗）的字体排列有问题」：EP el-form 默认
  label-position=right，字数不一的中文标签右缘齐、左缘参差（「角色」「启用」贴右、「可见根路径」
  伸左），窄屏尤显乱。纯 CSS 修复——glass.css `@media (max-width:768px)` 内改 `.el-form-item__label`
  **左对齐**：`justify-content: flex-start !important`（EP 标签是 flex，改 justify-content 即可）。
  所有标签从同一左缘起排、右侧留白，文字保持自然。**踩坑/返工：起初用两端对齐
  `text-align:justify; text-align-last:justify`（须先 `display:block` 破 EP 的 flex 才生效），但会在
  「角色」「启用」等短标签的字与字之间插入空隙，用户明确反对「不要在字体里面加空格」→ 改左对齐。**
  已核 admin 用户弹窗：6 标签左缘齐（left=29），「用户名」渲染宽 42px＝自然三字宽（若被 justify 撑开
  会到 ~98px），确认无字间距。全站移动端表单共用此规则（编辑用户/添加存储/站点设置）。注：本轮
  mobile-check 卡在播放页——反馈#29 已查明是 #25 续播按钮文案过期（现已改用 `.vdc-play` 类），与本改无关。
- 2026-07-08 反馈#26「照片墙的最近查看也加『查看更多』」：与反馈#24 对称的纯前端改动
  （LibraryPhotos.vue）——加第四态 `?viewed=1` 视图（viewedView 冻结取值 computed、isHome 加
  `!viewedView`、watch 依赖加 viewedView）；loadMore 命中 viewedView 改打
  `/media/history?kind=image&limit=50`（一次取满不分页）；「最近查看」货架头加「查看更多」按钮
  → push `{query:{viewed:'1'}}`，大标题「最近查看」隐排序/横幅/货架、空态专用文案；网格复用纯图墙
  `.photo-grid .cell`（无字幕，点开灯箱）。**吸取反馈#24 教训：按钮直接用独立类 `.see-more`
  不复用 `.see-all`**（photos-check.mjs 断言 `.see-all` count===1，撞类会破）。验证
  frontend/photos-history-check.mjs（新，留库 11 项）+ photos-check 19、e2e 14 全绿、零控制台错误；
  截图 _shots/17-photos-viewed.png。（本轮服务器由并行会话反复 rebuild/restart，我的源码改动落盘
  后其构建自动带上，served LibraryPhotos chunk 含 viewed 已核）
- 2026-07-08 反馈#25「增加播放进度功能」＝断点续播 + 进度展示：
  - **后端**（handler_media.go / db.go / router.go）：play_history 表加 `position`/`duration`
    两列（db.go migrate 尾部 `ALTER TABLE ADD COLUMN` 补旧库，新库重复列失败忽略）；
    POST /media/played 收 `{position,duration}`——**接近片尾（≥95% 或剩余 ≤5s）position 归零**
    （下次从头）；**duration 仅在 >0 时更新**（播放器 ready 早于元数据会上报 dur=0，
    不能覆盖已知时长否则进度条画不出，用 `duration=CASE WHEN excluded.duration>0 THEN
    excluded.duration ELSE play_history.duration END`）；新增 GET /media/progress?path=
    读单文件续播位置（无记录返 0/0，播放页起播定位用）；/media/history 带回 position/duration
  - **Play.vue**：起播前 GET /media/progress 取续播点（与 /video/info 并行不拖慢），
    `resumeAt>0` 时——hls.js 用 `new Hls({startPosition:resumeAt})`、direct/Safari 原生走
    `art.currentTime=resumeAt`（在 `ready` 事件里）；上报点＝ready(起播记最近播放)+
    **video:loadedmetadata(补 duration)**+每 10s 定时+pause/seeked/ended(播完报满进度→后端归零)+
    onBeforeUnmount(SPA 返回时最后一次)；`?restart=1`（详情卡「从头播放」）跳过续播定位
  - **进度条 UI**：LibraryVideo 最近播放货架/`?played=1` 网格卡片缩略图底沿加 `.prog-bar`
    （`pct(v)=position/duration`，0 不画）+ 副文本「看到 x% · 时间」；VideoDetailCard 打开时
    GET /media/progress 回填，大图底 `.vdc-prog` 进度条 + 「观看进度」信息行 +
    主按钮据进度显示「继续观看／立即播放」+ 有进度时多一个「从头播放」（→ restart=1）
  - **踩坑**：①直连分支加 `await progP` 拓宽了「异步期间快速离页 → artRef 变 null 仍
    new Artplayer」竞态 → `mount()` 里 `await nextTick()` 后加 `if(!artRef.value) return`
    卸载守卫（detail-check 曾报 ArtPlayerError container 无效）。②去掉 report 的整秒去重——
    它会挡掉 loadedmetadata 的补报（ready(0) 后 loadedmetadata 仍是 sec=0 被跳过 → duration 存不进）；
    改由各调用点自身节流（timeupdate 10s 定时，其余生命周期事件低频）
  - 验证：单测 media_progress_test.go（上报→读回→history 带回→duration=0 不覆盖→片尾归零→
    图片无进度→不存在 404）+ db_test 迁移；frontend/progress-check.mjs（新，留库 15 项）——
    direct mp4/HLS .ts「播放→seek→离开→重进从原位续播」、最近播放货架进度条宽度>0、
    详情卡继续观看/从头播放/进度行、从头播放不跳续播点；hls-check 30/detail-check 15/
    e2e 14 张全过（照片页 OneDrive 缩略图 429 限流属真实环境已知基线）
- 2026-07-08 反馈#24「最近播放加『查看更多』，显示最近播放的 50 个视频」：纯前端，
  复用现有 `?dir=`/`?all=1` 三态视图机制加第四态 `?played=1`（LibraryVideo.vue）——
  - `playedView` 冻结取值 computed（同 dir/all，keep-alive 驻留期切走 query 变空不误触发
    重置）；`isHome` 加 `!playedView` 条件；watch 依赖加 playedView；loadMore 命中
    playedView 时改打 `/media/history?kind=video&limit=50`（后端 limit 上限 100，一次取满
    不分页 hasMore=false）；大标题「最近播放」、隐藏排序下拉与 Featured/货架、空态专用文案
  - 「最近播放」货架头加「查看更多」按钮 → push `{query:{played:'1'}}`；网格副文本 playedView
    时显示 subText（含断点续播「看到 x%」，与并行的续播特性天然合流）
  - **踩坑：查看更多按钮初版复用 `.see-all` 类**，与「所有视频·查看全部」撞类 → scroll-check
    `page.click('.see-all')` 命中 DOM 里更靠前的它、跳 played 而非 all 视图导致断言超时。
    改独立类 `.see-more`（CSS `.see-all,.see-more` 共享样式）解决；后续新增货架入口按钮务必用
    独立类，勿复用 `.see-all`（scroll-check/photos-check 都按该类唯一定位）
  - 验证 frontend/history-check.mjs（新，留库 12 项）：先 POST /media/played 预置历史 →
    货架按钮存在/点击进 played=1/大标题/无排序下拉·横幅·货架/网格条数=接口且 ≤50/副文本以
    「播放」结尾/卡片弹详情/返回键回主页/深链直达/播放后返回仍在视图；截图 _shots/16-played-view。
    scroll-check 18/18、detail-check 15/15、e2e 14 张全绿（本轮与并行「断点续播」特性合并部署，
    server 二进制含两者，e2e 零控制台错误）
- 2026-07-08 反馈#23「.ts 文件还是无法播放」：mpegts 里的 AAC 是 ADTS 帧，event remux
  `-c:a copy` 进 fMP4 首个音频包即被拒（Malformed AAC bitstream），ffmpeg 死后 hls muxer
  照样 finalize 出 0.28s 截断片+ENDLIST 的"合法"列表 → 前端拿到列表却播不动。
  修复=copy+aac 恒挂 `-bsf:a aac_adtstoasc`（ASC 源直通无害）+ media_info 加 audio_aac 列
  （迁移清旧缓存防复发）。明细/负向验证/回归见 M10 段「踩坑#2」；hls-check.mjs 26→30 项
  （新样片 转播录像.ts 已经 upload API 入索引）；vod 档不受影响（音频恒转 aac 无 ADTS 问题）
- 2026-07-08 反馈#22「桌面端 UI 已差不多，针对移动端进行 UI 优化」：全站 ≤768px 响应式适配——
  - **导航重构**：App.vue 移动端隐藏顶栏 `.nav`，新增底部玻璃 Tab 栏（视频库/照片墙/文件/搜索，
    图标+文字、active 高亮、`env(safe-area-inset-bottom)` 刘海屏留白）；顶栏压到 48px 只留
    brand+用户链；后台管理入口仍在用户下拉。glass.css `.page` 移动档 padding
    `68px 14px calc(88px + safe-area)` 给 Tab 栏让位
  - **新增 `utils/viewport.js`**：`isMobile` 响应式 ref（matchMedia 768px，与 CSS 断点同源），
    只给纯 CSS 够不着的地方用——el-carousel 高度 prop（420px→240px）、Files 表格列显隐
  - 视频库/照片墙：Featured 标题 34→20px、info 内边距收紧；货架卡 264→190px（照片 188→140px）；
    视频网格固定 2 列、照片网格 3 列；lib-title 28→22px
  - Files：工具栏 flex-wrap 换行（选中批量操作时第二行）；表格隐藏「修改时间」列、
    大小/操作列收窄（总宽恰好 ≤390px 视口）
  - Search：搜索条 flex-wrap，输入框独占一行；结果行大小/时间折到名称下方第二行
  - 弹窗/抽屉全局收窄（glass.css 移动档，`!important` 压过行内 width）：el-dialog/el-message-box
    `max-width: calc(100vw-24px)`；el-drawer 统一 `width: calc(100vw-24px)`（420px 定宽超屏、
    TextDrawer 60% 太窄，移动端一律近全宽）；VideoDetailCard 信息区收紧、双按钮 flex:1 均分；
    Login 卡片 `min(380px,100%)`；Admin tabs/index-card 移动档
  - **触屏交互修复（真 bug）**：`.v-card` 中央的悬停播放图标 `.play` opacity:0 但仍参与命中测试，
    触屏点卡片中心会被它拦截直接进播放页、点边缘却弹详情（行为随落点漂移且 mobile-check
    首跑就撞上）。修复=`@media (hover: none)` 整个 display:none——触屏统一「点卡片=详情，
    详情里立即播放」，桌面悬停行为不变（detail-check 第 5 步直达语义仍过）
  - 验证：**frontend/mobile-check.mjs（新，留库）26/26**（390×844 isMobile+hasTouch：登录/
    Tab 栏可见与切换/各页无横向溢出/轮播 240/网格 2·3 列/时间列隐藏/批量按钮换行/任务抽屉与
    存储弹窗收进屏幕/灯箱开合/播放器挂载/搜索条换行；截图 _shots/m01~m08）；控制台零错误
    （thumb 404/500 与 ERR_ABORTED 为云端瞬时+翻页中断已知良性，脚本按 URL 归类）；
    桌面回归全绿：e2e 14 张 / scroll-check 18/18 / detail-check 15/15 / photos-check 19/19
    （photos 首跑失败为 OneDrive 缩略图 429 限流风暴瞬时导航超时，风暴过后复跑全过）
  - 注意：el-dialog/el-drawer 的 width 是行内样式，媒体查询覆盖必须 `!important`；
    EP 按需样式 chunk 加载晚于 glass.css，同权重规则会被级联盖掉（message-box 亦加 !important）
- 2026-07-08 反馈#21「挂载云盘勾选在视频库/照片墙展示后，后台预载封面与源信息并储存」：
  - **新表 media_info**（db.go）：视频 ffprobe 决策持久缓存——path 主键 +
    size/modified（RFC3339 秒级，与 files.modified 同构）失效判定 +
    video_copy/audio_copy/audio_aac/has_video/has_audio/duration/probed_at；
    旧库补列 ALTER audio_aac 成功（=首次升级）顺带清表强制重探（并行会话的 ADTS 修复合入）
  - **media 包**：`Decide` 三级缓存——内存(路径+size+mtime) → media_info 持久层 →
    现场 ffprobe 后**双写回内存+库**（media.New 增 db 参数，nil=仅内存供测试）；
    新增 `Info()`（info.go）收拢 direct/hls/unsupported 三档判定（directPlayExts 从
    server 迁入，导出 `IsDirectExt`），handler_video.go videoInfo 瘦身为 fs.Get+Info。
    效果：云盘视频重启后再开详情卡/播放页，策略+时长免现场探测秒回
  - **新包 internal/preload**：后台预载服务——`collect()` 查 files 表全部视频/图片，
    按**最长前缀归属挂载**（Mounts() 本身降序）用 `MediaVisible` 过滤
    （视频→show_video / 图片→show_photo，关开关即不预载该盘对应类型）；
    4 worker 并发 `process()`：thumbs.Get 预热封面（远端下载落盘/本地生成，已缓存秒过）+
    非 direct 视频 media.Decide 探测入库；代际 gen 取消旧轮、Progress 快照（done/covers/
    probes/current）；幂等可随时重跑
  - **触发接线**：index.Builder 加 `OnComplete` 钩子（全量重建成功才回调）→ main.go
    `idx.OnComplete(pl.Run)`——存储增删改/勾选开关保存 → Reload → Rebuild → 完成即预载；
    启动时索引已存在则直接 `pl.Run()` 补漏；**main 改 net.Listen 先绑端口再触发预载**
    （云盘探测走本机 /api/raw 回环，必须已在监听）
  - **API**：GET /admin/preload/progress + POST /admin/preload/run（server.New 增 pl 参数）；
    Admin.vue 索引管理页签改双卡片——「文件索引」+「封面与源信息预载」（百分比进度条/
    当前路径/封面·源信息计数/重新预载按钮），轮询与索引进度合一
  - 测试：media/info_test.go（持久层读写/size·modified 失效/覆盖写/db=nil 空操作/
    真 ffprobe 探测→新会话持久层命中）；preload/preload_test.go（collect 可见性三态/
    图片封面预热落盘/mkv 探测入库+mp4 direct 不入库，无 ffmpeg 自动 Skip）；
    坑：测试 Cleanup 必须 d.Close()，否则 Windows 下 newlist.db 占用删不掉 TempDir
  - 实测：隔离库（本地 23 文件）启动自动预载 19 项封面+6 探测；重启 probed_at 不变
    （持久缓存生效不重探）；开关三态验证（全开 19/全关 0/只开视频库 11，恢复秒完成）；
    /video/info mkv=remux·avi=transcode 带真实时长、mp4=direct；
    **真实库 44324 项**（OneDrive /test）自动开跑，封面持续落盘（sharepoint 偶发
    429/406 单文件失败容错继续，下轮重试）；e2e 14 张、hls-check 26/26、
    detail-check 15/15 全过，Admin 截图 _shots/11-admin-index.png 见双卡片
  - 已知限制（v1）：无缩略图/探测失败的文件不做负缓存，每轮预载会重试一次（好处=瞬时
    错误自愈，代价=每轮对失败文件各打一次 API）；重命名/移动后按新路径重新下载/重探
    （封面键=逻辑路径哈希、media_info 键=path）；预载不删 thumbs 孤儿文件
- 2026-07-08 反馈#20「照片墙的最近添加改成最近查看」：复用视频「最近播放」的 play_history
  机制——**后端本就 kind 无关，零改动**：POST /media/played 校验 `ext_type IN ('video','image')`
  接受照片，GET /media/history?kind=image 早已按 kind 过滤（handler_media.go）
  - utils/lightbox.js 集中上报查看：openLightbox 内 `pswp.on('change')` + init 后各调一次
    `reportView(paths[idx])`；防抖 600ms（快速翻页只记停留那张，不逐张刷屏）；
    `http.post('/media/played',…,{silent:true})`。照片墙 & 文件管理灯箱都计入查看历史
  - api/http.js 加 `silent` 选项：响应拦截器 `else if (!err.config?.silent)` 才弹 ElMessage，
    供 fire-and-forget 上报失败静默（照片不在媒体索引时 /media/played 返 404 也不打扰）；
    Play.vue 播放上报同步改 `{silent:true}`
  - LibraryPhotos.vue：`recent`→`viewed` ref；loadStatic 第二请求由 /media/list?sort=modified
    改 /media/history?kind=image&limit=12；货架标题「最近添加」→「最近查看」、副标题
    `formatTime(played_at)+"查看"`、点击 openList(viewed,…)
  - **行为说明**（同视频最近播放）：查看历史为空时货架不显示（v-if viewed.length）；
    不 live-refresh（loadStatic 仅 watch([dir,all]) 触发，看完照片需重进/刷新主页才更新货架）
  - 验证：photos-check.mjs 改为先点开网格照片记查看历史→重进主页断言「最近查看」货架标题+
    卡片出现（19/19 全过，控制台/pageerror 零错误，requestfailed 全为翻页中断 ERR_ABORTED 良性）
- 2026-07-08 反馈#19「每次打开视频返回时都会退回到最上方」：列表页滚动位置丢失——
  返回时 router 重建组件、watch 重跑 loadMore(true) 清空重拉、window.scrollTo(0) 归零。
  - router/index.js 加 `scrollBehavior`：后退/前进 `return savedPosition`，否则新导航回顶
  - App.vue `<router-view>` 包 `<keep-alive :include="['LibraryVideo','LibraryPhotos','Files','Search']">`，
    四个列表页驻留内存——返回时数据、DOM、滚动高度原样保留，配合 savedPosition 秒回原位；
    四页均加 `defineOptions({ name })` 供 include 精确匹配
  - **坑：keep-alive 驻留期路由切走（进 /play 或 /files 深链）时 route.query/params 会变空**，
    原 `computed(() => route.query.dir)` 会读到空值触发 watch([dir,all]) 重置列表+滚动归零。
    修复=用带 prev 参数的 computed 冻结取值：仅当 `route.path===本页路径` 时读新值，否则保留旧值
    （LibraryVideo/Photos 冻 dir/all，Files 冻 current）——切走不动、返回不重拉
  - **坑：body.infuse-mode 类原挂在 onMounted/onUnmounted**，keep-alive 下不再卸载→切到别的页
    仍残留极光样式。改挂 `onActivated/onDeactivated`（随激活态增删），onMounted 只留 IO 观察器
  - **坑：teleport 弹窗（VideoDetailCard/TasksDrawer append-to-body）挂在 body 上不随组件树隐藏**，
    浏览器后退带着弹窗切走后遮罩残留（z-index 2010 display:block、body 锁滚 el-popup-parent--hidden）。
    `onDeactivated` 关不掉（触发太晚，此时组件已被 keep-alive 冻结，teleport DOM 不再刷新）→
    改用 vue-router `onBeforeRouteLeave(() => visible=false)`：在冻结前触发、仍可正常收起遮罩
  - 验证：frontend/scroll-check.mjs（新，留库）18/18 断言+控制台零错误——主页/查看全部/文件页
    三处「深滚→播放→返回」滚动位置保持(±50px)且不重拉(media/list、fs/list 请求数不变)、
    随机网格内容返回不变、返回按钮回主页仍归顶、开着详情卡片后退遮罩不残留、infuse-mode 随页增删；
    detail-check.mjs 15/15、e2e 14 张全过（thumb ERR_ABORTED 翻页中断良性）
- 2026-07-08 反馈#18「用户管理已可改密码，删除单独的修改密码功能」：
  - 前端：Admin.vue 站点设置页签移除「账号密码→修改密码」按钮+分隔线+弹窗挂载
    （Key 图标/pwDlg/import 同步清理）；删除 components/ChangePasswordDialog.vue
  - 后端：删 router.go `PUT /user/password` 路由与 handler_auth.go changePassword
    （users.UpdatePassword 保留——用户管理 handler_user.go 改密仍在用）
  - 改密路径现在只剩：后台·用户管理编辑用户填新密码（管理员操作）
- 2026-07-08 反馈#17「照片墙主页照片也像视频库一样随机展示，查看全部按序」：
  照抄反馈#12 同套改法——LibraryPhotos isHome 网格 sort=random（200 条每次进主页重抽）、
  主页区块头移除排序下拉只留「查看全部」；?all=1/?dir= 保持有序+排序下拉不变；
  photos-check.mjs 无排序下拉断言不用改
- 2026-07-08 反馈#16「视频库点击视频弹出二级卡片（缩略图+详细信息）」：
  - 新组件 `components/VideoDetailCard.vue`：el-dialog（append-to-body，壳样式须全局
    `<style>` 块——teleport 后 .el-dialog 元素不吃 scoped）；上半 16:9 大缩略图
    （thumbUrl 1200）+右上圆形关闭钮，下半标题/徽标行（扩展名+播放策略+时长+大小）/
    详情行（文件名/所在目录→点击跳文件管理/修改时间/最近播放来源加上次播放）/
    「立即播放」「文件位置」按钮；unsupported 显示后端降级文案
  - 打开时异步拉 /video/info 填策略徽标（direct=原生直连/remux=转封装/transcode=转码；
    seq 代际丢弃过期响应；本地文件秒回、云盘几秒内到）
  - LibraryVideo.vue：三处卡片（最近添加/最近播放/网格）点击改弹详情；**悬停播放图标
    @click.stop 仍直达播放页**；Featured 横幅点击弹详情、「立即播放」按钮 @click.stop
    直达（**e2e 04-play 依赖 .hero-btn 直达语义保留**）
  - 验证：frontend/detail-check.mjs（新，留库）12/12 断言+控制台零错误——弹出/缩略图/
    信息行/策略徽标/立即播放跳转/Escape 关闭/文件位置跳转/播放图标直达/横幅两种点击；
    截图 _shots/15-video-detail.png；全量 e2e 14 张过（requestfailed 全为翻页中断
    ERR_ABORTED 良性，云盘缩略图多时数量偏多属预期）
  - 附：本次 vite build 首跑遇 rollup 原生解析器对 photoswipe 偶发 panic
    （utf16_positions.rs unwrap None），重跑即过，非代码问题
  - **追加（2026-07-08）**：卡片壳从不透明 #14141d 改液态玻璃——background 用
    --glass-bg 半透明白、--glass-border 边框、::before 顶部高光线（同 .glass 签名）、
    @supports 无 backdrop-filter 回退深色；磨砂 blur 本就来自 glass.css 全局
    .el-dialog 规则（之前被不透明底色盖住看不出）。detail-check.mjs 复跑 14/14 过
    （含并行会话加的 keep-alive onDeactivated 关弹窗两项）；控制台仅剩随机抽中
    无封面视频的 thumb 404（数据依赖，fallback 图标兜底，良性）
  - **莫奈取色（2026-07-08）**：信息区玻璃按封面动态染色（Material You 风格）——
    onArtLoad 在封面 `<img>` 加载后 canvas 缩样到 24×24 → 5-bit/通道量化分桶 →
    跳过近黑(<24)/近白(>235)像素 → 主导色打分 `n*(1+饱和/n/64)`（占比为主+饱和度加成
    防灰底压过主体）→ vibrant() 转 HSL 抬饱和 s≥0.42、钳亮度 l∈[0.34,0.58] → 半透明
    竖直渐变（顶 0.30 底 0.14 主色衰减到 0.55×）叠在信息区。**取色失败自动回退中性玻璃**
    （无封面/跨域 302 兜底 getImageData 抛错→catch→tint=null；缩略图同源不会跨域）；
    open() 重置 tint 防上一部残留；:key 切换后旧 img 迟到 load 用 isConnected 挡掉。
    纯 CSS/canvas 前端实现，无新依赖。detail-check.mjs 加「莫奈取色染上玻璃」断言
    （本地样片封面 waitForFunction 查 .vdc-info 内联 linear-gradient）15/15 过、
    控制台零错误；截图 _shots/15b-video-detail-tint.png
- 2026-07-07 反馈#15「视频点击跳转后播放器要等一会才加载出来」：三处串行延迟拆解——
  ① Play.vue await /video/info（对云盘 = 一次 Graph Stat）才挂播放器；
  ② /api/raw 每次 302 前都现调 Graph 拿 downloadUrl；③ 首次进播放页还要现下 Play chunk。
  - Play.vue：DIRECT_EXTS（与后端 directExts 同表，M10 后改由 /video/info 全权判定）
    命中则**进页立即挂 ArtPlayer**不等接口；其余扩展名仍走 /video/info（unsupported 文案）
  - LibraryVideo.vue onMounted：`import('./Play.vue')` 预热播放页 chunk（含 ArtPlayer 143KB）
  - onedrive.go：**直链缓存**——linkCache[relPath]{url,size,mod,exp}，TTL 10min
    （远低于 downloadUrl ~1h 有效期，M6 换链重取到的缓存链必然未过期，安全）；
    Rename/Remove/Move/Put 成功后整表清空；Link 内惰性建表（测试不经 Init 直接构造）
  - 测试：onedrive_test.go 新增 TestLinkCacheAndInvalidate（TTL 内命中不打 API/
    不同路径独立/Rename 后失效重取）；全套 go test ./internal/... 通过
  - 实测（frontend/play-timing.mjs 留库，真实 OneDrive mp4）：播放器挂载 1.6~2.2s →
    **0.7~0.9s**；二次播放元数据就绪 6.8s → **2.7s**（省 Graph 往返）；首次 6.8s 不变——
    大头是 sharepoint 拉 moov（源录制文件 moov 常在尾部，须多次 Range），属直链播放
    固有开销，待 M10 转码/faststart 处理；全量 e2e 14 张通过
- 2026-07-07 反馈#14「点击照片要等几秒才显示原图」：旧灯箱 init 前 `await probe(原图)`
  ——为测尺寸把整张原图先下载一遍，下载完才弹窗，PhotoSwipe 再加载第二遍
  （云盘 302 直链每次都变 → 缓存不命中，原图真下两遍；反馈#11 踩坑段早有记录）。
  - `frontend/src/utils/lightbox.js` 重写秒开策略：立即 init；首图 msrc 用列表页
    已缓存缩略图（PhotoSwipe 仅 isFirstSlide 用 msrc 当占位，正好覆盖点击场景）；
    占位尺寸 probe **缩略图**取比例×8（浏览器已缓存≈瞬时，250ms 超时兜底防变形）；
    真实尺寸在 `loadComplete` 事件从 `<img>.naturalWidth` 原地刷新
    （slide.width/height + content + data 同步改，`slide.resize()` 重算 zoomLevels，
    **不用 refreshSlideContent**——那会重建 content 再发一次原图请求）；
    原 change+probe 原图逻辑删除，全程零额外原图请求
  - `LibraryPhotos.vue`：openList 增加 msize 参数，hero=1200 / recent=480 / 网格默认 320，
    与各货架正在展示的缩略图同尺寸 → 占位图直接命中浏览器缓存；Files.vue 网格本就 320 免改
  - `handler_raw.go`：图片响应加 Cache-Control（本地/代理分支 private,max-age=86400；
    302 直链分支 private,max-age=600 同 thumb 兜底惯例——灯箱前后翻页重建 slide 会
    重复请求同一 URL）；新增 isImage() 按 contentTypeFor 前缀判断
  - 验证：photos-check.mjs 18 项全过；全量 e2e 14 张截图通过（仅翻页 ERR_ABORTED 与
    基线一致）；curl 实测 GET/HEAD raw 图片均带 Cache-Control: private,max-age=86400
- 2026-07-07 反馈#13「每次刷新都会重新加载封面」：远端盘缩略图无任何缓存——
  thumbHandler 对 Thumber 盘 302 直链，redirect 响应无缓存头且 sharepoint 目标 URL
  每次带新 tempauth → 浏览器缓存永远失效，服务端还每次都打一轮 Graph API。
  - `internal/thumb/thumb.go`：Thumber 分支改**下载落盘缓存**（remote()）——
    键 = sha1(逻辑路径+"|remote")（远端 Stat 是网络调用，mtime 不进键；忽略 size 参数，
    OneDrive 固定 large≈800px 一份多用）；TTL 30 天，过期刷新失败沿用旧文件；
    复用既有 singleflight；新增 dlSem(6) 下载并发（与 CPU 生成 sem(2) 分开）；
    download() = 30s 超时 + 浏览器 UA（防网盘校验）+ 20MB 上限 + tmp+rename；
    首次下载失败回退 302（至少显示一次），驱动无缩略图继续走本地分支→404 不变
  - `handler_video.go`：302 兜底分支加 Cache-Control private,max-age=600；
    本地文件分支原有 public,max-age=86400 + c.File 的 Last-Modified/304 不变，
    远端缓存文件走同一分支自动获得
  - 前端零改动（thumbUrl 的 token 来自 localStorage，刷新间稳定，URL 不变）
  - 实测：curl 首次 1.77s（Graph+下载落盘）→ 二次 52ms 磁盘直出、内容一致、
    If-Modified-Since → 304；playwright 照片墙首载 74 张全走网络(2.8MB) →
    reload 后 70 缓存/21 网络 → 再刷 87 缓存（增量为随机 Featured 新抽图+
    懒加载新进视口的图，属预期）；go test server 包通过
- 2026-07-07 反馈#12「视频库首页所有视频随机挑选，查看全部按序」：
  - LibraryVideo.vue loadMore()：isHome → sort=random（HOME_CAP=200 每次进首页重新抽，
    与 Featured 同款后端 ORDER BY RANDOM()）；?all=1 / ?dir= 视图保持 modified/name 排序不变
  - 首页「所有视频」区块头移除排序下拉（随机下无意义），只留「查看全部 ›」；
    全部/目录视图 lib-head 的排序下拉保留
  - 验证：playwright 断言首页无下拉/两次进入网格顺序不同/全部视图首屏与 API
    「最新在前」一致/控制台零错误（_shots/01e）；全量 e2e 14 张通过
- 2026-07-06 反馈#1「主页不要显示图片」：MediaHome 移除图片/相册板块（已被反馈#2 覆盖）。
- 2026-07-06 反馈#2「去掉首页，视频库/照片墙按首页风格」：
  - 删除 MediaHome.vue、MediaShelf.vue、home-check.mjs；glass.css 清理 .shelf-scroll
  - router：`/` → redirect `/library/video`（登录后落地视频库）；App.vue 导航去掉「首页」
  - LibraryVideo.vue：顶部加 Hero 轮播（最新 5 个视频，立即播放）+ 海报网格铺开（Apple TV 式），
    空态改 glass 引导卡；Hero 仅在非 ?dir= 视图显示；hero 排序用 new Date(modified)（ISO 字符串）
  - LibraryPhotos.vue：同款 Hero（最新 5 张，点击开灯箱「查看大图」）+ 照片网格，空态同上
  - e2e-check.mjs 重排：登录 waitForURL 改 /library/video，截图 01-library-video…11-admin-index，
    _shots/ 旧图已清空重生成；全流程通过，唯一 requestfailed 是离开播放页时视频流 ERR_ABORTED（良性）
- 2026-07-06 反馈#3「后台添加存储弹窗被裁剪卡在面板里」：Admin.vue 两个 el-dialog（存储/用户）
  加 append-to-body 修复（根因见踩坑记录 backdrop-filter 条）；e2e 已加弹窗回归截图验证通过
- 2026-07-07 反馈#4「视频库改成 Infuse iPad 版样式」：LibraryVideo.vue 整页重写——
  - 近黑影院背景：页面挂载时 body 加 `infuse-mode`，glass.css 把极光 span 压暗到 opacity 0.12（带过渡）
  - 顶部 Featured 大横幅轮播（kicker「最近添加」+大标题+立即播放，圆点指示器右下）；
    「最近添加」横向货架（12 个，隐藏滚动条）；「合集」货架（复用后端 /media/groups，
    此前前端弃用的接口重新启用，封面叠名称+数量，点击进 ?dir= 视图）；「所有视频」网格
  - 目录视图改 Infuse 资料库样式：圆形返回键+28px 大标题+排序在右
  - **顺带解除 200 上限**：网格 offset 翻页滚动加载（IntersectionObserver rootMargin 600px、
    每页 120、gen 代际计数丢弃 dir/sort 切换后的在途响应）；照片墙仍一次 500 未改
  - e2e：`.hero-btn` 类保留（04-play 步骤依赖）；全流程 14 张截图通过、无新增错误；
    另拍 01b（货架+网格）/01c（目录视图）两张参考图在 _shots/
- 2026-07-07 反馈#5「把合集改成最近播放」：新增服务端播放历史——
  - db.go 加 `play_history(user_id,path,played_at)` 表（PK=user_id+path，upsert 只留最新一次）
  - handler_media.go：POST /api/media/played {path}（NormPath+索引内可见媒体文件校验，
    否则 404）；GET /api/media/history?kind=&limit=（JOIN files，base_path 视野过滤内联在
    f.path 上——baseFilter 的裸 path 列在 JOIN 下有歧义；文件删/移后历史条目自然消失）
  - Play.vue：video/info 返回 direct 时 fire-and-forget 上报；LibraryVideo.vue「合集」货架
    整段替换为「最近播放」（卡片同「最近添加」，副文本=播放时间），/media/groups 前端再度弃用
  - 验证：node 冒烟（空历史→上报 200→出现→不存在路径 404→重复上报不重复）；
    playwright 播放后回库页货架正确按时间倒序（_shots/01d）；全量 e2e 通过；
    go test ./internal/server/ 通过
  - 注意：Windows 下运行中的 exe 被锁——先 go build -o newlist-new.exe，停服后 mv 替换再启
- 2026-07-07 反馈#6「主页所有视频默认 200 个，点查看全部看完整列表」：
  - LibraryVideo.vue 三态视图：isHome(!dir&&!all) / ?all=1 全部视图 / ?dir= 目录视图；
    主页网格一次拉 200（HOME_CAP）不再翻页，区块头加「查看全部 ›」（.see-all）；
    全部视图复用 lib-head 大标题（返回键+「所有视频」+排序）+ 120/页滚动加载；
    视图切换 window.scrollTo(0)
- 2026-07-07 反馈#10「存储加『在搜索中展示』开关」（复用反馈#8 机制）：
  - registry.go CommonFields 追加 show_search（缺省 true）；fs.go MediaVisible(kind) 加
    "search"→show_search 分支；handler_search.go fsSearch 在 base 过滤后追加
    `mediaVisFilter(sql,args,"search","path")`。关闭后该存储**全部内容**（含目录与挂载点行）
    不出现在搜索结果；三开关互相独立；文件管理不受影响；前端零改动（动态表单自动渲染）
  - media_vis_test.go 扩展：/隐藏盘 加 show_search=false → 搜 mp4 不含其文件但含嵌套
    可见子盘与可见盘；搜 jpg 验证 show_search 独立于 show_photo（照片墙有、搜索无）
  - 实测：schema 四驱动含新字段；关开关搜索立即排除、重建完成后对照
    「同批索引行：搜索无/视频库有/文件管理有」；恢复回基线；索引收尾无错、条数完整
  - **验证脚本踩坑**：存储 PUT 会先 DELETE files 再重扫——PUT 后立刻轮询可能看到
    「清空前的旧行」造成假阳性/假阴性，正确姿势是等 /admin/index/progress 结束再断言
- 2026-07-07 反馈#9「修改密码移入后台管理」：
  - App.vue：顶栏用户下拉移除「修改密码」项及 ChangePasswordDialog 挂载（下拉只剩后台管理/退出登录）
  - Admin.vue：站点设置页签内加「账号密码 → 修改密码」按钮 + ChangePasswordDialog（组件复用）
  - ChangePasswordDialog.vue：el-dialog 加 append-to-body（现在渲染于 .glass 容器内，
    不加会被 backdrop-filter 裁剪——踩坑记录已知问题）
  - 后端 PUT /user/password 不动（仍 authed 级）；**副作用：非管理员用户失去自助改密入口**，
    需要时管理员在用户管理里「重置密码」代改（用户知情选择）
  - 验证：playwright 断言下拉无该项/后台按钮在/弹窗完整弹出（含真实改密 admin123→admin456→
    改回，成功提示均出现）、控制台零错误；截图 _shots/12-admin-password-dialog.png；全量 e2e 回归通过
- 2026-07-07 反馈#8「挂载存储可控制内容是否进视频库/照片墙」：
  - registry.go 新增 CommonFields（show_video「在视频库展示」/ show_photo「在照片墙展示」，
    bool 缺省 true），Register 时追加给**所有**驱动（区别于仅远端的 CommonRemoteFields）；
    后台存储表单自动渲染，无需前端专门改动
  - fs.go 新增 `Mount.MediaVisible(kind)`：读 Cfg，键不存在视为 true（旧存储缺省展示）
  - handler_media.go 新增 `mediaVisFilter(sql,args,kind,col)`：按**文件归属挂载=最长前缀**
    生成 CASE WHEN 条件（fs.Mounts() 本身按路径长度降序，命中即最长前缀；嵌套可见挂载
    不被隐藏的父挂载牵连）；全部挂载可见时不追加条件（默认零开销）。
    应用于 mediaList / mediaHistory（f.path 列）/ mediaGroups；**查询期过滤，改开关即时生效
    无需等索引重建**（存储保存本来就会触发重建，属既有行为）。mediaPlayed 上报不拦（只影响展示）。
    文件管理 /fs/list 与搜索不受影响
  - Admin.vue openStorage 编辑旧存储时按驱动字段默认值补齐 config 缺失键
    （否则新增 bool 字段开关会错误显示为关）
  - 测试 media_vis_test.go（全链路 sqlite+local 驱动+索引重建+Router）：隐藏盘视频不出现/
    旧存储无键缺省展示/嵌套可见挂载(/隐藏盘/子盘)按最长前缀正常展示/show_photo 独立/
    最近播放同过滤/groups 同过滤/fs/list 不受影响。**坑**：server 测试二进制需
    `_ "newlist/internal/driver/local"`（生产由 main 引入）；local 驱动 os.OpenRoot 持有
    根目录句柄，Windows 下测试收尾要清 storages 再 Reload 让旧驱动 Drop，否则 TempDir 删不掉
  - 真实服务端到端实测通过：4 驱动 schema 含新字段；关 show_video 视频库**立即**排除本地存储
    （重建完成后仍正确）、照片墙不受影响、文件管理可列、最近播放同过滤；关 show_photo 照片墙
    排除；恢复后回到基线。注意：每次存储 PUT 触发全量重建，真实 OneDrive 4.3万条一轮约 7 分钟
- 2026-07-07 反馈#11「照片墙改成视频库那样的界面」：LibraryPhotos.vue 按 LibraryVideo.vue
  整页重写（结构/样式逐段对齐，含 infuse-mode 影院暗背景）：
  - 三态视图 isHome / ?all=1 / ?dir=；Featured 随机推荐横幅（limit=5&sort=random，
    点击开灯箱「查看大图」）+「最近添加」货架（12 张方形卡片，点击开灯箱）+
    「所有照片」网格（主页 HOME_CAP=200 + 「查看全部」；all/dir 视图 120/页
    IntersectionObserver 滚动加载——**顺带解除照片墙一次 500 的旧上限**）；
    lib-head 大标题（返回键+排序：最新在前/按名称）；灯箱按各自列表开
    （hero/recent/grid 独立传 paths+index）
  - 网格 cell 改 Infuse 画框样式（圆角 12px/深底/hover 放大+白描边），**类名保留
    `.photo-grid .cell`**（e2e 03-lightbox 步骤依赖）；货架卡片方形 188px 带名称+时间
  - 验证：photos-check.mjs（新增，仿 home-check 惯例留库）18 项断言全过——横幅 5 张/
    kicker/货架 12/网格 200 封顶/查看全部跳转/所有照片大标题/返回/目录视图（风景 5 张）/
    灯箱开合/infuse-mode 挂摘；all 视图实测 120→240→360 滚动加载（真实 OneDrive 几万张图）；
    全量 e2e 14 张截图通过、无 JS 错误
  - 踩坑：灯箱 init 前 probe 预载原图，OneDrive 走 302 原图下载慢——测试等 .pswp 要长超时；
    PhotoSwipe 开场动画期间 Escape 可能被忽略，关灯箱要重试至 .pswp 消失
- 2026-07-07 反馈#7「顶部大屏改随机展示」（追加：横屏过滤需求已撤回——封面
  object-fit:cover 竖屏也能铺满，且实际封面多为横图）：
  - 后端 /media/list 支持 sort=random（ORDER BY RANDOM()）；前端 Featured 改独立请求
    limit=5&sort=random，kicker 文案「最近添加」→「随机推荐」，每次回主页重新抽
  - 验证：random 连查三次顺序均不同；DOM 断言全过（hero=5/主页 200/查看全部跳转/
    all 视图 120→240 滚动加载/返回键/控制台零错误）；全量 e2e 14 张通过

