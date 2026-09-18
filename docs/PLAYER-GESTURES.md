# 播放手势：双击 ±10 秒 / 按住 2 倍速（2026-09-17）

用户反馈播放器不好用，要的是 YouTube 那套：长按倍速、双击左侧快退 10 秒、双击右侧快进 10 秒。
经确认，桌面端与触屏都要生效，倍速取 2x。

## 交付内容

| 文件 | 改动 |
|---|---|
| `frontend/src/utils/playerGestures.js` | 新增。手势全部逻辑 + 反馈层 DOM，导出 `attachPlayerGestures(art)` → detach 句柄，与 `attachMediaSession` 同构 |
| `frontend/src/pages/Play.vue` | mount 末尾 attach、teardown 摘除、`video:pause` 上报加防护 |
| `frontend/src/assets/player.css` | 播放器全部皮肤（控件玻璃胶囊、细进度条、手势反馈、移动端收窄）。**全局，不能放 scoped** |
| `frontend/gesture-check.mjs` | 新增。验收脚本，真 ArtPlayer + 真 video，桌面与触屏两条路径 |

## 手势规格

- **双击左 35% / 右 35%**：∓10 秒。连击累加（10→20→30），徽章实时显示累计值
- **中间 30%**：桌面双击全屏、触屏双击播放/暂停（各端原本的语义原样保留）
- **按住 500 ms**：进 2 倍速，顶部浮出提示条；松手回到按住前那一档（用户在设置里选的常驻倍速不受影响）
- 暂停时按住不加速；手指移动超 10 px 判为滑动，不算按住

## 关键决策与踩过的坑

### ArtPlayer 的点击语义必须让开，但不能硬拦

ArtPlayer 5.4 在 `$video` 上自己代理了 click，三个静态常量决定行为：

| 常量 | 默认 | 处理 |
|---|---|---|
| `MOBILE_CLICK_PLAY` | false | 保留。触屏单击只显隐控件，本来就与 YouTube 一致 |
| `MOBILE_DBCLICK_PLAY` | true | **关掉**。让开触屏双击 |
| `DBCLICK_FULLSCREEN` | true | **关掉**。全屏改由中间区自己做 |

桌面端单击那句 `art.toggle()` 是无条件的，且 `toggle` 由 `Object.defineProperty` 定义
（`writable`/`configurable` 均为 false），**赋值会抛 TypeError，改不掉**。

一度考虑在 `$player` 捕获阶段 `stopImmediatePropagation` 整个接管，查证后否掉：
`art.emit('click')` 还接着**移动端「点一下出控件」**（`artplayer.mjs:1485`）、右键菜单关闭
（`:1055`）、以及 document 级的 `isFocus`（热键依赖）。拦一次要补三处内部行为，升级即碎。

**最终做法**：让它照常 toggle，在 `queueMicrotask` 里原样撤销 —— 微任务跑在本轮所有 click
处理器之后（不依赖谁先注册）、浏览器绘制之前，用户看不到中间状态。真正的单击暂停推迟到
双击窗口（`DBCLICK_TIME` = 300 ms）结束后再补。这也正是 YouTube 桌面端单击暂停略有延迟的
原因：它同样要留出双击判定时间。撤销走公开的 `play()`/`pause()`，不碰任何内部状态。

### 自带的 fastForward 用不了

`option.fastForward` 只认触屏、固定 3 倍（`FAST_FORWARD_VALUE`）、要按满 1 秒，且全程无视觉
反馈 —— 松手前用户不知道自己进没进倍速。桌面端更是完全没有。所以自己实现。

### 撤销基准要实时读，否则中间区双击会被自己吃掉

`neutralize()` 若比较入口处捕获的播放状态快照，中间区双击那次 `setPlaying(!downPlaying)`
会在微任务里被当成「ArtPlayer 干的」撤销回去，功能静默失效。改为：`setPlaying()` 同步把
`downPlaying` 记成新基准，`neutralize()` 比较实时值，手势自己发起的切换便自动让位。
（此 bug 在动手写验收脚本前的代码复查中发现并修掉。）

