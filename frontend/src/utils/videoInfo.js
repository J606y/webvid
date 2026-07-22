// 格式探测（/video/info）按路径记忆：详情二级卡片与播放页共用同一次探测。
//
// 反馈#29：非直连格式点「立即播放」时，播放页原本会再发一次 /video/info（云盘上是
// 又一次 ffprobe），探测未回前播放页是空白面板、看着像「点了没反应」。改为两处都走
// 这里：同路径命中在途 Promise 直接复用，全程只探一次；用户在「检测格式中」时点播放，
// 播放页接的就是详情卡那同一次探测，探完即起播。
//
// 成功结果缓存整段会话（后端另按 size+mtime 失效缓存，同路径换内容会重探）；失败不缓存
// 允许重试。返回值即 /video/info 的 PlayInfo：{ strategy, reason, duration, message }。
import { api } from './api'

const cache = new Map() // path -> Promise<PlayInfo>

export function fetchVideoInfo(path) {
  let p = cache.get(path)
  if (!p) {
    p = api.video.info(path)
    p.catch(() => cache.delete(path)) // 失败清缓存，下次重试
    cache.set(path, p)
  }
  return p
}
