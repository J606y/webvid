// 播放器单例与「画中画寄存」。
//
// 为什么播放器实例要住在 Vue 组件外面：iOS 的系统画中画绑的是那个 <video> 元素本身。
// 播放器若随播放页组件销毁，用户一开小窗、一返回视频库，小窗立刻消失 —— 而"一边看一边
// 干别的"正是画中画唯一的意义。所以实例归本模块所有，播放页只是借它一个位置：
//   进播放页  attach(插槽)  → 容器搬进页面，内联播
//   离播放页  leave()       → 在小窗里就搬进隐藏宿主继续播（寄存），否则销毁
//   退出小窗  onPip(false)  → 还在播 = 用户点了还原键，回播放页接回；已暂停 = 关闭键，收摊
//
// 容器 div 由本模块自建、不归 Vue 管：Vue 卸载页面时会连同它的子树一起摘掉，
// 只有自建的节点才能在卸载前整块搬走。样式因此也不能用 scoped，见 assets/player.css。
import Artplayer from 'artplayer'
import router from '../router'
import { api } from './api'
import { attachMediaSession } from './mediaSession'
import { demoteHevc } from './codec'
import { forgetVideoInfo } from './videoInfo'
import { playRoute } from './path'
import { pipMode, isPipActive, enterPip, exitPip, onPipChange } from './pip'

const PARK_ID = 'wv-player-park'

// 画中画图标：SF Symbols 的 pip.enter 形制（外框 + 右下角小窗）。
// width/height 属性必须显式写死 —— 只有 viewBox 的 svg 在 iOS WebKit 的百分比尺寸链里
// 会解析成 0 高，iPhone 上图标直接消失（同 .art-state 三角的教训）。
// class 与 ArtPlayer 自带图标对齐，才能吃到 --art-control-icon-size 与 fill 规则。
const PIP_ICON = '<svg class="art-icon art-icon-pip" xmlns="http://www.w3.org/2000/svg" width="22" height="22" viewBox="0 0 24 24">'
  + '<path fill-rule="evenodd" d="M3 4.5h18A1.5 1.5 0 0 1 22.5 6v12a1.5 1.5 0 0 1-1.5 1.5H3A1.5 1.5 0 0 1 1.5 18V6A1.5 1.5 0 0 1 3 4.5Zm.2 1.7v11.6h17.6V6.2H3.2Z"></path>'
  + '<rect x="11.8" y="10.8" width="8.4" height="6" rx="1"></rect>'
  + '</svg>'

let art = null
let hls = null
let container = null   // 自建的播放器容器，随实例生灭
let curPath = ''       // 当前实例播的逻辑路径，用于判断"接回"还是"换片"
let curHls = false     // 当前实例是否 HLS（错误文案分流）
let curReason = ''     // 转封装 / 转码，供接回时还原页面徽标
let curHevc = false    // 本次直出靠的是本机自报的 HEVC 能力（解码失败时据此降级）
let onFail = null      // 播放页给的运行期中断回调；寄存后置空（页面已不在，没处弹面板）
let detachMS = null    // 系统「正在播放」会话摘除句柄
let detachPip = null   // 画中画事件摘除句柄
let reportTimer = null // 进度定时上报句柄
let parked = false     // 是否寄存中（人在别的页面，视频在小窗里播）
let pipAdded = false   // 画中画按钮是否已加（能力要等元数据就绪才准，加得比构造晚）
let failAt = -1        // 首次报错时的播放位置（<0 = 未记），见 markFail

// ---- 进度上报（原在 Play.vue，寄存期间必须继续跑，故随实例落到这里）----