### art.playing 带 readyState 条件，不能当播放状态用（验收逼出来的真 bug）

`art.playing` 不是 `!video.paused`，它是（`artplayer.mjs:3098`）：

```js
!!($video.currentTime > 0 && !$video.paused && !$video.ended && $video.readyState > 2)
```

刚 seek 完或缓冲中 `readyState` 会掉到 1~2，于是**视频明明在播，它却返回 false**。拿它当
「按下那一刻的播放状态」基准，基准一反，推迟执行的单击就把 `setPlaying(!downPlaying)` 算成
了 play —— 用户快进完立刻点暂停，视频不停反而继续播；长按倍速同样会被 `!art.playing` 拒掉。

**改为一律读 `video.paused`**（模块内 `playingNow()`）。它是纯粹的播放意图，不受缓冲影响。

### ArtPlayer 在 play/pause/playbackRate 的 setter 里硬塞了 notice

`art.pause()` 会 `notice.show = "暂停"`（`:2990`），`art.play()` 同理（`:3117`），
`art.playbackRate` 弹「速度: 2x」（`:3084`）。于是撤销那次 toggle 虽然功能正确，却会在左上角
闪出「暂停」「播放」，等于把好不容易藏住的中间状态又喊出来。

两手处理：手势自己的动作一律直接操作 `$video`（原生 play/pause/ratechange 事件照常派发，
ArtPlayer 的控件图标同步不受影响）；ArtPlayer 自己 toggle 弹的那句，在 `neutralize()` 的
微任务里 `art.notice.show = ''` 抹掉。顺带单击暂停也不再弹字 —— 中央那枚玻璃圆已经把状态
说清楚了，同 YouTube。

### 伪暂停不该上报进度

桌面端双击的撤销会留下一个「pause 事件到达时其实已经在播」的瞬间。`Play.vue` 的
`video:pause` 上报因此加了 `if (!art.playing)` 防护 —— 顺带把真暂停才上报这件事做对了。

### 磨砂只用 backdrop-filter

光晕是高频触发的动画，只用 `rgba` 底 + `opacity` 过渡，**不碰 `filter: blur`**（iOS 重光栅化
卡死的老雷区）。徽章与倍速提示条沿用项目现有的液态玻璃配方（`blur(7px) saturate(1.8)`）。

### 播放器样式必须全局：网页全屏会把播放器搬到 body（2026-09-18 修）

iPhone 上点网页全屏，手势层退化成裸文本堆在屏幕左上角，玻璃胶囊与细进度条一并消失，
退出全屏就恢复。根因在 `artplayer.mjs:2794`：

```js
if (constructor.FULLSCREEN_WEB_IN_BODY) {   // 默认 true
  append(document.body, $player);            // 把 .art-video-player 搬到 body
}
```

播放器一搬走，页面里的 `.player` 就不再是它的祖先，而 scoped 编译出的
`.player[data-v-xxx] xxx` 整批失配。红色进度条还在，是因为它来自 `theme` **选项**、
运行时内联写在元素上，不走 CSS —— 据此可判断影响范围：**CSS 定义的部分全丢**。

**修法**：样式整体搬到全局 `assets/player.css`，作用域写成 `.art-video-player.art-video-player`。

- **类名双写**是为了把特异性补回 `(0,2,0)`。只写一次是 `(0,1,0)`，与 ArtPlayer 运行时注入的
  同级规则打平，而它的 `<style>` 排在产物 CSS 之后就赢了，玻璃胶囊会被打回原样。
- **调变量那条写三遍**：ArtPlayer 在 `.art-video-player.art-mobile`（`(0,2,0)`）里也在调
  `--art-control-height`、`--art-bottom-gap`，双写与它打平就轮不到我们，手机上的控件高度
  会被压回 38px。三写等于迁移前 `.player[data-v] .art-video-player` 的 `(0,3,0)`。
