// iOS 式 hero 转场：弹层从触发处放大展开、关闭缩回原位（「从哪来回哪去」）。
// 单一真源，覆盖两种用法（options 覆盖差异，缺省=后台弹窗手感）：
//   · 后台管理弹窗（Admin）：中心锚等比缩放，从触发按钮里长出来。
//   · 视频详情卡（VideoDetailCard）：顶边锚（封面与缩略图重合）、超大来源（Featured 横幅）
//     贴四边非等比 morph + 淡入淡出、关场淡掉 chrome 只留封面缩回、圆角补偿、轮播容器回位锚。
//
// 踩坑均已内建：
//   · 全程只动 transform 与 overlay opacity（纯合成器属性，iOS WebKit 安全；严禁 filter 参与
//     动画——glass.css 反馈#35 教训）。弹层自身磨砂由各自 CSS 永久关闭（零切换，规避 iOS 对
//     backdrop-filter 层做缩放动画时采样错位/闪变，反馈#48）。
//   · EP 自带的 modal-fade/dialog-fade：在 overlay 与 overlay-dialog 上用内联 animation:none
//     接管（overlay 是 v-show 持久节点，内联样式常驻，此后每次开合都由这里驱动）。
//   · 关场瞬间即把页面还给用户（反馈#42）：提前摘 EP 滚动锁类 el-popup-parent--hidden +
//     遮罩 pointer-events:none 输入穿透，缩回动画只是收尾演出；body 滚动条宽度补偿留给 EP
//     幂等 cleanup 恢复，别动否则跳版。
//   · 关场收尾不复原弹层内联样式（反馈#45）：done() 后节点由 Vue 异步卸载（配 destroy-on-close
//     下次开是全新节点），抢先清 transform 会让卸载前那一帧闪出全尺寸弹层。
//   · CSS 过渡时长与 JS 收尾定时器同源：均由 dur 派生（timer = 过渡 + 固定缓冲），改一处不失同步。
import { nextTick } from 'vue'
import { MOBILE_BREAKPOINT } from './viewport'

const EASE = 'cubic-bezier(0.32, 0.72, 0, 1)' // iOS 卡片/Sheet 手感
const reduced = () => window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches ?? false
const MOBILE = 700 // 超大来源贴四边 morph 的独立阈值（略小于全站移动断点，刻意）
const isDesktop = () => window.innerWidth > MOBILE_BREAKPOINT