// report 上报播放进度：起播 position=0 只刷"最近播放"；播放中带当前秒数；
// duration 供货架/详情卡画进度条（后端只在 duration>0 时更新，避免元数据未就绪的
// 早期上报把已知时长覆盖成 0）。ended=true 只在播完时带上，后端据此把续播点归零
// （拖动到片尾不算看完，续播点要忠实保留）。silent 失败不打扰。频率由各调用点节流。
function report(position, ended = false) {
  if (!curPath) return
  const sec = Math.floor(position || 0)
  const dur = art && isFinite(art.duration) ? art.duration : 0
  api.media.played({ path: curPath, position: sec, duration: dur, ended }).catch(() => {})
}

// 关页兜底：寄存之后就没有"组件卸载"这个上报时机了，标签页被关掉会连末次进度一起丢。
function onPageHide() {
  if (art) { try { report(art.currentTime) } catch { /* 末次进度，忽略异常 */ } }
}

// ---- 寄存 ----

// 隐藏宿主：离屏定位而非 display:none / visibility:hidden —— 后者会让 WebKit 判定
// 元素不可渲染，直接把小窗收掉。样式见 assets/player.css。
function parkHost() {
  let el = document.getElementById(PARK_ID)
  if (!el) {
    el = document.createElement('div')
    el.id = PARK_ID
    document.body.appendChild(el)
  }
  return el
}

// move 同一任务内 remove + insert，播放不中断：HTML 规范的「移出文档即暂停」判定要等
// stable state，同步搬移到那时元素已经回到文档里了。
function move(target) {
  if (container && container.parentNode !== target) target.appendChild(container)
}

// 退出小窗时的分流。只在寄存态生效：人在播放页时进出小窗不该有任何副作用。
function onPip(active) {
  if (active || !parked) return
  // WebKit 的小窗关闭键会顺带暂停播放，还原键不会 —— 以此区分用户想收摊还是想接着看。
  if (art && !art.video.paused) router.push(playRoute(curPath))
  else destroy()
}

// ---- 对外 ----

// attach 把播放器交给播放页的插槽。同一部片且实例还活着 = 从小窗接回，不重建、不重新起播。
export async function attach(slot, opts) {
  if (art && curPath === opts.path) {
    onFail = opts.onFail || null
    parked = false
    move(slot)
    exitPip(art.video)
    return
  }
  destroy() // 换片：旧实例先补报进度再收掉
  await create(slot, opts)
}

// leave 播放页卸载时调用（onBeforeUnmount，此时 Vue 还没摘 DOM）。
export function leave() {
  if (art && isPipActive(art.video)) {
    parked = true
    onFail = null // 页面没了，运行期中断只能收摊，没处弹兜底面板
    move(parkHost())
    return
  }
  destroy()
}

// destroy 收掉播放器与转码流，并补记一次末次进度。离页、换片、关小窗共用。
export function destroy() {
  if (reportTimer) { clearTimeout(reportTimer); reportTimer = null }
  window.removeEventListener('pagehide', onPageHide)
  // 先摘系统会话再销毁播放器：否则片名与封面会留在灵动岛上，指着一个已经不在播的视频
  if (detachMS) { detachMS(); detachMS = null }
  if (detachPip) { detachPip(); detachPip = null }
  if (art) {
    try { report(art.currentTime) } catch { /* 末次进度，忽略异常 */ }
    art.destroy(true)
    art = null
  }
  if (hls) {
    hls.destroy()
    hls = null
  }
  if (container) { container.remove(); container = null }
  curPath = ''
  curReason = ''
  curHevc = false
  onFail = null
  parked = false
  pipAdded = false
  failAt = -1
}

// snapshot 给播放页判断"这部片是不是还在我手上活着"：在的话页面直接复用，
// 不必再探测一次格式、也不必重新起播（从小窗接回就靠它）。
export function snapshot(path) {
  if (!art || curPath !== path) return null
  return { isHls: curHls, reason: curReason }
}