- **不要改用 `FULLSCREEN_WEB_IN_BODY = false` 绕过**：`.player` 带着 `glass` 类，
  `backdrop-filter` 会给内部的 `position: fixed` 造出包含块，全屏铺不满；那样还得在全屏时
  额外摘掉磨砂，把问题换成一个更隐蔽的前提（祖先链上没有 transform/filter）。

顺带清掉了 `player.css` 里那 144 行死代码：它按 `.wv-player` 写，而这个类名在 DOM 里根本
不存在（历史上为「画中画跨页存活」做过的宿主架构，后来回退了，样式文件忘了删）。

### iPhone 长按进倍速时弹出放大镜：只有 touchstart preventDefault 能压住（2026-09-18 修）

长按播放器进 2 倍速的同时，iOS 浮出那枚椭圆放大镜，把倍速提示整个盖掉。

**这条走了三轮弯路，每一轮都是「看着像」而没有证据**：先猜是 `backdrop-filter` 做变换动画
时采样错位；再猜是播放器里的文字层没禁选（`-webkit-` 前缀那套）；又猜是 iOS 往播放器外面
找最近的可选文字（于是整页禁选）。**三条全错**，前两条改完真机照旧，第三条是靠探针证伪的。

最后靠 `frontend/select-probe.mjs`（局域网探针，手机直接打开，把计算样式与事件流打到屏幕上）
拿到事实：

| 探针读数 | 说明什么 |
|---|---|
| 播放器 / video / 标题 / 页面 四处 `user-select=none`、`callout=none` | CSS 全部生效，「属性没落上」这条排除 |
| `selectionchange 空选=true 选中="" @ <div.art-video-player …>` | **是折叠选区（光标），不是文字选区** —— 所以 `user-select` 管不着 |
| 逐个开关实测：拦 `selectstart` 无效、`html/body` 也禁选无效、`removeAllRanges` 无效 | 网页这侧只剩最后一扇门 |
| 拦 `touchstart` → 放大镜消失 | WebKit 起这套手势的唯一入口就在 touchstart 的默认行为里 |

**修法**（`playerGestures.js`）：在 `$video` 的 `touchstart` 上 `preventDefault()`，
单指才拦（多指缩放照常放行）。

代价是浏览器不再补发 `click`，而单击显隐控件、双击快进全挂在它上面 —— 所以在 `touchend`
里按「短按（< 400 ms）且没划开」自己派发一次 `MouseEvent('click')`，原来那条 click 链路
（ArtPlayer 的 + 我们的 `onClick`）一个字没改。派发在 touchend 处理器内同步完成，
仍在用户手势上下文里，`play()` 不会被 WebKit 拦。

这条 `touchend` 必须绑在 `$video` 上而不是 `document`：目标阶段先于 `document` 上的
`endPress` 跑，那时 `boosting` 还是 true，长按松手才不会被补成一次点击。

**顺带的影响**：手指从画面上起手不再能滚页面。播放页本就不滚，YouTube 移动端同样如此。

播放器根上那条禁选（`user-select` / `-webkit-touch-callout`）保留 —— 它是迁移前
`.art-video` 那条的等价扩大版，防的是拖动时选中控件文字，与本 bug 无关。
而一度加在 `.play-page` 上的整页禁选**已删**：探针证明它对这个 bug 没有任何作用。

### 设置面板在手机上被裁掉半截（2026-09-19 修）

竖屏非全屏时打开齿轮，选项一多面板就被切掉一块。ArtPlayer 的 `.art-settings` /
`.art-selector-list` 是 `position:absolute; bottom: var(--art-control-height)`，高度上限取
`--art-settings-max-height`（桌面 300px、`.art-mobile` 180px）**定值**；而 390 屏减页边距后
播放器只有 ~206px 高，180px 面板加上 44px 控件行必然顶出去，被播放器的 `overflow` 裁掉。

