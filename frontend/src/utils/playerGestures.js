// YouTube 式播放手势：双击左右 ±10 秒、按住 2 倍速。
//
// 为什么不用 ArtPlayer 自带的 fastForward：它只认触屏、固定 3 倍、要按满 1 秒，
// 且全程没有视觉反馈 —— 松手前用户不知道自己进没进倍速。
//
// 与 ArtPlayer 点击逻辑的关系（本模块最需要当心的地方）：
// ArtPlayer 在 $video 上自己代理了 click，单击/双击语义由三个静态常量决定。
//   · 移动端：MOBILE_CLICK_PLAY 本就是 false（单击只显隐控件，与 YouTube 一致），
//     再把 MOBILE_DBCLICK_PLAY 关掉，它对双击便不再有任何动作，我们追加监听即可。
//   · 桌面端：单击那句 art.toggle() 是无条件的，而 toggle 由 Object.defineProperty
//     定义（writable/configurable 均为 false）改不掉。于是改成「让它照常 toggle，
//     再在微任务里原样撤销」—— 微任务跑在本轮事件处理之后、浏览器绘制之前，
//     用户看不到中间状态；真正的单击暂停推迟到双击窗口结束后再补。这也正是
//     YouTube 桌面端单击暂停略有延迟的原因：它同样要留出双击的判定时间。
// 撤销走的是公开的 play()/pause()，不碰 ArtPlayer 任何内部状态，升级不易碎。

const STEP = 10          // 每次快退/快进的秒数
const BOOST_RATE = 2     // 按住时的倍速
const BOOST_DELAY = 500  // 按住多久进入倍速
const MOVE_SLOP = 10     // 手指移动超过这么多像素就算滑动，不是按住
const SIDE_RATIO = 0.35  // 左右感应区各占的宽度，中间三成留给全屏 / 播放暂停
const RIPPLE_LINGER = 420 // 连击结束后光晕再停留多久
const TAP_MAX = 400      // 按到这么久还没松手就不算点击了（长按归倍速管）

// 三角箭头：三枚依次点亮。原始朝右（顶点在右 = 快进），快退那侧由 CSS 翻转，两边共用一份
const ARROWS = '<svg class="ges-arrows" width="42" height="15" viewBox="0 0 42 15">'
  + '<path d="M9 7.5 1 1v13z"></path>'
  + '<path d="M23 7.5 15 1v13z"></path>'
  + '<path d="M37 7.5 29 1v13z"></path>'
  + '</svg>'

const LAYER_HTML = '<div class="ges-zone ges-back"><div class="ges-badge">'
  + ARROWS + '<span class="ges-secs">10 秒</span>'
  + '</div></div>'
  + '<div class="ges-zone ges-fwd"><div class="ges-badge">'
  + ARROWS + '<span class="ges-secs">10 秒</span>'
  + '</div></div>'
  + '<div class="ges-boost"><span>' + BOOST_RATE + ' 倍速播放中</span>'
  + '<svg width="20" height="13" viewBox="0 0 20 13"><path d="M8 6.5 1 1v11z"></path><path d="M18 6.5 11 1v11z"></path></svg>'
  + '</div>'

/**
 * 给播放器挂上手势。返回摘除句柄，务必在销毁播放器前调用
 * （否则倍速可能停在 2x、定时器留到下一个视频）。
 */