// useHeroDialog(selector, setClosed) —— 旧式位置签名（后台弹窗，中心锚缺省手感）。
// useHeroDialog({ selector, setClosed, anchor, coverBig, chromeClass, cornerVar, reanchor, dur, fadeOutClose })
//   —— 新式 options 签名（详情卡按需覆盖）。
export function useHeroDialog(arg, setClosedArg) {
  const opts = typeof arg === 'string' ? { selector: arg, setClosed: setClosedArg } : arg || {}
  const {
    selector,
    setClosed,
    zoomClass = 'admin-hero-zooming', // 转场态类名（停磨砂 + will-change）
    anchor = 'center', // 'center'（按钮，等比中心）| 'top'（缩略图，顶边对齐）
    coverBig = false, // 来源大于弹层时贴四边非等比 morph + 淡入淡出（横幅），桌面端专用
    chromeClass = null, // 关场附加类，淡掉 chrome（详情卡传 'vdc-closing'）
    cornerVar = null, // 关场圆角补偿的 CSS 变量名（详情卡传 '--vdc-art-r'，基值 12px）
    reanchor = null, // (originEl) => Element：关场回位锚（详情卡：轮播容器优先）
    dur = { in: 440, close: 340 }, // 开/关 transform 过渡时长（ms），收尾定时器由此派生
    fadeOutClose = { dur: 280, delay: 0 }, // 关场遮罩淡出时长/延迟（ms）
  } = opts

  const originStr = anchor === 'top' ? '50% 0' : '50% 50%'
  const sec = (ms) => ms / 1000 // ms → CSS 秒

  let origin = null // { el, rect }：触发元素
  let cardRect = null // 弹层落定矩形（开场量取，供开场中途关闭反推）
  let zoomTimer = 0,
    closeTimer = 0,
    closing = false

  const els = () => {
    const dlg = document.querySelector(selector)
    return { dlg, ov: dlg?.closest('.el-overlay'), ovd: dlg?.closest('.el-overlay-dialog') }
  }

  // 基础 FLIP：把 to 尺寸的元素变换到 from 的位置/大小。anchor 决定 dy 与 transform-origin。
  const baseTransform = (from, to) => {
    const s = Math.max(from.width / to.width, 0.01)
    const dx = from.left + from.width / 2 - (to.left + to.width / 2)
    const dy =
      anchor === 'top'
        ? from.top - to.top
        : from.top + from.height / 2 - (to.top + to.height / 2)
    return `translate(${dx}px, ${dy}px) scale(${s})`
  }

  // 贴四边非等比（超大横幅专用）：起始压成来源精确矩形，向内收缩落定，纵横比渐复原。
  const coverTransform = (from, to) => {
    const sx = Math.max(from.width / to.width, 0.01)
    const sy = Math.max(from.height / to.height, 0.01)
    const dx = from.left + from.width / 2 - (to.left + to.width / 2)
    const dy = from.top - to.top
    return `translate(${dx}px, ${dy}px) scale(${sx}, ${sy})`
  }

  // 弹层 DOM 就绪后、首帧上屏前摆好起飞姿态（nextTick 在 paint 之前跑完）。
  function zoomIn() {
    const { dlg, ov, ovd } = els()
    if (!dlg || !ov) return
    ov.style.animation = 'none'
    if (ovd) ovd.style.animation = 'none'
    ov.style.transition = 'none'
    ov.style.opacity = '0'
    const from = origin?.rect
    if (!from || reduced()) {
      // 无来源 / 系统减少动态：退化为整层快速淡入
      void ov.offsetWidth
      ov.style.transition = 'opacity 0.2s ease'
      ov.style.opacity = ''
      zoomTimer = setTimeout(() => {
        ov.style.transition = ''
      }, 240)
      return
    }
    if (ovd) ovd.style.overflow = 'hidden' // 起飞姿态可能探出视口，别闪滚动条
    cardRect = dlg.getBoundingClientRect()
    const bigger = from.width > cardRect.width // 来源比弹层还大（Featured 横幅）
    const fade = coverBig && bigger // 超大来源配淡入/淡出（凝聚/消融观感）
    const useCover = fade && isDesktop() // 非等比仅桌面端；移动端横幅≈同宽，维持等比手感
    dlg.classList.add(zoomClass)
    dlg.style.transformOrigin = originStr
    dlg.style.transition = 'none'
    dlg.style.transform = useCover ? coverTransform(from, cardRect) : baseTransform(from, cardRect)
    if (fade) dlg.style.opacity = '0'
    void ov.offsetWidth // 起始态强制落地，随后改动才走 transition
    ov.style.transition = 'opacity 0.3s ease'
    ov.style.opacity = ''
    dlg.style.transition = `transform ${sec(dur.in)}s ${EASE}` + (fade ? ', opacity 0.3s ease' : '')
    dlg.style.transform = '' // → 落定
    if (fade) dlg.style.opacity = '1'
    zoomTimer = setTimeout(() => {
      dlg.classList.remove(zoomClass)
      dlg.style.transition = ''
      dlg.style.transform = ''
      dlg.style.transformOrigin = ''
      dlg.style.opacity = ''
      ov.style.transition = ''
      if (ovd) ovd.style.overflow = ''
    }, dur.in + 60)
  }

  // 关闭转场：弹层缩回来源、遮罩淡出，结束才真正关（EP before-close 传 done；
  // 取消钮/保存成功无 done，用 setClosed 兜底）。
  function animatedClose(done) {
    const finish = typeof done === 'function' ? done : setClosed || (() => {})
    if (closing) return
    const { dlg, ov, ovd } = els()
    // 回位锚点：来源仍在则用其实时矩形（reanchor 可改锚到容器，如轮播已自转到别张）；否则用开场 rect。
    let srcEl = origin?.el?.isConnected ? origin.el : null
    if (srcEl && reanchor) srcEl = reanchor(srcEl) || srcEl
    const to = srcEl ? srcEl.getBoundingClientRect() : origin?.rect
    if (!dlg || !ov || !to || reduced()) {
      finish()
      return
    }
    // 开场动画进行中被关：布局矩形取开场量的 cardRect（此刻 gBCR 含补间 transform 不可用；
    // 补间期布局盒没变，恒等 cardRect，靠 zoomClass 是否还在判断）。
    const from = dlg.classList.contains(zoomClass) ? cardRect : dlg.getBoundingClientRect()
    if (!from) {
      finish()
      return
    }
    closing = true
    clearTimeout(zoomTimer)
    document.body.classList.remove('el-popup-parent--hidden') // 提前解滚动锁（反馈#42）
    ov.style.pointerEvents = 'none'
    if (ovd) ovd.style.overflow = 'hidden'
    const biggerClose = coverBig && to.width > from.width // 缩回目标比弹层大=横幅：向后放大 + 淡出
    const useCoverClose = biggerClose && isDesktop()
    dlg.classList.add(zoomClass)
    if (chromeClass) dlg.classList.add(chromeClass)
    dlg.style.transformOrigin = originStr
    // 内联 transition 会盖过类里的：chrome（卡底玻璃/边框/阴影）淡出过渡须一并列出（否则瞬切）。
    let tr = `transform ${sec(dur.close)}s ${EASE}`
    if (chromeClass) tr += ', background 0.16s ease, border-color 0.16s ease, box-shadow 0.16s ease'
    if (biggerClose) tr += ', opacity 0.36s ease 0.1s'
    dlg.style.transition = tr
    dlg.style.transform = useCoverClose ? coverTransform(to, from) : baseTransform(to, from)
    if (biggerClose) {
      dlg.style.opacity = '0'
    } else if (cornerVar) {
      // 只余封面缩回缩略图：全尺寸设 12/scale，缩到位时正好渲染成 12px 与缩略图严丝合缝。
      const scale = Math.max(to.width / from.width, 0.01)
      dlg.style.setProperty(cornerVar, `${(12 / scale).toFixed(1)}px`)
    }
    ov.style.transition =
      `opacity ${sec(fadeOutClose.dur)}s ease` + (fadeOutClose.delay ? ` ${sec(fadeOutClose.delay)}s` : '')
    ov.style.opacity = '0'
    closeTimer = setTimeout(() => {
      ov.style.transition = '' // 先清 transition 再 done()：EP leave 立即完成、节点马上卸载
      finish()
      ov.style.pointerEvents = '' // overlay 是持久节点，穿透态不清会漏到下次
      if (ovd) ovd.style.overflow = ''
      closing = false
      // 不复原 dlg 内联样式（反馈#45）：配 destroy-on-close 下次是全新节点，抢清会闪全尺寸帧
    }, dur.close + 20)
  }

  // 瞬时终结进行中的关场（遮罩已放行输入，缩回途中可能直接重开）：清定时器、复原内联样式、
  // 补回提前摘掉的滚动锁类（visible 全程没翻 false，EP 自己不会再加）。
  function cancelClose() {
    clearTimeout(closeTimer)
    closing = false
    document.body.classList.add('el-popup-parent--hidden')
    const { dlg, ov, ovd } = els()
    if (dlg) {
      dlg.classList.remove(zoomClass)
      if (chromeClass) dlg.classList.remove(chromeClass)
      dlg.style.transition = 'none' // 别让 transform 复位走缩放（随后 zoomIn 会重设）
      dlg.style.transform = ''
      dlg.style.transformOrigin = ''
      dlg.style.opacity = '' // 横幅关场淡出中途被截，复原不透明
      if (cornerVar) dlg.style.removeProperty(cornerVar)
    }
    if (ov) ov.style.pointerEvents = ''
    if (ovd) ovd.style.overflow = ''
  }

  // open(触发元素, 显示函数)：记录来源矩形 → 翻显示开关 → nextTick 摆位起飞。
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
