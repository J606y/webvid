// 播放手势验收：双击左右 ±10 秒、按住 2 倍速。
//
// 为什么不直接在项目页面上点：手势的难点全在「与 ArtPlayer 自带点击逻辑抢同一个
// click 事件」上 —— 桌面端那次无条件的 toggle 撤销得干不干净、连击累加会不会被
// ArtPlayer 的双击计数带偏。这些要能反复精确复现，就不能掺进登录、云盘拉流这些变量。
// 所以这里起一个最小页面：真的 ArtPlayer、真的 <video>、真的手势模块，别的一概不要。
//
// 用法：
//   node gesture-check.mjs
// 可选：
//   NL_CHROME  浏览器可执行文件路径，默认系统 Edge
//   NL_FFMPEG  ffmpeg 路径，默认走 PATH
//   NL_HEAD    设为 1 则开着窗口跑，肉眼看动效
import { chromium } from 'playwright-core'
import { createServer } from 'node:http'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const HERE = dirname(fileURLToPath(import.meta.url))
const CHROME = process.env.NL_CHROME || 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const FFMPEG = process.env.NL_FFMPEG || 'ffmpeg'
const SAMPLE = join(HERE, '_gesture-sample.mp4')
const SHOTS = join(HERE, '_gesture-shots')
const IPHONE_UA = 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1'

if (!existsSync(CHROME)) {
  console.error(`找不到浏览器：${CHROME}\n用 NL_CHROME 指定 Edge/Chrome 的 exe 路径`)
  process.exit(1)
}

// 造样本：120 秒、带秒数刻度的测试图，快进 10 秒肉眼可验证。
// 有声轨是必要的 —— 倍速在纯静音轨上不改变任何可观测量，容易把「没生效」看成「生效了」。
if (!existsSync(SAMPLE)) {
  console.log('造测试视频…')
  try {
    execFileSync(FFMPEG, [
      '-v', 'error', '-y',
      '-f', 'lavfi', '-i', 'testsrc2=size=960x540:rate=25:duration=120',
      '-f', 'lavfi', '-i', 'sine=frequency=440:duration=120',
      '-c:v', 'libx264', '-preset', 'ultrafast', '-pix_fmt', 'yuv420p',
      '-c:a', 'aac', '-shortest', '-movflags', '+faststart',
      SAMPLE,
    ], { stdio: 'inherit' })
  } catch (err) {
    console.error(`ffmpeg 造样本失败：${err.message}\n用 NL_FFMPEG 指定 ffmpeg 路径`)
    process.exit(1)
  }
}
mkdirSync(SHOTS, { recursive: true })