// markFail 记下断点。落到兜底面板之前，ArtPlayer 已经重设 url 重载过两轮
// （RECONNECT_TIME_MAX，每轮间隔 1 秒），hls.js 的恢复同理 —— 那时 currentTime 早已归零，
// 「从中断处重试」实际是从头开始。位置只在第一次报错时记；一旦恢复播放即作废。
function markFail() {
  if (failAt < 0 && art && isFinite(art.currentTime)) failAt = art.currentTime
}

// mediaErrMessage 原生播放路径的中断原因。浏览器肯说的只有 MediaError.code，
// 但足以把"取不到流"和"解不了码"分开 —— 过去两者落到同一句"网络不稳"，
// 用户照着那句话排查，方向从一开始就是错的。
function mediaErrMessage() {
  const code = art && art.video.error ? art.video.error.code : 0
  if (code === 3) return '播放已中断：视频流无法解码，该文件可能已损坏。'
  if (code === 4) {
    return curHls
      ? '播放已中断：转码会话已结束，请重试。'
      : '播放已中断：无法读取视频源，存储可能暂时不可用。'
  }
  return curHls
    ? '播放已中断：视频流断开，可能是网络不稳或转码会话已结束。'
    : '播放已中断：视频流断开，可能是网络不稳或文件已不可访问。'
}

// hlsErrMessage hls.js 的中断原因。它只交出状态码（响应体拿不到，xhr-loader 给的
// text 是 statusText），但状态码已经够分事：404/410 是转码会话被回收，
// 5xx 是服务端从存储取不到源（见 internal/stream 首块失败返回的 502）。
function hlsErrMessage(data, isMedia) {
  if (isMedia) return '播放已中断：视频流无法解码，该文件可能已损坏。'
  const code = data.response?.code || 0
  if (code === 404 || code === 410) return '播放已中断：转码会话已结束，请重试。'
  if (code >= 500) return '播放已中断：服务器无法从存储读取这段视频，请稍后重试。'
  return '播放已中断：视频流断开，可能是网络不稳或转码会话已结束。'
}

// fail 运行期播放中断 → 交播放页落到可重试、可下载的兜底面板。
// 探测期失败一直有 unsupported 兜底，运行期（转码会话被回收、分片报错、断流）却没有，
// 播放器只会无尽转圈。断点随回调交出去，重试从中断处接着播。
function fail(msg) {
  let at = failAt
  if (at < 0) at = art && isFinite(art.currentTime) ? art.currentTime : 0
  const cb = onFail
  // 切断 ArtPlayer 自带的断流重连，别让它在实例销毁后继续重设 url
  if (art) art.off('video:error')
  destroy()
  if (cb) cb(msg, at)
}

// ensurePipControl 加画中画按钮。能力判定放在元数据就绪之后：iOS 在那之前
// webkitSupportsPresentationMode 可能还报 false，构造时判会漏掉按钮。
function ensurePipControl() {
  if (!art || pipAdded || !pipMode(art.video)) return
  pipAdded = true
  art.controls.add({
    name: 'pip',
    position: 'right',
    index: 40,
    html: PIP_ICON,
    tooltip: '画中画',
    click() {
      const v = art.video
      if (isPipActive(v)) exitPip(v)
      else enterPip(v).catch(() => { art.notice.show = '当前设备不支持画中画' })
    },
  })
}

// nativeHlsFirst：iPhone 只给 ManagedMediaSource、没有 MediaSource。这类机型把 m3u8
// 交回 Safari 原生 HLS —— 系统画中画、AirPlay 与硬解都在原生这条链上，走 MSE 等于把它们
// 让出去（hls.js README 同此建议）。iPad 与 macOS 有完整 MSE，画中画早已可用，维持 hls.js。
function nativeHlsFirst() {
  if (window.MediaSource) return false
  return !!document.createElement('video').canPlayType('application/vnd.apple.mpegurl')
}

