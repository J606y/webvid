const videoExts = new Set(['mp4', 'mkv', 'avi', 'mov', 'wmv', 'flv', 'webm', 'm4v', 'ts', 'm2ts', 'rmvb', 'rm', 'mpg', 'mpeg', 'vob', '3gp'])
const imageExts = new Set(['jpg', 'jpeg', 'png', 'gif', 'webp', 'bmp', 'avif', 'heic'])
const audioExts = new Set(['mp3', 'flac', 'wav', 'm4a', 'ogg', 'aac', 'wma', 'opus'])
const textExts = new Set(['txt', 'log', 'ini', 'conf', 'cfg', 'yml', 'yaml', 'json', 'js', 'mjs', 'jsx', 'tsx', 'py', 'go', 'java', 'c', 'cpp', 'h', 'hpp', 'cs', 'rs', 'rb', 'php', 'sh', 'bat', 'ps1', 'css', 'scss', 'html', 'vue', 'xml', 'sql', 'toml', 'csv'])

export function ext(name) {
  const i = name.lastIndexOf('.')
  return i < 0 ? '' : name.slice(i + 1).toLowerCase()
}

// 前端分类比后端细：audio/pdf/markdown/text 用于点击分发
export function extType(name) {
  const e = ext(name)
  if (videoExts.has(e)) return 'video'
  if (imageExts.has(e)) return 'image'
  if (audioExts.has(e)) return 'audio'
  if (e === 'pdf') return 'pdf'
  if (e === 'md' || e === 'markdown') return 'markdown'
  if (textExts.has(e)) return 'text'
  return 'other'
}

export function typeIcon(item) {
  if (item.is_dir) return 'Folder'
  switch (extType(item.name)) {
    case 'video': return 'VideoCamera'
    case 'image': return 'Picture'
    case 'audio': return 'Headset'
    case 'pdf': return 'Notebook'
    case 'markdown': return 'Memo'
    case 'text': return 'Document'
    default: return 'Files'
  }
}

export function formatSize(n) {
  if (n == null || n < 0) return '-'
  if (n === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return (i === 0 ? v : v.toFixed(1)) + ' ' + units[i]
}

export function formatTime(iso) {
  if (!iso) return '-'
  const d = new Date(iso)
  if (isNaN(d)) return '-'
  const p = (x) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

// 缩略图加载失败：隐藏 img，让下方 .thumb-fallback 兜底图标露出（Files/Library*/Search/VideoDetailCard 共用）
export function hideImg(e) { e.target.style.display = 'none' }

// hasThumb：文件（非目录）且为图片/视频 → 有缩略图（Files/Search 网格共用）。
export function hasThumb(item) {
  if (item.is_dir) return false
  const t = extType(item.name)
  return t === 'image' || t === 'video'
}

// progressPct：播放进度百分比（0-100 整数）；position/duration 任一为空 → 0
// （续播条/继续观看的显示判据，LibraryVideo 与 VideoDetailCard 共用）。
export function progressPct(position, duration) {
  if (!position || !duration) return 0
  return Math.min(100, Math.round((position / duration) * 100))
}

// 去扩展名（同 ext() 的 lastIndexOf 判定：i>0 才算有扩展名，隐藏文件如 .gitignore 保持原样不截断）
export function stripExt(name) {
  const i = name.lastIndexOf('.')
  return i > 0 ? name.slice(0, i) : name
}

// 秒 → 时长文本：h:mm:ss（不足一小时省略时位）/ m:ss
export function formatDuration(s) {
  s = Math.round(s || 0)
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60
  const mm = h ? String(m).padStart(2, '0') : String(m)
  return (h ? `${h}:${mm}` : mm) + ':' + String(sec).padStart(2, '0')
}
