// 系统「正在播放」接入：iOS 灵动岛与锁屏、Android 通知栏、macOS 媒体键。
//
// WebKit 只要看到一段「有音轨且正在播」的 video，就会自行建一个系统媒体会话
// （WebCore MediaElementSession 的 NowPlaying 判定，网页没有开关能关掉它）。
// 这个会话默认拿不到页面里的任何信息 —— 灵动岛那颗胶囊于是只剩一个站名，
// 没有片名、没有封面，长按出来的控件也接不上播放器。
// Media Session API 是唯一的补齐口子：把片名、封面、进度与控件交给系统。
// 目标不是让胶囊消失（做不到），而是让它显示得体、能用。
import { thumbUrl, parent, segments } from './path'
import { stripExt } from './file'

const SEEK_STEP = 10 // 系统快退/快进按钮的默认步长（秒），设备给了 seekOffset 就用它的

// attachMediaSession 把播放器接上系统会话，返回 detach。
// detach 必须在离页/重试时调用：否则片名与封面会留在灵动岛上，指向一个已经不在播的视频。
export function attachMediaSession(art, path) {
  const ms = navigator.mediaSession
  if (!ms || typeof window.MediaMetadata !== 'function') return () => {}

  const dir = segments(parent(path)).pop() // 所在目录名当作「专辑」，剧集连播时锁屏上一眼能认
  ms.metadata = new window.MediaMetadata({
    title: stripExt(segments(path).pop() || ''),
    artist: dir || 'WebVid',
    // 封面取 480 档：列表卡片用的就是这一档，多半已在服务端缓存里直接命中，
    // 不必为锁屏再向云盘拉一次视频数据抽帧。灵动岛那颗图只有二十几点，绰绰有余。
    artwork: [{ src: thumbUrl(path, 480), type: 'image/jpeg' }],
  })

  const seek = (t) => {
    const d = art.duration
    art.currentTime = Math.min(Math.max(t, 0), isFinite(d) && d > 0 ? d : Infinity)
  }
  const actions = {
    play: () => { art.play().catch(() => {}) }, // 系统按钮触发的播放同样可能撞上自动播放策略
    pause: () => art.pause(),
    stop: () => art.pause(),
    seekbackward: (d) => seek(art.currentTime - (d?.seekOffset || SEEK_STEP)),
    seekforward: (d) => seek(art.currentTime + (d?.seekOffset || SEEK_STEP)),
    seekto: (d) => seek(d?.seekTime ?? art.currentTime),
  }
  for (const [name, fn] of Object.entries(actions)) {
    // 浏览器不认的动作会抛 TypeError，逐个隔离：能接几个是几个，别让一个不支持的
    // 动作带崩整套控件。
    try { ms.setActionHandler(name, fn) } catch { /* 该动作此平台不支持 */ }
  }

  // 锁屏与灵动岛的进度条。duration 非有限（HLS event 列表边转码边播时就是 Infinity）
  // 或 position 越界都会让 setPositionState 抛 TypeError，先自校验再交给系统。
  const syncPosition = () => {
    const d = art.duration
    if (!isFinite(d) || d <= 0 || typeof ms.setPositionState !== 'function') return
    const rate = art.video.playbackRate
    try {
      ms.setPositionState({
        duration: d,
        position: Math.min(Math.max(art.currentTime || 0, 0), d),
        playbackRate: rate > 0 ? rate : 1, // 暂停态某些实现会给 0，而入参要求正数
      })
    } catch { /* 进度条非关键路径，失败不影响片名与封面 */ }
  }

  let lastSync = 0
  art.on('video:loadedmetadata', syncPosition)
  art.on('video:seeked', syncPosition)
  art.on('video:ratechange', syncPosition)
  art.on('video:play', () => { ms.playbackState = 'playing'; syncPosition() })
  art.on('video:pause', () => { ms.playbackState = 'paused'; syncPosition() })
  // timeupdate 约 4Hz，节流到 1s：系统进度条本就按 playbackRate 自走，这里只纠偏。
  art.on('video:timeupdate', () => {
    const now = Date.now()
    if (now - lastSync < 1000) return
    lastSync = now
    syncPosition()
  })

  return () => {
    for (const name of Object.keys(actions)) {
      try { ms.setActionHandler(name, null) } catch { /* 当初就没设上 */ }
    }
    if (typeof ms.setPositionState === 'function') {
      try { ms.setPositionState() } catch { /* 清空进度，个别实现不接受空参 */ }
    }
    ms.metadata = null
    ms.playbackState = 'none'
  }
}
