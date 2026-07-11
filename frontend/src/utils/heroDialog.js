// 后台管理弹窗的 iOS 式 hero 转场：从触发按钮处放大展开、关闭缩回按钮原位（「从哪来回哪去」）。
// 提炼自 VideoDetailCard 的详情卡转场，踩坑均已内建：
//   · 全程只动 transform 与 overlay opacity（纯合成器属性，iOS WebKit 安全；严禁 filter 参与
//     动画——glass.css 反馈#35 教训）；弹窗自身磨砂由 glass.css .admin-hero 区块永久关闭
//     （零切换，规避 iOS 对 backdrop-filter 层做缩放动画时采样错位/闪变，反馈#48）。
//   · EP 自带的 modal-fade/dialog-fade：在 overlay 与 overlay-dialog 上用内联 animation:none
//     接管（overlay 是 v-show 持久节点，内联样式常驻，此后每次开合都由这里驱动）。
//   · 关场瞬间即把页面还给用户（反馈#42）：提前摘 EP 滚动锁类 el-popup-parent--hidden +
//     遮罩 pointer-events:none 输入穿透，缩回动画只是收尾演出；body 滚动条宽度补偿留给 EP
//     幂等 cleanup 恢复，别动否则跳版。
//   · 关场收尾不复原弹窗内联样式（反馈#45）：done() 后节点由 Vue 异步卸载（配 destroy-on-close
//     下次开是全新节点），抢先清 transform 会让卸载前那一帧闪出全尺寸弹窗。
import { nextTick } from 'vue'

const EASE = 'cubic-bezier(0.32, 0.72, 0, 1)' // iOS 卡片/Sheet 手感
const reduced = () => window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches ?? false

// 中心锚 FLIP：把弹窗缩到来源按钮的大小、中心对齐按钮中心（来源是小按钮、无内容对位需求，
// 故不需要详情卡那套贴边/补圆角/淡 chrome 的复杂处理，等比缩放即"从按钮里长出来"）
function flip(from, to) {
  const s = Math.max(from.width / to.width, 0.08)
  const dx = (from.left + from.width / 2) - (to.left + to.width / 2)
  const dy = (from.top + from.height / 2) - (to.top + to.height / 2)
  return `translate(${dx}px, ${dy}px) scale(${s})`
}