// loadPolicy 拼一条 hls.js 的加载策略。
//
// hls.js 的默认耐心远短于服务端：分片首字节 10 秒即判超时（fragLoadPolicy 默认
// maxTimeToFirstByteMs=10000），播放列表 20 秒。而服务端起播时要等 ffmpeg 出东西，
// 全程不发一个字节 —— event 模式的列表最长等 30 秒，init.mp4 20 秒，分片 90 秒
// （拖到未生成处要 -ss 重启，从头起跑）。默认值下客户端每 10 秒断一次、重试几轮，
// 每一轮都在服务端多留一个阻塞的 handler，而服务端的耐心一次都没用上。
// 这里把两头对齐：客户端等得比服务端久一点，超时才真的意味着出了问题。
function loadPolicy(firstByteMs, totalMs, timeoutRetry, errorRetry) {
  return {
    default: {
      maxTimeToFirstByteMs: firstByteMs,
      maxLoadTimeMs: totalMs,
      timeoutRetry: { maxNumRetry: timeoutRetry, retryDelayMs: 0, maxRetryDelayMs: 0 },
      errorRetry: { maxNumRetry: errorRetry, retryDelayMs: 1000, maxRetryDelayMs: 8000 },
    },
  }
}

async function create(slot, opts) {
  const { path, url, isHls, resumeAt = 0 } = opts

  // hls.js 在构造播放器前就位，customType 才能保持同步（ArtPlayer 在 url 赋值时同步调它）
  let Hls = null
  if (isHls && !nativeHlsFirst()) {
    const mod = await import('hls.js') // 独立 chunk，仅转码播放时加载
    if (mod.default.isSupported()) Hls = mod.default
  }
  if (!slot.isConnected) return // 异步期间已快速离页，别再建播放器

  curPath = path
  curHls = isHls
  curReason = opts.reason || ''
  curHevc = !!opts.hevc
  onFail = opts.onFail || null

  container = document.createElement('div')
  container.className = 'wv-player glass glass-panel'
  slot.appendChild(container)

  const artOpts = {
    container,
    url,
    title: path.split('/').filter(Boolean).pop() || '',
    theme: '#ff0000', // YouTube 红：已播进度条 / 拖拽圆点 / 音量 / 选中项统一取此色
    volume: 0.7,
    // 关掉 ArtPlayer 的 backdrop：它默认给弹窗加 .art-backdrop 类、附带一条
    // `.art-video-player.art-backdrop .art-volume-inner{background:rgba(0,0,0,.75)}`（0,3,0 高优先级），
    // 会把弹窗背景钉死成黑、盖过 --art-widget-background。关掉后弹窗背景回落到该变量（可控成白），
    // 磨砂由 assets/player.css 自己加。
    backdrop: false,
    setting: true,
    playbackRate: true,
    aspectRatio: true,
    // ArtPlayer 自带的画中画只认方法存在、不认真实能力（系统里关掉画中画照样给按钮），
    // 也不监听 webkitpresentationmodechanged，改由 ensurePipControl 接管。
    pip: false,
    fullscreen: true,
    fullscreenWeb: true,
    hotkey: true,
    autoSize: false,
    autoplay: true,
    // 中间大播放态图标换成纯三角（去掉 ArtPlayer 自带的实心圆），
    // .art-state 用液态玻璃圆承托 —— 圆由玻璃画、三角只是白色glyph。
    // svg 必须带显式 width/height 属性（ArtPlayer 自带图标全都带）：只有 viewBox 的
    // svg 在 iOS WebKit 的百分比尺寸链里会解析成 0 高，iPhone 上三角直接消失只剩玻璃圆。
    icons: {
      state: '<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 24 24"><path d="M8 5v14l11-7z"></path></svg>',
    },
  }

  if (isHls) {
    artOpts.type = 'm3u8'
    artOpts.customType = {
      m3u8(video, src) {
        if (!Hls) {
          video.src = src // Safari 原生 HLS：token 已由服务端注入播放列表
          return
        }
        if (hls) hls.destroy()
        // 续播：从 resumeAt 起（0 = 从头）。event 型列表（remux 边跑边播）默认会追
        // "直播沿"，显式 startPosition 强制落到目标位置；vod 列表本就全时间轴可 seek。
        hls = new Hls({
          startPosition: resumeAt,
          manifestLoadPolicy: loadPolicy(45000, 60000, 2, 1),
          playlistLoadPolicy: loadPolicy(45000, 60000, 2, 2),
          fragLoadPolicy: loadPolicy(100000, 140000, 2, 6),
        })
        let netRetry = 0
        let mediaRetry = 0
        // 运行期中断兜底：转码会话被回收、分片请求失败、断流都在这里报 fatal。
        // 网络与解码类先按 hls.js 的既定手法就地恢复，连续恢复不了才落兜底面板。
        hls.on(Hls.Events.ERROR, (_, data) => {
          if (!data.fatal) return
          markFail() // 断点要在恢复动作之前记：startLoad / recoverMediaError 都会动 currentTime
          if (data.type === Hls.ErrorTypes.NETWORK_ERROR && netRetry < 3) {
            netRetry++
            hls.startLoad()
            return
          }
          if (data.type === Hls.ErrorTypes.MEDIA_ERROR && mediaRetry < 2) {
            mediaRetry++
            hls.recoverMediaError()
            return
          }
          // 本机报了支持 HEVC，实际解不动：能力探测再准也只是探测，这里是它的退路。
          // 记下来改走转码，并丢掉按旧能力算出的探测结论，重试即生效。
          if (data.type === Hls.ErrorTypes.MEDIA_ERROR && curHevc && demoteHevc()) {
            forgetVideoInfo(curPath)
            fail('播放已中断：这台设备解不了该视频的编码，已改用转码播放。')
            return
          }
          fail(hlsErrMessage(data, data.type === Hls.ErrorTypes.MEDIA_ERROR))
        })
        hls.loadSource(src)
        hls.attachMedia(video)
      },
    }
  }

  art = new Artplayer(artOpts)
  detachMS = attachMediaSession(art, path) // 接上系统「正在播放」：iOS 灵动岛/锁屏拿到片名、封面、进度与控件
  detachPip = onPipChange(art.video, onPip)
  window.addEventListener('pagehide', onPageHide)

  // 续播定位：direct / Safari 原生 HLS 走 video.currentTime；hls.js 已在 startPosition 处理
  art.on('ready', () => {
    if (resumeAt > 0 && !hls) art.currentTime = resumeAt
    report(art.currentTime || 0) // 起播即记一次最近播放
    ensurePipControl()
  })
  // 元数据就绪后补记一次，确保 duration 落库（ready 可能早于 metadata，dur 尚为 0）
  art.on('video:loadedmetadata', () => {
    report(art.currentTime || 0)
    ensurePipControl()
  })
  // 播放中每 10s 上报一次；暂停/跳转/播完各补一次
  art.on('video:timeupdate', () => {
    if (reportTimer) return
    reportTimer = setTimeout(() => {
      reportTimer = null
      if (art && !art.paused) report(art.currentTime)
    }, 10000)
  })
  art.on('video:pause', () => report(art.currentTime))
  art.on('video:seeked', () => report(art.currentTime))
  art.on('video:ended', () => report(art.duration || 0, true)) // 播完 → 后端归零，下次从头
  art.on('video:playing', () => { failAt = -1 }) // 又播起来了，之前记的断点作废
  // 原生播放路径（直连文件 / iPhone 的原生 HLS）的运行期兜底：hls.js 分支自有恢复策略，
  // 这里只管没有 hls 实例的情形。先让 ArtPlayer 自带的重连试两轮，仍不行才落兜底面板。
  art.on('error', (_, times) => {
    markFail() // 记在放行重连之前：ArtPlayer 每轮重连都会重设 url，currentTime 随即归零
    if (hls || times < 2) return
    fail(mediaErrMessage())
  })
}