**修法**：上限改成「不超过播放器自己」，两个面板本就自带 `overflow-y: auto`，装不下自己滚：

```css
--art-settings-max-height: min(300px, calc(100% - var(--art-control-height) - 10px));
```

百分比按定位祖先（`.art-video-player`）的高度算，桌面与网页全屏下 `min()` 仍取 300px，观感不变。
这条也得写在**三写**那组里 —— 两个变量 `.art-mobile` 都在设，双写打平就轮不到我们。

### 转横屏后 Safari 工具栏冒出来：不是代码问题

竖屏进网页全屏、再转横屏，屏幕上会多出 Safari 自己的地址栏/工具栏。网页全屏铺满的是
**网页视口**，视口之外的浏览器外壳归 Safari 管，网页没有 API 能收起它；转屏时 Safari
重新显示工具栏是系统行为。真·全屏能顶掉它，但 iPhone Safari 不给任意元素用 Fullscreen API
（见上一节）。唯一彻底的解法是**把 WebVid 加到主屏幕**，以 standalone webapp 打开，
根本没有 Safari 工具栏（`manifest.webmanifest` 的 `display: standalone` 与
`apple-mobile-web-app-capable` 都已就位）。

验收脚本里加了一条「竖屏进全屏后转横屏仍铺满视口」，Chromium 下恒过 —— 它守的是我们这侧
的布局没退化，**证明不了 Safari 外壳的行为**，别拿它当那件事的结论。

## iPhone 的原生全屏会把画面交给系统播放器

ArtPlayer 在 `video:loadedmetadata` 时一次性决定全屏走哪条路（`artplayer.mjs:2763`）：

| 条件 | 走法 | 结果 |
|---|---|---|
| `screenfull.isEnabled`（即 `document[*fullscreenEnabled]`） | `screenfull.request($player)` 对 div 全屏 | DOM 还是我们的，控件与手势保留 |
| 否则 `$video.webkitSupportsFullscreen` | `$video.webkitEnterFullscreen()` | **系统 AVPlayer 接管**，自定义 UI 与手势全失效 |
| 都不满足 | 提示「不支持全屏」 | —— |

iPhone Safari 不给任意元素用 Fullscreen API，`document` 上连 `webkitFullscreenEnabled`
都没有，必然落到第二行。而 `fullscreenWeb` 只是给 `$player` 加 CSS 类撑满视口，不碰原生
API，任何平台都不会被接管。

**处理**：`Play.vue` 顶部按能力算出 `CAN_ELEMENT_FULLSCREEN`，以它决定 `option.fullscreen`
开不开 —— iPhone 上原生全屏按钮直接不创建，只留网页全屏。按能力判断而非认 UA：iPad 与
桌面支持元素全屏，全屏后 DOM 仍是我们的，没必要砍。手势的中间区双击同样跟着退到
`fullscreenWeb`。另给手势绑了 `webkitbeginfullscreen` → `endPress`，堵住「按住进了 2 倍速、
画面被系统接走、`touchend` 回不来、倍速卡在 2x」的窗口。

**`autoOrientation` 有意不开**：它用 `transform: rotate(90deg)` 硬转画面（`:3567`），转完
`getBoundingClientRect()` 返回的是变换后的包围盒，而 `clientX` 仍是屏幕坐标 —— 左右分区会
算反成上下。要开必须让 `zoneOf()` 感知 `art.isRotate` 另走一套坐标。而它只服务「锁定竖排
方向」的用户：没锁方向的人把手机横过来，视口跟着转，网页全屏本就铺满横屏。收益不抵复杂度。

**注意**：这条改动在 Chromium/Edge 里验证不了 —— 它的 `document.fullscreenEnabled` 恒为
true，模拟不出 iPhone 的 API 缺失。上面的结论来自 ArtPlayer 源码路径，真机需最终确认一次：
iPhone 上播放页底栏应只有「网页全屏」一个铺满按钮。

## 验收

```
cd frontend && node gesture-check.mjs
```