// dlgSelector：唯一定位该弹窗本体，如 '.admin-hero-storage .el-dialog'
// setClosed：把宿主的 v-model ref 置 false（用于非 EP 触发的关闭：取消钮 / 保存成功）
export function useHeroDialog(dlgSelector, setClosed) {
  let origin = null        // { el, rect }：触发按钮
  let cardRect = null      // 弹窗落定矩形（开场量取，供开场中途关闭反推）
  let zoomTimer = 0, closeTimer = 0, closing = false

  const els = () => {
    const dlg = document.querySelector(dlgSelector)
    return { dlg, ov: dlg?.closest('.el-overlay'), ovd: dlg?.closest('.el-overlay-dialog') }
  }

  // 弹窗 DOM 就绪后、首帧上屏前摆好起飞姿态（nextTick 在 paint 之前跑完）
  function zoomIn() {
    const { dlg, ov, ovd } = els()
    if (!dlg || !ov) return
    ov.style.animation = 'none'
    if (ovd) ovd.style.animation = 'none'
    ov.style.transition = 'none'
    ov.style.opacity = '0'
    const from = origin?.rect
    if (!from || reduced()) { // 无来源 / 系统减少动态：退化为整层快速淡入
      void ov.offsetWidth
      ov.style.transition = 'opacity 0.2s ease'
      ov.style.opacity = ''
      zoomTimer = setTimeout(() => { ov.style.transition = '' }, 240)
      return
    }
    if (ovd) ovd.style.overflow = 'hidden' // 起飞姿态可能探出视口，别闪滚动条
    cardRect = dlg.getBoundingClientRect()
    dlg.classList.add('admin-hero-zooming')
    dlg.style.transformOrigin = '50% 50%'
    dlg.style.transition = 'none'
    dlg.style.transform = flip(from, cardRect) // 起始态：缩在按钮处
    void ov.offsetWidth // 起始态强制落地，随后改动才走 transition
    ov.style.transition = 'opacity 0.3s ease'
    ov.style.opacity = ''
    dlg.style.transition = `transform 0.44s ${EASE}`
    dlg.style.transform = '' // → 落定
    zoomTimer = setTimeout(() => {
      dlg.classList.remove('admin-hero-zooming')
      dlg.style.transition = ''; dlg.style.transform = ''; dlg.style.transformOrigin = ''
      ov.style.transition = ''
      if (ovd) ovd.style.overflow = ''
    }, 470)
  }

  // 关闭转场：弹窗缩回来源按钮、遮罩淡出，结束才真正关（EP before-close 传 done；
  // 取消钮/保存成功无 done，用 setClosed 兜底）
  function animatedClose(done) {
    const finish = typeof done === 'function' ? done : (setClosed || (() => {}))
    if (closing) return
    const { dlg, ov, ovd } = els()
    // 回位锚点：按钮若仍在（编辑行/工具栏未重渲）用其实时矩形；否则用开场量的 rect 兜底
    const srcEl = origin?.el?.isConnected ? origin.el : null
    const to = srcEl ? srcEl.getBoundingClientRect() : origin?.rect
    if (!dlg || !ov || !to || reduced()) { finish(); return }
    // 开场动画进行中被关：布局矩形取开场量的 cardRect（此刻 gBCR 含补间 transform 不可用；
    // 补间期布局盒没变，恒等 cardRect，靠 admin-hero-zooming 是否还在判断）
    const from = dlg.classList.contains('admin-hero-zooming') ? cardRect : dlg.getBoundingClientRect()
    if (!from) { finish(); return }
    closing = true
    clearTimeout(zoomTimer)
    document.body.classList.remove('el-popup-parent--hidden') // 提前解滚动锁（反馈#42）
    ov.style.pointerEvents = 'none'
    if (ovd) ovd.style.overflow = 'hidden'
    dlg.classList.add('admin-hero-zooming')
    dlg.style.transformOrigin = '50% 50%'
    dlg.style.transition = `transform 0.34s ${EASE}`
    dlg.style.transform = flip(to, from) // → 缩回按钮
    ov.style.transition = 'opacity 0.28s ease'
    ov.style.opacity = '0'
    closeTimer = setTimeout(() => {
      ov.style.transition = '' // 先清 transition 再 done()：EP leave 立即完成、节点马上卸载
      finish()
      ov.style.pointerEvents = '' // overlay 是持久节点，穿透态不清会漏到下次
      if (ovd) ovd.style.overflow = ''
      closing = false
      // 不复原 dlg 内联样式（反馈#45）：配 destroy-on-close 下次是全新节点，抢清会闪全尺寸帧
    }, 360)
  }

  // 瞬时终结进行中的关场（遮罩已放行输入，缩回途中可能直接重开）：清定时器、复原内联样式、
  // 补回提前摘掉的滚动锁类（visible 全程没翻 false，EP 自己不会再加）
  function cancelClose() {
    clearTimeout(closeTimer)
    closing = false
    document.body.classList.add('el-popup-parent--hidden')
    const { dlg, ov, ovd } = els()
    if (dlg) {
      dlg.classList.remove('admin-hero-zooming')
      dlg.style.transition = 'none' // 别让 transform 复位走缩放（随后 zoomIn 会重设）
      dlg.style.transform = ''
      dlg.style.transformOrigin = ''
    }
    if (ov) ov.style.pointerEvents = ''
    if (ovd) ovd.style.overflow = ''
  }

  // open(触发按钮, 显示函数)：记录来源矩形 → 翻显示开关 → nextTick 摆位起飞
  function open(originEl, show) {
    clearTimeout(zoomTimer)
    if (closing) cancelClose()
    cardRect = null
    origin = originEl ? { el: originEl, rect: originEl.getBoundingClientRect() } : null
    show()
    nextTick(zoomIn)
  }

  return { open, animatedClose, cancelClose }
}
