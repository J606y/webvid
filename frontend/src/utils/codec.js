// 本机的 HEVC 解码能力探测。
//
// 服务端据此决定 HEVC 视频是原样封装直出（零编码开销）还是拉起 x264 重编码。判错的
// 代价是不对称的：多转一次只是费 CPU，判成能解而实际解不了就是黑屏，所以一律实测，
// 不看 UA —— Safari、装了 HEVC 扩展的 Edge、有硬解的 Chrome 都能直出，按 UA 判必然
// 既有漏判也有误判。
//
// iPhone 只有 ManagedMediaSource、没有 MediaSource（见 playerHost 的 nativeHlsFirst），
// 两者都没有时由 <video>.canPlayType 说话。

// 探测串里的 level 是名义值：这里问的是「认不认这个 profile」，不是某个具体文件的 level。
// 真遇到超出设备能力的流，播放器会报错并落到既有的兜底面板（见 playerHost）。
const MAIN = 'video/mp4; codecs="hvc1.1.6.L93.B0"' // Main，8bit 4:2:0
const MAIN10 = 'video/mp4; codecs="hvc1.2.4.L120.B0"' // Main 10，10bit 4:2:0

const DEMOTED = 'wv-hevc-demoted'

let cached = null

function typeSupported(type) {
  const MS = window.ManagedMediaSource || window.MediaSource
  if (MS && typeof MS.isTypeSupported === 'function') return MS.isTypeSupported(type)
  const v = document.createElement('video')
  return typeof v.canPlayType === 'function' && v.canPlayType(type) !== ''
}

// hevcCap 返回 0（不支持）/ 8（Main）/ 10（含 Main 10）。同一台设备结果不会变，只探一次。
export function hevcCap() {
  if (cached !== null) return cached
  try {
    if (sessionStorage.getItem(DEMOTED) === '1') {
      cached = 0 // 本会话已经实测过解不了，别再试（见 demoteHevc）
      return cached
    }
  } catch { /* 隐私模式下 sessionStorage 可能不可用，按未降级处理 */ }
  try {
    cached = typeSupported(MAIN10) ? 10 : typeSupported(MAIN) ? 8 : 0
  } catch {
    cached = 0 // 老浏览器连 isTypeSupported 都没有
  }
  return cached
}

// demoteHevc 记下「这台设备报了支持、实际解不了」，此后一律走转码。
// 能力探测再准也只是探测，真解不动时得有一条退路，而且退了就别再回头试。
export function demoteHevc() {
  if (cached === 0) return false
  cached = 0
  try {
    sessionStorage.setItem(DEMOTED, '1')
  } catch { /* 存不下就只在本次会话内存里生效 */ }
  return true
}
