// iOS 式 hero 转场验证：视频详情卡从点击封面处放大展开、关闭缩回原位（VideoDetailCard）。
// 断言开/关两段都进入 .vdc-zooming 转场态（磨砂暂停）、落定后样式清理干净且玻璃恢复、
// overlay（v-show 持久节点）二次开合复用正常、prefers-reduced-motion 下跳过转场。
// 用法: node zoom-check.mjs （需服务在跑；NL_BASE=http://localhost:5299 可换实例）
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

let passed = 0, failed = 0
const ok = (name, cond) => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}`) }
}
const dlg = '.el-dialog.vdc'
// 磨砂策略定稿（#48 三轮）：卡片自身（含 ::after）恒无 backdrop-filter，观感由遮罩
// 整页 blur(8px) 恒定垫底——动画期与落定后同一配方，任何阶段卡片都不得挂自带磨砂
const noOwnFilter = (s) => {
  const el = document.querySelector(s)
  const a = getComputedStyle(el)
  const b = getComputedStyle(el, '::after')
  return !((a.backdropFilter || '') + (a.webkitBackdropFilter || '') +
    (b.backdropFilter || '') + (b.webkitBackdropFilter || '')).includes('blur')
}

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })

// 1. 开场：点网格卡片 → 进入 .vdc-zooming 转场态（磨砂暂停），随后落定清理干净
await page.click('.v-grid .v-card >> nth=0')
await page.waitForSelector(`${dlg}.vdc-zooming`, { timeout: 2000 })
ok('开场进入 vdc-zooming 转场态', true)
ok('转场期卡片无自带磨砂（iOS 安全）', await page.evaluate(noOwnFilter, dlg))
await page.waitForFunction((s) => {
  const el = document.querySelector(s)
  return el && !el.classList.contains('vdc-zooming') && !el.style.transform
}, dlg, { timeout: 3000 })
ok('落定后转场态清理干净', true)
ok('落定后卡片仍无自带磨砂（观感恒由遮罩垫底，零切换）', await page.evaluate(noOwnFilter, dlg))
ok('遮罩整页磨砂在场', await page.evaluate(() =>
  ((getComputedStyle(document.querySelector('.el-overlay')).backdropFilter || '') +
    (getComputedStyle(document.querySelector('.el-overlay')).webkitBackdropFilter || ''))
    .includes('blur')))
ok('落定后遮罩不透明度=1', await page.evaluate(() =>
  getComputedStyle(document.querySelector('.el-overlay')).opacity === '1'))
ok('封面低清底图存在', await page.locator(`${dlg} img.vdc-art-lo`).count() === 1)

// 2. 关场：ESC → 重新进入转场态缩回来源处，结束后卸载。
//    动画期间页面须立即还给用户（反馈#42：EP 滚动锁 done() 后才解 + 遮罩拦触摸 = 半秒滑不动）
await page.keyboard.press('Escape')
// ESC 分发同步触发 animatedClose，单次 evaluate 在 360ms 动画窗口内一把采完（多次往返会 flake）
const during = await page.evaluate(async () => {
  const el = document.querySelector('.el-dialog.vdc')
  const ov = document.querySelector('.el-overlay')
  const zooming = !!el?.classList.contains('vdc-zooming')
  const lock = document.body.classList.contains('el-popup-parent--hidden')
  const pe = ov ? getComputedStyle(ov).pointerEvents : ''
  window.scrollBy(0, 300) // 锁未解时 body overflow:hidden 传播到视口，这句是空操作
  await new Promise(requestAnimationFrame)
  return { zooming, lock, pe, scrolled: window.scrollY }
})
ok('关场重新进入转场态', during.zooming)
ok('关场期滚动锁已提前解除', during.lock === false)
ok('关场期遮罩输入穿透', during.pe === 'none')
ok('关场动画期页面立即可滚动', during.scrolled > 0)
await page.waitForSelector(dlg, { state: 'detached', timeout: 5000 })
ok('关场结束卡片卸载', true)
await page.evaluate(() => window.scrollTo(0, 0))

// 2b. 关场途中重开（遮罩已放行输入，能直接点到下面的卡）：
//     新卡必须存活过迟到 done() 的 360ms 窗口，且滚动锁类补了回来
await page.click('.v-grid .v-card >> nth=0')
await page.waitForFunction((s) => {
  const el = document.querySelector(s)
  return el && !el.classList.contains('vdc-zooming')
}, dlg, { timeout: 3000 })
await page.keyboard.press('Escape')
await page.waitForSelector(`${dlg}.vdc-zooming`, { timeout: 2000 })
await page.click('.v-grid .v-card >> nth=1')
await page.waitForTimeout(600)
ok('关场途中重开新卡存活（迟到 done 未误关）',
  await page.locator(`${dlg} .vdc-title`).isVisible())
ok('重开后滚动锁恢复', await page.evaluate(() =>
  document.body.classList.contains('el-popup-parent--hidden')))
await page.keyboard.press('Escape')
await page.waitForSelector(dlg, { state: 'detached', timeout: 5000 })

// 2c. 开场补间中途立即关（反馈#42 复现步骤）：布局矩形须取开场量好的 cardRect
//     （补间期 gBCR 含中途 transform），正常缩回卸载
await page.click('.v-grid .v-card >> nth=0')
await page.waitForSelector(`${dlg}.vdc-zooming`, { timeout: 2000 })
await page.keyboard.press('Escape')
await page.waitForSelector(dlg, { state: 'detached', timeout: 5000 })
ok('开场中途立即关闭正常缩回卸载', true)

// 3. 二次开合（overlay v-show 持久节点复用）：仍正常落定 + 点遮罩关闭（before-close 路径）
await page.click('.v-grid .v-card >> nth=1')
await page.waitForSelector(dlg, { timeout: 5000 })
await page.waitForFunction((s) => {
  const el = document.querySelector(s)
  return el && !el.classList.contains('vdc-zooming') && !el.style.transform &&
    getComputedStyle(el.closest('.el-overlay')).opacity === '1'
}, dlg, { timeout: 3000 })
ok('二次开合复用 overlay 正常落定', true)
await page.mouse.click(30, 450)
await page.waitForSelector(dlg, { state: 'detached', timeout: 5000 })
ok('点遮罩关闭走转场后卸载', true)

// 4. Featured 横幅（来源比卡片大）：反馈#44——开场从横幅"大窗口"向前缩小落定（转场期
//    缩放 > 1 且带淡入凝聚），关场由卡片向后放大回横幅（缩放 > 1 且带淡出消融）。
//    先起采样循环再操作，记录整个转场期的最大缩放与最小不透明度
const sampler = async (s) => {
  const t0 = performance.now()
  let maxScale = 0, minOpacity = 2
  while (performance.now() - t0 < 700) {
    const el = document.querySelector(s)
    if (el) {
      const cs = getComputedStyle(el)
      if (cs.transform && cs.transform !== 'none')
        maxScale = Math.max(maxScale, new DOMMatrixReadOnly(cs.transform).a)
      minOpacity = Math.min(minOpacity, parseFloat(cs.opacity))
    }
    await new Promise(requestAnimationFrame)
  }
  return { maxScale, minOpacity }
}
await page.waitForSelector('.feat-item', { timeout: 15000 })
const openSampleP = page.evaluate(sampler, dlg)
await page.click('.el-carousel__item.is-active .feat-item')
const openSample = await openSampleP
ok('横幅起飞态为大窗口（缩放>1 向前缩小落定）', openSample.maxScale > 1.1)
ok('横幅开场带淡入凝聚', openSample.minOpacity < 0.9)
await page.waitForFunction((s) => {
  const el = document.querySelector(s)
  return el && !el.classList.contains('vdc-zooming') && !el.style.transform
}, dlg, { timeout: 3000 })
ok('横幅开场正常落定', true)
const closeSampleP = page.evaluate(sampler, dlg)
await page.keyboard.press('Escape')
const closeSample = await closeSampleP
ok('横幅关场向后放大回横幅（缩放>1）', closeSample.maxScale > 1.1)
ok('横幅关场带淡出消融', closeSample.minOpacity < 0.9)
await page.waitForSelector(dlg, { state: 'detached', timeout: 5000 })
ok('横幅关闭正常卸载', true)

// 5. prefers-reduced-motion：不做转场直接可见，关得掉
await page.emulateMedia({ reducedMotion: 'reduce' })
await page.click('.v-grid .v-card >> nth=0')
await page.waitForSelector(dlg, { timeout: 5000 })
await page.waitForTimeout(150)
ok('减少动态下无转场态', await page.evaluate((s) =>
  !document.querySelector(s)?.classList.contains('vdc-zooming'), dlg))
ok('减少动态下卡片直接可见', await page.locator(`${dlg} .vdc-title`).isVisible())
await page.keyboard.press('Escape')
await page.waitForSelector(dlg, { state: 'detached', timeout: 5000 })
ok('减少动态下 ESC 正常关闭', true)
await page.emulateMedia({ reducedMotion: null })

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
console.log(`\n${failed === 0 && errors.length === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 ? 0 : 1)
