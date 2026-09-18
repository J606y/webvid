// 「右键 + 长按」唤出菜单，以自定义指令提供：
//
//   <div v-menu="(at) => openMenu(at, item)">
//
// 回调收到 {x, y, el}：坐标用来摆菜单，el 是绑定元素，供详情卡做转场起点。
//
// 为什么是指令而不是 `v-on="handlers"`：后者要经过模板表达式求值 + toHandlers 转换 +
// （组件上还要）attrs fallthrough 三道关，任何一环不成立都是**静默失败** —— 生产构建里
// Vue 的 warn 是空函数，toHandlers 拿到非对象直接返回空对象，一个事件不绑、一句话不说。
// 实测就栽在这里：代码、打包、产物全对，元素上就是没有处理器。指令直接 addEventListener，
// 没有中间环节可掉。
//
// 触屏必须做长按：iPhone 没有右键，只认 contextmenu 等于手机上没这功能。
// 同时只可能长按一个目标，所以计时器是模块级单例，不必给每张卡片各留一份。

let timer = null
let x0 = 0
let y0 = 0
let holdEl = null // 正被按住的那个元素：卸载时据此判断该不该掐掉计时
let fired = false // 本次按压是否已经弹出过菜单，决定松手时要不要吞掉补发的 click

function cancelHold() {
  if (timer) {
    clearTimeout(timer)
    timer = null
  }
  holdEl = null
}

// 长按松手时浏览器会补一次 click，那一下会顺势打开详情卡/灯箱 —— 用户本意是唤出菜单。
// 在捕获阶段吃掉紧随其后的那次 click；once 触发后自身即摘除，万一某些浏览器长按后压根
// 不补 click，定时兜底摘掉，否则会把用户下一次正常点击误吃了。
function swallowNextClick() {
  const kill = (e) => {
    e.stopPropagation()
    e.preventDefault()
  }
  document.addEventListener('click', kill, { capture: true, once: true })
  setTimeout(() => document.removeEventListener('click', kill, { capture: true }), 400)
}

const HOLD_MS = 500 // 与播放器的长按倍速同档，手感一致
const SLOP = 10     // 手指挪开这么多像素就算滑列表，不是长按

export const vMenu = {
  mounted(el, binding) {
    el._menuCb = binding.value
    const fire = (x, y) => el._menuCb?.({ x, y, el })

    // iOS 长按会起文字选区、弹「拷贝/查找」那套系统菜单，和我们的长按菜单抢同一个手势。
    // touch-callout 只挡图片和链接那套弹窗，文字选区得靠 user-select —— 卡片上有文件名
    // 和副标题，手指落在字上就被当成选词了，两者都要关。
    // 这两个属性都可继承，设在卡片根上，里面的文字与图片一并生效。
    // 用 setProperty 写带前缀的名字最稳；原值记下来，指令摘掉时还原，
    // 不把元素永久改成不可选。
    el._menuCss = ['-webkit-user-select', 'user-select', '-webkit-touch-callout']
      .map((k) => [k, el.style.getPropertyValue(k)])
    for (const [k] of el._menuCss) el.style.setProperty(k, 'none')

    const h = {
      contextmenu(e) {
        e.preventDefault() // 盖掉浏览器自带菜单
        cancelHold()
        fire(e.clientX, e.clientY)
      },
      touchstart(e) {
        if (e.touches.length !== 1) {
          cancelHold() // 多指是缩放/滑动
          return
        }
        x0 = e.touches[0].clientX
        y0 = e.touches[0].clientY
        cancelHold()
        fired = false
        holdEl = el
        timer = setTimeout(() => {
          timer = null
          fired = true
          fire(x0, y0)
        }, HOLD_MS)
      },
      touchmove(e) {
        const t = e.touches?.[0]
        if (!t) return
        if (Math.abs(t.clientX - x0) > SLOP || Math.abs(t.clientY - y0) > SLOP) cancelHold()
      },
      // 松手这一刻才布防，而不是菜单弹出时：长按常常握满一秒（看清菜单再松手），
      // 从弹出算起的 400 ms 窗口早就过期，补发的 click 会落到菜单上 ——
      // 轻则点中遮罩把菜单关掉，重则点中第一项直接执行。菜单又恰好锚在触摸点，
      // 松手位置漂个几像素就撞上了。
      touchend() {
        cancelHold()
        if (fired) {
          fired = false
          swallowNextClick()
        }
      },
      touchcancel() {
        cancelHold()
        fired = false
      },
    }

    el._menuH = h
    for (const k in h) {
      // 触摸那几个不拦默认行为，挂 passive 不挡页面滚动；contextmenu 要 preventDefault，不能 passive
      el.addEventListener(k, h[k], k.startsWith('touch') ? { passive: true } : undefined)
    }
  },

  // v-for 重渲染时回调是新闭包（捕获了新的 item），必须跟着换，
  // 否则删完一项后菜单还指着旧的那个
  updated(el, binding) { el._menuCb = binding.value },

  unmounted(el) {
    // 只有正按着的就是自己时才掐计时。计时器是模块级共享的，列表重排或删掉某项
    // 导致别的卡片卸载时，不该把用户此刻正在别处进行的长按一起取消。
    if (holdEl === el) {
      cancelHold()
      fired = false
    }
    // 还原 mounted 时改掉的选择/长按样式，原本没设过的直接移除
    if (el._menuCss) {
      for (const [k, v] of el._menuCss) {
        if (v) el.style.setProperty(k, v)
        else el.style.removeProperty(k)
      }
      delete el._menuCss
    }
    const h = el._menuH
    if (!h) return
    for (const k in h) el.removeEventListener(k, h[k])
    delete el._menuH
    delete el._menuCb
  },
}