export function attachPlayerGestures(art) {
  const C = art.constructor
  // 让开 ArtPlayer 的默认双击语义：移动端的播放/暂停、桌面端的全屏，
  // 都改由下面按左右分区重新分配（中间区仍保留原来的行为）。
  // 这是类级别的静态属性，不是实例的 —— 改了必须在 detach 还原，否则此后任何
  // 不带手势创建的播放器都会莫名其妙丢掉双击播放与双击全屏，且无处可查。
  const prevMobileDbl = C.MOBILE_DBCLICK_PLAY
  const prevDblFullscreen = C.DBCLICK_FULLSCREEN
  C.MOBILE_DBCLICK_PLAY = false
  C.DBCLICK_FULLSCREEN = false

  // ArtPlayer 的 clickInit 调 art.toggle() 且不 catch。视频原本暂停时它走 art.play()
  // （内部 await $video.play()），而我们紧接着在微任务里 pause() 把它撤销 —— 那个 await
  // 会以 AbortError 拒绝，没人接住，于是每次「点击恢复播放」都留一条未处理拒绝。
  // 这不是故障，是 play/pause 抢同一个媒体元素的既定行为，在这里就地收掉；
  // 只认这一种，别的 AbortError 照常冒上去。
  const onUnhandled = (e) => {
    const r = e.reason
    if (r?.name === 'AbortError' && String(r.message || '').includes('play()')) e.preventDefault()
  }
  window.addEventListener('unhandledrejection', onUnhandled)

  const { $player, $video } = art.template
  const undo = []            // proxy 注册的解绑函数
  let dead = false           // 播放器已销毁：所有异步回调据此闸断

  let $back = null, $fwd = null, $boost = null
  art.layers.add({
    name: 'gesture',
    html: LAYER_HTML,
    style: { position: 'absolute', top: '0', right: '0', bottom: '0', left: '0', pointerEvents: 'none' },
    mounted: ($el) => {
      $back = $el.querySelector('.ges-back')
      $fwd = $el.querySelector('.ges-fwd')
      $boost = $el.querySelector('.ges-boost')
    },
  })

  // ---- 点击手势 ----
  let downPlaying = false    // 按下那一刻的播放状态：撤销与延迟 toggle 都以它为准
  let lastClickAt = 0
  let runSide = ''           // 当前连击的方向（''=不在连击中）
  let runSecs = 0            // 本轮连击累计的秒数（YouTube 的 10→20→30）
  let runTimer = null
  let singleTimer = null
  let muteClickUntil = 0     // 长按松手会补一次 click，它不属于点击手势

  const isMobile = () => $player.classList.contains('art-mobile')

  // 播放状态一律读 video.paused，不能用 art.playing —— 后者附带 readyState > 2 的条件
  // （artplayer.mjs:3098），刚 seek 完或缓冲中会把「正在播」报成 false。基准一反，
  // 推迟执行的单击就会把暂停算成播放：快进完立刻点一下，视频不停反而继续播。
  // video.paused 是纯粹的播放意图，不受缓冲影响，正是这里要的语义。
  const playingNow = () => !$video.paused

  function zoneOf(clientX) {
    const rect = $player.getBoundingClientRect()
    if (!rect.width) return 'center'
    const r = (clientX - rect.left) / rect.width
    if (r < SIDE_RATIO) return 'back'
    if (r > 1 - SIDE_RATIO) return 'fwd'
    return 'center'
  }

  // setPlaying 把播放状态摆到指定值，并把它记成新的基准 —— 手势自己发起的切换
  // （中间区双击、推迟执行的单击）是「作数」的，下面的 neutralize 不该再把它撤销。
  // play() 的 promise 会被紧随其后的 pause() 打断并 reject，这里不是错误，静默即可。
  // 直接操作 video 而不是 art.play()/art.pause()：后者在 setter 里硬塞了
  // notice.show（artplayer.mjs:2990、3117），撤销那次 toggle 会在左上角闪出
  // 「暂停」「播放」字样，等于把好不容易藏住的中间状态又喊出来。
  // 原生 play/pause 事件照常派发，ArtPlayer 的控件图标同步不受影响。
  function setPlaying(playing) {
    if (dead) return
    downPlaying = playing
    try {
      const p = playing ? $video.play() : $video.pause()
      if (p && typeof p.catch === 'function') p.catch(() => {})
    } catch { /* 播放器可能已在异步间隙销毁 */ }
  }

  // neutralize 撤销 ArtPlayer 在桌面端单击时那次无条件的 toggle。
  // 放在微任务里：此刻本轮所有 click 处理器都已跑完（不依赖谁先注册），
  // 而浏览器尚未绘制，中间状态不会被看见。
  // 比的是实时的 downPlaying 而非入口处的快照：同一轮里若手势自己调过 setPlaying，
  // 基准已经跟着挪了，撤销便自动让位。
  function neutralize() {
    queueMicrotask(() => {
      if (dead) return
      if (playingNow() !== downPlaying) setPlaying(downPlaying)
      // 撤销得了播放状态，撤销不了它已经喊出的话：ArtPlayer 的 toggle 走 art.pause()，
      // 那里面同步塞了 notice.show = "暂停"。连提示一并抹掉，才是真的没发生过。
      // 顺带也让单击暂停不再弹字 —— 中央那枚玻璃圆已经把状态说清楚了，同 YouTube。
      try { art.notice.show = '' } catch { /* 已销毁 */ }
    })
  }

  function cancelSingle() {
    if (singleTimer) { clearTimeout(singleTimer); singleTimer = null }
  }

  function seekBy(delta) {
    const cur = art.currentTime
    if (!isFinite(cur)) return
    let t = cur + delta
    if (t < 0) t = 0
    // 转封装边播边转时 duration 是「已转好的长度」，会一路增长；
    // 按当下值收口，别把进度甩到还没产出的区间上去。
    const dur = art.duration
    if (isFinite(dur) && dur > 0 && t > dur - 0.5) t = Math.max(0, dur - 0.5)
    art.currentTime = t
  }

  function showRipple(side, secs) {
    const $z = side === 'fwd' ? $fwd : $back
    if (!$z) return
    const $secs = $z.querySelector('.ges-secs')
    if ($secs) $secs.textContent = `${secs} 秒`
    const $other = side === 'fwd' ? $back : $fwd
    if ($other) $other.classList.remove('is-on')
    // 连点同一侧时重放一次光晕：先摘类、强制回流、再挂回去
    $z.classList.remove('is-on')
    void $z.offsetWidth
    $z.classList.add('is-on')
  }

  function hideRipple() {
    if ($back) $back.classList.remove('is-on')
    if ($fwd) $fwd.classList.remove('is-on')
  }

  function bumpSeek() {
    runSecs += STEP
    seekBy(runSide === 'fwd' ? STEP : -STEP)
    showRipple(runSide, runSecs)
    // 末次点击后再等一个双击窗口，过了这轮连击就算结束
    if (runTimer) clearTimeout(runTimer)
    runTimer = setTimeout(() => {
      runTimer = null
      runSide = ''
      runSecs = 0
      hideRipple()
    }, C.DBCLICK_TIME + RIPPLE_LINGER)
  }

  function onClick(e) {
    if (dead || art.isLock) return
    if (Date.now() < muteClickUntil) { neutralize(); return }

    const now = Date.now()
    const within = now - lastClickAt <= C.DBCLICK_TIME
    const zone = zoneOf(e.clientX)
    lastClickAt = now
    neutralize()

    // 连击进行中且还在同一侧：继续累加
    if (within && runSide && zone === runSide) {
      cancelSingle()
      bumpSeek()
      return
    }

    // 双击的第二下
    if (within) {
      cancelSingle()
      if (zone === 'center') {
        // 中间区保留各端原本的双击语义：桌面全屏、触屏播放/暂停。
        // 元素全屏不可用的设备（iPhone）原生全屏按钮已被摘掉，这里同样退到网页全屏，
        // 免得双击把画面送进系统播放器。
        if (isMobile()) setPlaying(!downPlaying)
        else if (art.option.fullscreen) art.fullscreen = !art.fullscreen
        else art.fullscreenWeb = !art.fullscreenWeb
      } else {
        runSide = zone
        runSecs = 0
        bumpSeek()
      }
      return
    }

    // 第一下：先不动，等满双击窗口再决定
    runSide = ''
    if (!isMobile()) {
      cancelSingle()
      singleTimer = setTimeout(() => {
        singleTimer = null
        setPlaying(!downPlaying)
      }, C.DBCLICK_TIME)
    }
  }

  // ---- 长按倍速 ----
  let pressTimer = null
  let boosting = false
  let prevRate = 1
  let pressX = 0
  let pressY = 0

  function startPress(x, y) {
    downPlaying = playingNow()
    pressX = x
    pressY = y
    if (pressTimer) { clearTimeout(pressTimer); pressTimer = null }
    // 暂停时按住不加速（同 YouTube）：没有在走的画面，倍速没有意义
    if (dead || art.isLock || !downPlaying) return
    pressTimer = setTimeout(() => {
      pressTimer = null
      boosting = true
      // 同样绕开 art.playbackRate 的 setter：它会弹「速度: 2x」的 notice
      // （artplayer.mjs:3084），和我们自己那条倍速提示条重复一份
      prevRate = $video.playbackRate
      $video.playbackRate = BOOST_RATE
      if ($boost) $boost.classList.add('is-on')
    }, BOOST_DELAY)
  }

  function endPress() {
    if (pressTimer) { clearTimeout(pressTimer); pressTimer = null }
    if (!boosting) return
    boosting = false
    // 恢复的是按住之前那档，用户在设置里选的常驻倍速不受影响
    try { $video.playbackRate = prevRate } catch { /* 已销毁 */ }
    if ($boost) $boost.classList.remove('is-on')
    muteClickUntil = Date.now() + 400
  }

  const bind = (target, name, fn, opt) => undo.push(art.proxy(target, name, fn, opt))

  bind($video, 'click', onClick)

  // ---- 触屏：压掉 iOS 的长按手势，click 自己补 ----
  // iPhone 上长按画面会浮出「放置光标」的那枚放大镜，把倍速提示整个盖掉。真机逐个验过：
  // user-select: none（播放器、页面、html/body 全铺满）、拦 selectstart、选区一变就
  // removeAllRanges —— 三条都拦不住，探针里读到的选区是空选（折叠光标），压根不是文字选区。
  // 唯一能关上的闸门是在 touchstart 上 preventDefault：那是 WebKit 起这套手势的入口。
  // 代价是浏览器不再补发 click，而单击显隐控件、双击快进全挂在它上面 —— 所以松手时自己
  // 按「短按且没怎么动」补派一次，原来那条 click 链路（ArtPlayer 的 + 我们的 onClick）
  // 一个字都不用改。派发在 touchend 处理器内同步完成，仍在用户手势上下文里，play() 不会被拦。
  // 顺带的影响：手指从画面上起手不再能滚页面。播放页本就不滚，YouTube 移动端同样如此。
  let tap = null // 本次触摸还算不算点击：{x, y, at}

  bind($video, 'touchstart', (e) => {
    if (e.touches.length !== 1) { tap = null; endPress(); return } // 多指（缩放）照常放行
    e.preventDefault()
    const t = e.touches[0]
    tap = { x: t.clientX, y: t.clientY, at: Date.now() }
    startPress(t.clientX, t.clientY)
  }, { passive: false })
  bind($video, 'touchmove', (e) => {
    if (!e.touches.length) return
    const t = e.touches[0]
    if (Math.abs(t.clientX - pressX) > MOVE_SLOP || Math.abs(t.clientY - pressY) > MOVE_SLOP) {
      tap = null // 划开了就不是点击
      endPress()
    }
  }, { passive: true })
  // 这条要绑在 $video 上而不是 document：目标阶段先于下面的 endPress 跑，
  // 那时 boosting 还是 true，长按松手才不会被补成一次点击。
  bind($video, 'touchend', (e) => {
    const t = tap
    tap = null
    if (!t || boosting || Date.now() - t.at > TAP_MAX) return
    const p = e.changedTouches && e.changedTouches[0]
    $video.dispatchEvent(new MouseEvent('click', {
      bubbles: true,
      cancelable: true,
      view: window,
      clientX: p ? p.clientX : t.x,
      clientY: p ? p.clientY : t.y,
    }))
  })
  bind(document, 'touchend', endPress)
  bind(document, 'touchcancel', () => { tap = null; endPress() })

  bind($video, 'mousedown', (e) => {
    if (e.button !== 0) return
    startPress(e.clientX, e.clientY)
  })
  bind(document, 'mouseup', endPress)
  // 松手落在窗口外收不到 mouseup，靠失焦兜底，别把倍速留在 2x
  bind(window, 'blur', endPress)

  // 播放中断（缓冲耗尽、被系统暂停）时倍速没有意义，顺手收掉。
  // 绑原生 pause 而不是 art.on('video:pause')：proxy 能随 detach 一并解绑，
  // 不必依赖播放器销毁时才清空的事件总线。
  bind($video, 'pause', endPress)

  // 画面一旦被系统播放器接走（iPad 的原生全屏，或用户从系统菜单进），touchend 就回不到
  // 网页，倍速会一直卡在 2x。这里强制收掉，别把状态留在外面。
  bind($video, 'webkitbeginfullscreen', endPress)

  return function detach() {
    dead = true
    window.removeEventListener('unhandledrejection', onUnhandled)
    C.MOBILE_DBCLICK_PLAY = prevMobileDbl
    C.DBCLICK_FULLSCREEN = prevDblFullscreen
    cancelSingle()
    if (runTimer) { clearTimeout(runTimer); runTimer = null }
    if (pressTimer) { clearTimeout(pressTimer); pressTimer = null }
    if (boosting) {
      boosting = false
      try { $video.playbackRate = prevRate } catch { /* 已销毁 */ }
    }
    for (const fn of undo) {
      try { fn() } catch { /* 事件可能已随播放器一并解绑 */ }
    }
    undo.length = 0
  }
}
