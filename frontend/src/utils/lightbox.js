// PhotoSwipe 灯箱封装：传入图片逻辑路径数组与起始下标。
// 秒开策略：不再预下载原图探尺寸（旧版 await probe 原图，云盘 302 下原图
// 要下载两遍、灯箱要黑屏等几秒）。改为：
//   1) 立即 init，首图用列表页已缓存的缩略图当 msrc 占位（首帧秒出）；
//   2) 占位尺寸取缩略图比例放大（比例≈原图，避免开场画框变形），
//      缩略图通常已在浏览器缓存，probe 几乎瞬时，最多等 250ms 兜底；
//   3) 原图由 PhotoSwipe 自行加载，loadComplete 时从 <img>.naturalWidth
//      读真实尺寸原地刷新（slide.resize），全程零额外原图请求。
import PhotoSwipe from 'photoswipe'
import 'photoswipe/style.css'
import http from '../api/http'
import { rawUrl, thumbUrl } from './path'

// 记录一次「查看」（「最近查看」货架数据源，复用视频最近播放的 play_history）。
// 防抖 600ms：快速翻页只记停留下来的那张，不逐张刷屏；silent 失败不打扰。
let viewTimer = null
function reportView(p) {
  if (!p) return
  clearTimeout(viewTimer)
  viewTimer = setTimeout(() => {
    http.post('/media/played', { path: p }, { silent: true }).catch(() => {})
  }, 600)
}

function probe(src, ms = 250) {
  return new Promise((resolve) => {
    const img = new Image()
    const timer = setTimeout(() => resolve(null), ms)
    img.onload = () => { clearTimeout(timer); resolve({ w: img.naturalWidth, h: img.naturalHeight }) }
    img.onerror = () => { clearTimeout(timer); resolve(null) }
    img.src = src
  })
}

export async function openLightbox(paths, index = 0, msize = 320, getEl = null) {
  const items = paths.map((p) => ({
    src: rawUrl(p),
    msrc: thumbUrl(p, msize),
    width: 1920,
    height: 1080,
    thumbCropped: true, // 列表缩略图均为 cover 裁切，zoom 转场按裁切对齐画框
    measured: false,
  }))
  const dim = await probe(items[index].msrc)
  if (dim && dim.w && dim.h) {
    // ×8 保证占位尺寸大于屏幕，initial zoom 走 fit 缩小而非原尺寸悬空
    items[index].width = dim.w * 8
    items[index].height = dim.h * 8
  }

  const pswp = new PhotoSwipe({
    dataSource: items,
    index,
    bgOpacity: 0.9,
    zoom: true,
    wheelToZoom: true,
    // iOS 同款 hero 转场（反馈#49，与视频详情卡同手感）：调用方给出「第 i 张图对应的
    // 缩略图元素」时走 zoom——开场从点击的缩略图放大出来、关场缩回当前张的缩略图处
    //（翻页后回新位置）；不给（文件管理）保持原 fade
    showHideAnimationType: getEl ? 'zoom' : 'fade',
  })
  if (getEl) {
    pswp.addFilter('thumbEl', (thumbEl, data, i) => {
      const el = getEl(i)
      // 元素不在或被隐藏（缩略图加载失败 hideImg）→ 交回默认，该张退化为 fade
      return el && el.offsetWidth ? el : thumbEl
    })
  }
  // 原图（含预加载的相邻图）加载完成 → 用真实尺寸替换占位尺寸
  pswp.on('loadComplete', ({ slide, content, isError }) => {
    const el = content.element
    const it = content.data
    if (isError || !el || !el.naturalWidth || it.measured) return
    it.measured = true
    it.width = content.width = el.naturalWidth
    it.height = content.height = el.naturalHeight
    if (slide) {
      slide.width = it.width
      slide.height = it.height
      slide.resize()
    }
  })
  pswp.on('change', () => reportView(paths[pswp.currIndex]))
  pswp.init()
  reportView(paths[index])
  return pswp
}