起一个最小页面（真 ArtPlayer + ffmpeg 造的 120 秒样本），不掺登录与云盘拉流。覆盖：

- 桌面：双击左右 ±10 秒、**双击不改变播放状态**（撤销逻辑的靶心）、连击累加到 20 秒、
  单击照常暂停、按住进 2 倍速、松手还回 1.5x 常驻档、暂停时按住不加速、
  片头快退收口在 0 秒、片尾快进不越过时长、按住时摘除手势会还回倍率
- 触屏（iPhone UA）：识别为 `.art-mobile`、双击 ±10 秒、**双击不再触发播放/暂停**、
  单击不暂停、中间双击切换播放/暂停
- 横竖屏：转横屏后左右双击仍各自生效、光晕落在正确一侧，转回竖屏同样成立
  （`zoneOf()` 每次点击重读 `getBoundingClientRect()`，分区是按当下画面算的比例，
  不是进页面时定死；光晕层用百分比宽度，跟着一起走）
- 样式作用域（2026-09-18 补）：进网页全屏后确认播放器真被搬到 `<body>`，再量控件的
  `backdrop-filter` 与提示条的 `user-select`（常态 / 网页全屏 / 触屏各一遍）；
  竖屏另量胶囊 36px 与 `--art-control-height` 44px —— 后者专盯变量那条三写选择器的特异性，
  量到 38px 就是被 ArtPlayer 的 `.art-mobile` 打赢了

- 竖屏进网页全屏后转横屏仍铺满视口（守我们这侧的布局，不代表 Safari 外壳）
- 设置面板不顶出播放器上沿、底边落在画面内（页面里塞了 8 个占位选项，菜单只有两行的话
  这条等于没验）

判据取 `backdrop-filter` 而不是尺寸：ArtPlayer 自己一处没用，量到有值就只可能来自我们这份 CSS。

截图落在 `frontend/_gesture-shots/`。样本 `_gesture-sample.mp4` 首次运行时由 ffmpeg 生成。

**状态：46 项全过（2026-09-19）。** 视觉另经截图确认：箭头朝向正确、ArtPlayer 自带提示不再
冒出、横屏下徽章与光晕位置正确；`desktop-fullscreen-web.png` 里全屏后玻璃胶囊、时间胶囊与
贴边红细条都在位。**长按放大镜与设置面板两项已在 iPhone 真机确认。**

### 这个脚本过去有一半没验（教训）

旧版 `extractCss()` 是把 CSS 从 `Play.vue` 抠出来、**剥掉 `:deep()` 包装**再注入独立页面的 ——
它验的只是样式规则本身对不对，**从来没验过真实页面里的作用域能不能命中**。网页全屏整批样式
失配正是栽在这没验的一半上：33 项全过，而全屏一点就全白。现在整份 `player.css` 原样注入，
选择器自带 `.art-video-player`，作用域一并进了验收。

### 用例之间必须静置 800 ms

手势有跨用例的余温：连击窗口 300 ms，光晕的 `runTimer` 还要再留 420 ms 才清掉 `runSide`。
上一个用例的末击若离得太近，下一个用例的第一击会被**正确地**认成同侧连击续上去
（10→20→30→40，seek 也跟着多走一轮）。这是产品该有的行为 —— 真实用户 300 ms 内连点同侧
本就该累加 —— 是用例之间没隔离。`reset()` 开头的 `waitForTimeout(800)` 不能省，它还顺带清掉
上一条 notice（ArtPlayer 的提示要挂两秒，会被下一条断言读成本用例弹的）。

**曾被这件事骗过一轮**：症状是「单击不暂停」，事件流水显示有 `pause→play` 却没有第三个事件。
真相是那一击走了连击分支，`cancelSingle()` 之后压根没设延迟定时器。教训是**事件只反映
「状态真的变了」，反映不了「调了但没变」** —— 验收脚本里留了记录 `play`/`pause` 调用本身
和点击命中元素的诊断桩，失败时才打印，别删。
