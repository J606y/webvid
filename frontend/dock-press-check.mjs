// 移动端 dock 滑块按压放大的验收（2026-09-19）。
//
// 验的是三件事，缺一不可：
//   1. 按住时滑块真的涨了（scale 1.06），不是只加了个 class 而样式没落上；
//   2. 涨出来的那一圈没有溢出 dock 的圆角（.tabbar 有 5px 内边距，正好吃得下）；
//   3. 拖动期间 transform 仍是瞬时跟手 —— 改动把整条 transition:none 换成了「只留 scale」，
//      写错顺序就会让滑块追着 0.35s 缓动走，手感变成「拖不动」。
//
// 用法: NL_BASE=http://localhost:5243 node dock-press-check.mjs （需服务在跑）
import { chromium } from 'playwright-core'
import { mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const HERE = dirname(fileURLToPath(import.meta.url))
const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = process.env.NL_CHROME || 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const SHOTS = join(HERE, '_dock-shots')
const IPHONE_UA = 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1'

mkdirSync(SHOTS, { recursive: true })

let pass = 0
let fail = 0
const check = (name, ok, detail = '') => {
  if (ok) { pass++; console.log(`  ✓ ${name}`) }
  else { fail++; console.log(`  ✗ ${name}${detail ? ` —— ${detail}` : ''}`) }
}

const browser = await chromium.launch({ executablePath: CHROME, headless: process.env.NL_HEAD !== '1' })
const ctx = await browser.newContext({
  viewport: { width: 390, height: 844 },
  userAgent: IPHONE_UA,
  hasTouch: true,
  isMobile: true,
  deviceScaleFactor: 3,
})
const page = await ctx.newPage()
page.on('pageerror', (e) => { fail++; console.log(`  ✗ 页面异常：${e.message}`) })

// Playwright 的 touchscreen 只有 tap()，按住不放得自己造 TouchEvent。
// 处理器读的是 e.touches[0].clientX，所以 Touch 对象必须真造出来，不能只给个空 init。
const touch = (type, dx = 0) => page.evaluate(([type, dx]) => {
  const el = document.querySelector('.tabbar')
  const r = el.getBoundingClientRect()
  const t = new Touch({
    identifier: 1,
    target: el,
    clientX: r.x + r.width * 0.15 + dx, // 落在第一项上，离得够远才看得出拖动
    clientY: r.y + r.height / 2,
  })
  const held = type === 'touchend' ? [] : [t]
  el.dispatchEvent(new TouchEvent(type, {
    touches: held, targetTouches: held, changedTouches: [t], bubbles: true, cancelable: true,
  }))
}, [type, dx])

const pill = () => page.evaluate(() => {
  const el = document.querySelector('.tab-pill')
  const bar = document.querySelector('.tabbar')
  const cs = getComputedStyle(el)
  const a = el.getBoundingClientRect()
  const b = bar.getBoundingClientRect()
  return {
    scale: cs.scale,
    shadow: cs.boxShadow,
    transition: cs.transitionProperty,
    grabbed: el.classList.contains('is-grabbed'),
    x: a.x, w: a.width,
    // 四个方向各「探出」多少：正数=越过了 dock 的边，负数=还在里面
    out: Math.max(b.x - a.x, (a.x + a.width) - (b.x + b.width), b.y - a.y, (a.y + a.height) - (b.y + b.height)),
  }
})

try {
  await page.goto(`${BASE}/login`)
  await page.fill('input[placeholder="用户名"]', 'admin')
  await page.fill('input[placeholder="密码"]', 'admin123')
  await page.click('button:has-text("登 录")')
  await page.waitForURL(`${BASE}/library/video`)
  await page.waitForSelector('.tabbar', { timeout: 15000 })
  await page.waitForTimeout(600) // 等滑块就位（movePill 的淡入）

  console.log('\n移动端 dock 按压')

  // 长按 dock 会弹 Safari 的链接预览（每一项都是 router-link）。Chromium 只能验属性有没有
  // 落到元素上，「浮层还弹不弹」只有 iPhone 说了算 —— -webkit-touch-callout 在这里不被支持时
  // 读出来是空字符串，据此区分「没写」和「写了但这个浏览器不认」。
  const guard = await page.evaluate(() => {
    const cs = getComputedStyle(document.querySelector('.tab-item'))
    return { callout: cs.getPropertyValue('-webkit-touch-callout'), select: cs.webkitUserSelect || cs.userSelect }
  })
  check('dock 项禁掉长按选区', guard.select === 'none', `user-select=${guard.select}`)
  check('dock 项禁掉 iOS 链接预览', guard.callout === 'none' || guard.callout === '',
    `-webkit-touch-callout=${guard.callout}`)
  if (guard.callout === '') console.log('    （本浏览器不认 -webkit-touch-callout，这条只能上 iPhone 确认）')

  const idle = await pill()
  check('静止时不涨', idle.scale === 'none' || idle.scale === '1', `scale=${idle.scale}`)

  await touch('touchstart')
  await page.waitForTimeout(350) // 等 0.22s 的缓动跑完
  const held = await pill()
  check('按住时滑块涨到 1.06', held.scale === '1.06' || held.scale === '1.06 1.06', `scale=${held.scale}`)
  check('按住时投影加深', held.shadow !== idle.shadow, `按住 "${held.shadow}"`)
  check('涨大后没溢出 dock', held.out <= 0.5, `最多探出 ${held.out.toFixed(1)}px（dock 内边距 5px）`)
  await page.screenshot({ path: join(SHOTS, 'dock-pressed.png') })

  // 拖动：transform 必须瞬时跟手。列表里留着 transform 就说明改动写漏了。
  await touch('touchmove', 120)
  await page.waitForTimeout(30) // 远小于 0.35s：有缓动的话这会儿还没走到位
  const moved = await pill()
  check('拖动时 transform 不参与缓动', !moved.transition.includes('transform'), `transition-property=${moved.transition}`)
  check('拖动时滑块立刻跟到手指处', Math.abs(moved.x - idle.x - 120) < 6, `位移 ${(moved.x - idle.x).toFixed(1)}px，期望 120`)
  check('拖动期间仍保持涨大', moved.scale === '1.06' || moved.scale === '1.06 1.06', `scale=${moved.scale}`)
  await page.screenshot({ path: join(SHOTS, 'dock-dragging.png') })

  await touch('touchend')
  await page.waitForTimeout(600)
  const rest = await pill()
  check('松手落回原尺寸', rest.scale === 'none' || rest.scale === '1', `scale=${rest.scale}`)
  check('松手后不再是抓取态', !rest.grabbed)
  await page.screenshot({ path: join(SHOTS, 'dock-released.png') })
} catch (err) {
  fail++
  console.error('\n验收中断：', err.stack || err.message)
} finally {
  await browser.close()
}

console.log(`\n${'='.repeat(60)}\n通过 ${pass} 项，失败 ${fail} 项`)
process.exit(fail ? 1 : 0)
