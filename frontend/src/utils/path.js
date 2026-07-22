// 逻辑路径工具：路径恒以 / 开头、/ 分隔。
import { getToken } from './token'

export function encodePath(p) {
  return p.split('/').map(encodeURIComponent).join('/')
}

export function join(dir, name) {
  return dir === '/' ? '/' + name : dir + '/' + name
}

export function parent(p) {
  const i = p.lastIndexOf('/')
  return i <= 0 ? '/' : p.slice(0, i)
}

export function segments(p) {
  return p.split('/').filter(Boolean)
}

// img/video 标签带不了 Header，token 走 query（getToken 见 utils/token.js）
export function rawUrl(p, dl = false) {
  return `/api/raw${encodePath(p)}?token=${encodeURIComponent(getToken())}` + (dl ? '&dl=1' : '')
}

export function thumbUrl(p, size = 400) {
  return `/api/thumb${encodePath(p)}?size=${size}&token=${encodeURIComponent(getToken())}`
}

// HLS 转码播放列表地址（分片 URI 的 token 由服务端注入回列表）
export function hlsUrl(p) {
  return `/api/video/hls${encodePath(p)}/index.m3u8?token=${encodeURIComponent(getToken())}`
}

// 构造 /files /play 路由（逐段编码，兼容 % # ? 等字符）
export function filesRoute(p) {
  return '/files/' + segments(p).map(encodeURIComponent).join('/')
}

export function playRoute(p) {
  return '/play/' + segments(p).map(encodeURIComponent).join('/')
}

// 从路由 params.path（已解码的段数组）还原逻辑路径
export function fromParams(param) {
  if (!param || param.length === 0) return '/'
  const segs = Array.isArray(param) ? param : [param]
  return '/' + segs.join('/')
}
