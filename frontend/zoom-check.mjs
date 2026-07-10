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
const hasBlur = (s) => {
  const cs = getComputedStyle(document.querySelector(s))
  return ((cs.backdropFilter || cs.webkitBackdropFilter || '')).includes('blur')
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
ok('转场期卡片磨砂已暂停', await page.evaluate(hasBlur, dlg) === false)
await page.waitForFunction((s) => {
  const el = document.querySelector(s)
  return el && !el.classList.contains('vdc-zooming') && !el.style.transform
}, dlg, { timeout: 3000 })
ok('落定后转场态清理干净', true)
ok('落定后磨砂玻璃恢复', await page.evaluate(hasBlur, dlg))
ok('落定后遮罩不透明度=1', await page.evaluate(() =>
  getComputedStyle(document.querySelector('.el-overlay')).opacity === '1'))
ok('封面低清底图存在', await page.locator(`${dlg} img.vdc-art-lo`).count() === 1)

// 2. 关场：ESC → 重新进入转场态缩回来源处，结束后卸载
await page.keyboard.press('Escape')
await page.waitForSelector(`${dlg}.vdc-zooming`, { timeout: 2000 })
ok('关场重新进入转场态', true)
await page.waitForSelector(dlg, { state: 'detached', timeout: 5000 })
ok('关场结束卡片卸载', true)

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

// 4. Featured 横幅（来源比卡片宽）：起飞态缩放不得超过 1——按真实横幅矩形起飞会比
//    落定还大、四边探出屏幕，观感像从屏幕外飞入/飞出（#41 二轮），须走 92% 虚拟矩形浮出。
//    先起采样循环再点击，记录整个转场期的最大缩放
await page.waitForSelector('.feat-item', { timeout: 15000 })
const maxScaleP = page.evaluate(async (s) => {
  const t0 = performance.now()
  let mx = 0
  while (performance.now() - t0 < 700) {
    const el = document.querySelector(s)
    if (el) {
      const tr = getComputedStyle(el).transform
      if (tr && tr !== 'none') mx = Math.max(mx, new DOMMatrixReadOnly(tr).a)
    }
    await new Promise(requestAnimationFrame)
  }
  return mx
}, dlg)
await page.click('.feat-item >> visible=true')
const maxScale = await maxScaleP
ok('横幅起飞态缩放 ≤ 1（不从屏幕外飞入）', maxScale > 0 && maxScale <= 1.01)
await page.waitForFunction((s) => {
  const el = document.querySelector(s)
  return el && !el.classList.contains('vdc-zooming') && !el.style.transform
}, dlg, { timeout: 3000 })
ok('横幅开场正常落定', true)
await page.keyboard.press('Escape')
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