const PAGE = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
  html,body{margin:0;background:#000;height:100%}
  #app{width:100%;max-width:960px;aspect-ratio:16/9;margin:0 auto}
  ${playerCss()}
</style>
<script src="/artplayer.js"></script></head>
<body><div id="app"></div>
<script type="module">
  import { attachPlayerGestures } from '/playerGestures.js'
  // 诊断桩：事件只反映「状态真的变了」，反映不了「调了但没产生变化」。
  // 记调用本身，才能分清定时器没跑、还是跑了却调成反方向。只存在于验收页面。
  window.__calls = []
  const _play = HTMLMediaElement.prototype.play
  const _pause = HTMLMediaElement.prototype.pause
  HTMLMediaElement.prototype.play = function (...a) {
    window.__calls.push('play(' + (Date.now() % 100000) + ')')
    return _play.apply(this, a)
  }
  HTMLMediaElement.prototype.pause = function (...a) {
    window.__calls.push('pause(' + (Date.now() % 100000) + ')')
    return _pause.apply(this, a)
  }
  const art = new Artplayer({
    container: '#app', url: '/sample.mp4', volume: 0, muted: true,
    theme: '#ff0000', backdrop: false, setting: true, playbackRate: true,
    fullscreen: true, hotkey: true, autoSize: false, autoplay: false,
    // 塞满设置项：面板被裁是「选项多」才暴露的，菜单只有两行的话这条用例等于没验
    settings: Array.from({ length: 8 }, (_, i) => ({ name: 'stub' + i, html: '占位选项 ' + (i + 1) })),
  })
  window.__art = art
  art.on('ready', () => { window.__detach = attachPlayerGestures(art); window.__ready = true })
</script></body></html>`

// 整份 player.css 原样注入：验收跑的必须是即将上线的那份 CSS，不能在这儿另抄一份。
// 旧版是从 Play.vue 的 scoped 块里抠一段、再把 :deep() 剥掉 —— 那样只验得了规则本身
// 对不对，验不了真实页面里的作用域能不能命中，网页全屏整批样式失配就是从这个缺口漏过去的。
// 样式搬到全局后选择器自带 .art-video-player，这里一个字都不用改写，作用域一并进了验收。
function playerCss() {
  return readFileSync(join(HERE, 'src/assets/player.css'), 'utf8')
}

const files = {
  '/artplayer.js': [join(HERE, 'node_modules/artplayer/dist/artplayer.js'), 'text/javascript'],
  '/playerGestures.js': [join(HERE, 'src/utils/playerGestures.js'), 'text/javascript'],
  '/sample.mp4': [SAMPLE, 'video/mp4'],
}
const server = createServer((req, res) => {
  const path = req.url.split('?')[0]
  if (path === '/') {
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' })
    return res.end(PAGE)
  }
  const hit = files[path]
  if (!hit) { res.writeHead(404); return res.end('no') }
  const body = readFileSync(hit[0])
  res.writeHead(200, { 'content-type': hit[1], 'content-length': body.length, 'accept-ranges': 'bytes' })
  res.end(body)
})
await new Promise((r) => server.listen(0, '127.0.0.1', r))
const BASE = `http://127.0.0.1:${server.address().port}`

let pass = 0
let fail = 0
function check(name, ok, detail = '') {
  if (ok) { pass++; console.log(`  ✓ ${name}`) }
  else { fail++; console.log(`  ✗ ${name}${detail ? ` —— ${detail}` : ''}`) }
}

const browser = await chromium.launch({
  executablePath: CHROME,
  headless: process.env.NL_HEAD !== '1',
  args: ['--autoplay-policy=no-user-gesture-required'],
})
try {
  await runDesktop()
  await runMobile()
} catch (err) {
  fail++
  console.error('\n验收中断：', err.stack || err.message)
} finally {
  await browser.close()
  server.close()
}

console.log(`\n${'='.repeat(60)}\n通过 ${pass} 项，失败 ${fail} 项`)
process.exit(fail ? 1 : 0)

// ── 页面准备 ────────────────────────────────────────────────────────────────
async function open(ctx) {
  const page = await ctx.newPage()
  page.on('pageerror', (e) => { fail++; console.log(`  ✗ 页面异常：${e.message}`) })
  await page.goto(BASE, { waitUntil: 'domcontentloaded', timeout: 30000 })
  await page.waitForFunction(() => window.__ready === true, { timeout: 30000 })
  await page.waitForFunction(() => document.querySelector('video')?.readyState >= 1, { timeout: 30000 })
  // 捕获阶段记下每次点击真正命中了谁：手势只绑在 $video 上，若某一击被
  // .art-state 之类的浮层截走，分支判断会整个错位，而光看结果分辨不出来
  await page.evaluate(() => {
    window.__clicks = []
    document.addEventListener('click', (e) => {
      const t = e.target
      const cls = (t.className || '').toString().split(' ')[0]
      window.__clicks.push(`${t.tagName}${cls ? '.' + cls : ''}+${Date.now() % 100000}`)
    }, true)
  })
  return page
}

// 同 state()：跑测试的 try 块在文件中部，只有函数声明能被提升到那里用
function clearClicks(page) {
  return page.evaluate(() => { window.__clicks = [] })
}

function dumpClicks(page) {
  return page.evaluate(() => (window.__clicks || []).join('  '))
}

// 把播放位置摆到中段再测，免得快退撞见 0 秒下限、快进撞见片尾收口。
//
// 开头那段静置不能省：手势有跨用例的余温 —— 连击窗口 300 ms，光晕的 runTimer 还要再
// 留 420 ms 才清掉 runSide。上一个用例的末击若离得太近，下一个用例的第一击会被正确地
// 认成「同侧连击」续上去（10→20→30→40）。那是产品该有的行为，是用例之间没隔离。
// 顺带清掉上一条 notice：ArtPlayer 的提示要挂两秒，会被下一条断言读成本用例弹的。
async function reset(page, at = 60, playing = true) {
  await page.waitForTimeout(800)
  await page.evaluate(async ([t, p]) => {
    const v = document.querySelector('video')
    v.currentTime = t
    if (p) await v.play().catch(() => {})
    else v.pause()
    window.__art.notice.show = ''
    await new Promise((r) => setTimeout(r, 120))
  }, [at, playing])
}

// 必须是函数声明而非 const 箭头函数：跑测试的 try 块在文件中部，
// 那时 const 还没求值，只有函数声明能被提升到那里用
function state(page) {
  return page.evaluate(() => ({
    t: document.querySelector('video').currentTime,
    paused: document.querySelector('video').paused,
    rate: document.querySelector('video').playbackRate,
    secs: document.querySelector('.ges-zone.is-on .ges-secs')?.textContent || '',
    onSide: document.querySelector('.ges-back.is-on') ? 'back'
      : document.querySelector('.ges-fwd.is-on') ? 'fwd' : '',
    boost: !!document.querySelector('.ges-boost.is-on'),
    // ArtPlayer 在 play/pause/playbackRate 的 setter 里硬塞了 notice。手势撤销那次
    // toggle 若走它们，左上角会闪出「暂停」「播放」，等于把中间状态喊出来。
    // 只在 art-notice-show 时算数 —— 内层文本不会被清空，仅靠它判断会误报。
    notice: document.querySelector('.art-video-player.art-notice-show')
      ? (document.querySelector('.art-notice-inner')?.textContent || '').trim() : '',
  }))
}

// skin 量的是「样式有没有真的落到元素上」，而不是样式写得对不对。
// 判据取 backdrop-filter：ArtPlayer 自己一处没用，量到有值就只可能来自我们这份 CSS，
// 换成尺寸类属性反而分不清是谁给的。顺带量长按禁选 —— iPhone 上它一失效就弹选词放大镜。
// 必须是函数声明：跑测试的 try 块在文件中部，箭头函数那时还没求值。
async function skin(page, where) {
  const s = await page.evaluate(() => {
    const css = (sel) => {
      const el = document.querySelector(sel)
      return el ? getComputedStyle(el) : null
    }
    const ctrl = css('.art-controls .art-control')
    const boost = css('.ges-boost')
    return {
      blur: ctrl ? (ctrl.backdropFilter || ctrl.webkitBackdropFilter || 'none') : '(没有控件)',
      radius: ctrl ? ctrl.borderRadius : '',
      select: boost ? (boost.userSelect || boost.webkitUserSelect || 'auto') : '(没有提示条)',
    }
  })
  check(`${where}：控件玻璃胶囊生效`, s.blur !== 'none' && !s.blur.startsWith('('), `backdrop-filter = "${s.blur}"，圆角 ${s.radius}`)
  check(`${where}：长按不起选区`, s.select === 'none', `user-select = "${s.select}"`)
}

async function box(page) {
  const b = await page.locator('.art-video-player').boundingBox()
  if (!b) throw new Error('取不到播放器位置')
  return b
}

// 双击某一侧。ratio 是横向位置占播放器宽度的比例
async function dbl(page, ratio, gap = 90) {
  const b = await box(page)
  const x = b.x + b.width * ratio
  const y = b.y + b.height / 2
  await page.mouse.click(x, y)
  await page.waitForTimeout(gap)
  await page.mouse.click(x, y)
  await page.waitForTimeout(120)
}

async function tapTwice(page, ratio, gap = 90) {
  const b = await box(page)
  const x = b.x + b.width * ratio
  const y = b.y + b.height / 2
  await page.touchscreen.tap(x, y)
  await page.waitForTimeout(gap)
  await page.touchscreen.tap(x, y)
  await page.waitForTimeout(120)
}

// ── 桌面端 ──────────────────────────────────────────────────────────────────
async function runDesktop() {
  console.log('\n桌面端（鼠标）')
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  const page = await open(ctx)

  // 双击右侧快进 10 秒，且不改变播放状态 —— 这一条是撤销逻辑的靶心：
  // ArtPlayer 会在第一击时把视频暂停掉，手势层必须在绘制前原样还回去。
  await reset(page, 60, true)
  const a0 = await state(page)
  await dbl(page, 0.85)
  const a1 = await state(page)
  check('双击右侧快进 10 秒', Math.abs(a1.t - a0.t - 10) < 1.5, `${a0.t.toFixed(1)} → ${a1.t.toFixed(1)}`)
  check('双击快进不改变播放状态', a1.paused === false, a1.paused ? '被暂停了' : '')
  check('快进光晕出现在右侧', a1.onSide === 'fwd', `实际 "${a1.onSide}"`)
  check('徽章显示 10 秒', a1.secs === '10 秒', `实际 "${a1.secs}"`)
  check('快进不弹播放器自带提示', a1.notice === '', `冒出了 "${a1.notice}"`)
  await page.screenshot({ path: join(SHOTS, 'desktop-forward.png') })

  // 连击累加：第三击要落在前一击的双击窗口内，所以坐标先算好，
  // 中间不能再插 await box() —— 那点往返足够把第三击挤出 300 ms 判定窗口。
  await reset(page, 60, true)
  const rb = await box(page)
  const rx = rb.x + rb.width * 0.85
  const ry = rb.y + rb.height / 2
  await clearClicks(page)
  await page.mouse.click(rx, ry)
  await page.waitForTimeout(90)
  await page.mouse.click(rx, ry)
  await page.waitForTimeout(90)
  await page.mouse.click(rx, ry)
  await page.waitForTimeout(150)
  const b1 = await state(page)
  if (b1.secs !== '20 秒') console.log('    命中:', await dumpClicks(page))
  check('连击累加到 20 秒', b1.secs === '20 秒', `实际 "${b1.secs}"`)
  check('连击共快进 20 秒', Math.abs(b1.t - 60 - 20) < 2, `到 ${b1.t.toFixed(1)}`)
  await page.screenshot({ path: join(SHOTS, 'desktop-accumulate.png') })

  // 双击左侧快退
  await reset(page, 60, true)
  const c0 = await state(page)
  await dbl(page, 0.12)
  const c1 = await state(page)
  check('双击左侧快退 10 秒', Math.abs(c0.t - c1.t - 10) < 1.5, `${c0.t.toFixed(1)} → ${c1.t.toFixed(1)}`)
  check('快退光晕出现在左侧', c1.onSide === 'back', `实际 "${c1.onSide}"`)
  await page.screenshot({ path: join(SHOTS, 'desktop-backward.png') })

  // 单击仍要能暂停，只是推迟到双击窗口之后
  await reset(page, 60, true)
  // 失败时要能看出是「推迟的那次 toggle 没跑」还是「跑了但方向反了」，
  // 光看最终 paused 分不出来，所以记一条事件流水
  await page.evaluate(() => {
    window.__log = []
    const v = document.querySelector('video')
    const t0 = Date.now()
    for (const n of ['play', 'pause', 'seeking', 'seeked', 'ratechange']) {
      v.addEventListener(n, () => window.__log.push(`${n}+${Date.now() - t0}ms`))
    }
  })
  const b = await box(page)
  await clearClicks(page)
  await page.evaluate(() => { window.__calls = [] })
  await page.mouse.click(b.x + b.width * 0.5, b.y + b.height / 2)
  await page.waitForTimeout(500)
  const d1 = await state(page)
  if (d1.paused !== true) {
    console.log('    事件流水:', await page.evaluate(() => window.__log.join('  →  ')) || '(一个都没有)')
    console.log('    命中:', await dumpClicks(page))
    console.log('    play/pause 调用:', await page.evaluate(() => window.__calls.join('  ')) || '(一次都没有)')
  }
  check('单击中间照常暂停', d1.paused === true, '没停下来')
  // 再单击一次应当恢复播放
  await page.mouse.click(b.x + b.width * 0.5, b.y + b.height / 2)
  await page.waitForTimeout(500)
  check('再次单击恢复播放', (await state(page)).paused === false)

  // 按住倍速
  await reset(page, 60, true)
  await page.mouse.move(b.x + b.width * 0.5, b.y + b.height / 2)
  await page.mouse.down()
  await page.waitForTimeout(750)
  const e1 = await state(page)
  check('按住进入 2 倍速', e1.rate === 2, `实际 ${e1.rate}x`)
  check('倍速提示条出现', e1.boost === true, '没显示')
  check('倍速不弹播放器自带提示', e1.notice === '', `冒出了 "${e1.notice}"`)
  await page.screenshot({ path: join(SHOTS, 'desktop-boost.png') })
  await page.mouse.up()
  await page.waitForTimeout(250)
  const e2 = await state(page)
  check('松手恢复原速', e2.rate === 1, `实际 ${e2.rate}x`)
  check('松手收起提示条', e2.boost === false)
  check('按住松手不会误暂停', e2.paused === false, '被暂停了')

  // 按住前若已选了常驻倍速，松手要还回那一档而不是一律回 1x
  await page.evaluate(() => { window.__art.playbackRate = 1.5 })
  await reset(page, 60, true)
  await page.mouse.move(b.x + b.width * 0.5, b.y + b.height / 2)
  await page.mouse.down()
  await page.waitForTimeout(750)
  const f1 = await state(page)
  await page.mouse.up()
  await page.waitForTimeout(250)
  const f2 = await state(page)
  check('常驻 1.5x 时按住仍进 2 倍速', f1.rate === 2, `实际 ${f1.rate}x`)
  check('松手还回 1.5x 而非 1x', f2.rate === 1.5, `实际 ${f2.rate}x`)
  await page.evaluate(() => { window.__art.playbackRate = 1 })

  // 暂停状态下按住不该加速
  await reset(page, 60, false)
  await page.mouse.move(b.x + b.width * 0.5, b.y + b.height / 2)
  await page.mouse.down()
  await page.waitForTimeout(750)
  check('暂停时按住不进倍速', (await state(page)).rate === 1)
  await page.mouse.up()
  await page.waitForTimeout(150)

  // 片头片尾的边界：0 秒处快退不该越界，片尾快进不该冲过时长
  await reset(page, 3, true)
  await dbl(page, 0.12)
  const g1 = await state(page)
  check('片头快退收口在 0 秒', g1.t >= 0 && g1.t < 1, `到 ${g1.t.toFixed(2)}`)
  const dur = await page.evaluate(() => document.querySelector('video').duration)
  await reset(page, dur - 3, true)
  await dbl(page, 0.85)
  const g2 = await state(page)
  check('片尾快进不越过时长', g2.t <= dur, `${g2.t.toFixed(2)} / ${dur.toFixed(2)}`)

  // 皮肤作用域：样式在全局 player.css，靠 .art-video-player 双写命中。点网页全屏后
  // ArtPlayer 会把播放器整块搬到 <body>，这里要确认搬走之后规则依然生效 ——
  // 从前那批 scoped 样式正是死在这一步，一进全屏只剩裸文本。
  await reset(page, 60, true)
  await skin(page, '常态')
  await page.evaluate(() => { window.__art.fullscreenWeb = true })
  await page.waitForTimeout(400)
  const moved = await page.evaluate(() => document.querySelector('.art-video-player').parentElement === document.body)
  check('网页全屏把播放器搬到 body', moved, '没搬走，这条用例就证明不了什么：ArtPlayer 换实现了，回去核对 FULLSCREEN_WEB_IN_BODY')
  await skin(page, '网页全屏')
  await page.screenshot({ path: join(SHOTS, 'desktop-fullscreen-web.png') })
  await page.evaluate(() => { window.__art.fullscreenWeb = false })
  await page.waitForTimeout(400)

  // 离页摘除：正按住倍速时销毁播放器，倍率必须还回去
  await reset(page, 60, true)
  await page.mouse.move(b.x + b.width * 0.5, b.y + b.height / 2)
  await page.mouse.down()
  await page.waitForTimeout(750)
  await page.evaluate(() => { window.__detach() })
  await page.waitForTimeout(150)
  check('按住时摘除手势会还回倍率', (await state(page)).rate === 1, `实际 ${(await state(page)).rate}x`)
  await page.mouse.up()

  await ctx.close()
}

// ── 触屏 ────────────────────────────────────────────────────────────────────
async function runMobile() {
  console.log('\n触屏（iPhone UA）')
  const ctx = await browser.newContext({
    viewport: { width: 390, height: 844 },
    userAgent: IPHONE_UA,
    hasTouch: true,
    isMobile: true,
    deviceScaleFactor: 3,
  })
  const page = await open(ctx)
  const mobileClass = await page.evaluate(() => document.querySelector('.art-video-player').classList.contains('art-mobile'))
  check('ArtPlayer 识别为移动端', mobileClass, '没加 .art-mobile，分支判断会走错')

  await reset(page, 60, true)
  const a0 = await state(page)
  await tapTwice(page, 0.85)
  const a1 = await state(page)
  check('双击右侧快进 10 秒', Math.abs(a1.t - a0.t - 10) < 1.5, `${a0.t.toFixed(1)} → ${a1.t.toFixed(1)}`)
  check('双击不再触发播放/暂停', a1.paused === false, '被暂停了（MOBILE_DBCLICK_PLAY 没让开）')
  await page.screenshot({ path: join(SHOTS, 'mobile-forward.png') })

  await reset(page, 60, true)
  await tapTwice(page, 0.12)
  const b1 = await state(page)
  check('双击左侧快退 10 秒', Math.abs(60 - b1.t - 10) < 1.5, `到 ${b1.t.toFixed(1)}`)
  await page.screenshot({ path: join(SHOTS, 'mobile-backward.png') })

  // 单击只显隐控件，不该暂停
  await reset(page, 60, true)
  const bx = await box(page)
  await page.touchscreen.tap(bx.x + bx.width * 0.5, bx.y + bx.height / 2)
  await page.waitForTimeout(500)
  check('单击不暂停（只显隐控件）', (await state(page)).paused === false, '被暂停了')

  // 中间双击保留播放/暂停
  await reset(page, 60, true)
  await tapTwice(page, 0.5)
  check('中间双击切换播放/暂停', (await state(page)).paused === true, '没切换')

  // 横竖屏切换：分区是每次点击按当前画面重算的，不是进页面时定死
  await reset(page, 60, true)
  await page.setViewportSize({ width: 844, height: 390 })
  await page.waitForTimeout(400) // 等 ArtPlayer 的 resize 与布局落定
  const r0 = await state(page)
  await tapTwice(page, 0.85)
  const r1 = await state(page)
  check('横屏后双击右侧仍快进 10 秒', Math.abs(r1.t - r0.t - 10) < 1.5, `${r0.t.toFixed(1)} → ${r1.t.toFixed(1)}`)
  check('横屏后光晕仍在右侧', r1.onSide === 'fwd', `实际 "${r1.onSide}"`)
  await page.screenshot({ path: join(SHOTS, 'mobile-landscape.png') })

  await reset(page, 60, true)
  await tapTwice(page, 0.12)
  const r2 = await state(page)
  check('横屏后双击左侧仍快退 10 秒', Math.abs(60 - r2.t - 10) < 1.5, `到 ${r2.t.toFixed(1)}`)

  // 转回竖屏，确认不是单向有效
  await reset(page, 60, true)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.waitForTimeout(400)
  const r3 = await state(page)
  await tapTwice(page, 0.85)
  check('转回竖屏后仍快进 10 秒', Math.abs((await state(page)).t - r3.t - 10) < 1.5)

  // 竖屏进网页全屏、再转横屏：全屏靠 position:fixed + 内联 width/height:100%，
  // 转屏后必须跟着视口重算，否则画面还按竖屏那套尺寸铺，沉浸感就没了。
  await page.setViewportSize({ width: 390, height: 844 })
  await page.waitForTimeout(400)
  const fsBox = () => page.evaluate(() => {
    const r = document.querySelector('.art-video-player').getBoundingClientRect()
    return { x: r.x, y: r.y, w: r.width, h: r.height, vw: window.innerWidth, vh: window.innerHeight }
  })
  await page.evaluate(() => { window.__art.fullscreenWeb = true })
  await page.waitForTimeout(400)
  const f1 = await fsBox()
  check('竖屏网页全屏铺满视口', Math.abs(f1.w - f1.vw) < 2 && Math.abs(f1.h - f1.vh) < 2,
    `${f1.w.toFixed(0)}×${f1.h.toFixed(0)}，视口 ${f1.vw}×${f1.vh}`)
  await page.setViewportSize({ width: 844, height: 390 })
  await page.waitForTimeout(600)
  const f2 = await fsBox()
  check('竖屏进全屏后转横屏仍铺满视口', Math.abs(f2.w - f2.vw) < 2 && Math.abs(f2.h - f2.vh) < 2,
    `${f2.w.toFixed(0)}×${f2.h.toFixed(0)}，视口 ${f2.vw}×${f2.vh}`)
  await page.screenshot({ path: join(SHOTS, 'mobile-fsweb-rotate.png') })
  await page.evaluate(() => { window.__art.fullscreenWeb = false })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.waitForTimeout(400)

  // 设置面板不能顶出画面：手机上播放器只有 ~206px 高（390 屏减页边距后 16:9），
  // ArtPlayer 给的 180px 定值加上控件行就溢出，被播放器的 overflow 裁掉半截。
  await reset(page, 60, true)
  const pbox = await box(page)
  await page.locator('.art-control-setting').tap()
  await page.waitForTimeout(400)
  const panel = await page.locator('.art-settings').boundingBox()
  check('设置面板不顶出播放器上沿', !!panel && panel.y >= pbox.y - 1,
    panel ? `面板顶 ${panel.y.toFixed(0)} < 播放器顶 ${pbox.y.toFixed(0)}，超出 ${(pbox.y - panel.y).toFixed(0)}px` : '面板没打开')
  check('设置面板底边落在画面内', !!panel && panel.y + panel.height <= pbox.y + pbox.height + 1,
    panel ? `面板底 ${(panel.y + panel.height).toFixed(0)} > 播放器底 ${(pbox.y + pbox.height).toFixed(0)}` : '面板没打开')
  await page.screenshot({ path: join(SHOTS, 'mobile-settings.png') })
  await page.locator('.art-control-setting').tap() // 收起，别影响后面的用例
  await page.waitForTimeout(300)

  // 注入的是整份 player.css（含 @media），窄屏那档收窄尺寸真的会生效，量得到才算验过。
  // 竖屏 390px 走移动端一号；横屏 844px 超出 768px 断点自然回桌面尺寸，那是 CSS 的确定行为。
  await skin(page, '触屏')
  const m = await page.evaluate(() => ({
    w: getComputedStyle(document.querySelector('.art-controls .art-control')).minWidth,
    h: getComputedStyle(document.querySelector('.art-video-player')).getPropertyValue('--art-control-height').trim(),
  }))
  check('竖屏控件胶囊收到 36px', m.w === '36px', `实际 ${m.w}`)
  // ArtPlayer 在 .art-video-player.art-mobile 里把这个变量压成 38px，且注入在产物 CSS 之后。
  // 量到 44 说明我们那条三写选择器的特异性够高；量到 38 就是被它打平打赢了。
  check('竖屏控件行高压回 44px', m.h === '44px', `实际 "${m.h}"`)

  await ctx.close()
}
