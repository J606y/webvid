// 断点续播验证：播放→跳转离开→重进从原位续播；最近播放货架/详情卡进度条；
// 「从头播放」重置。用法: node progress-check.mjs（需服务在跑）
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const MP4 = '/本地存储/电影/星际漫游.mp4'   // direct 播放，seek/续播稳定
const TS = '/本地存储/电影/转码样片/转播录像.ts' // HLS remux 续播

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })

const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

let pass = 0, fail = 0
function check(name, ok, extra = '') {
  if (ok) { pass++; console.log(`  ✓ ${name} ${extra}`) }
  else { fail++; console.log(`  ✗ ${name} ${extra}`) }
}
const enc = (p) => p.split('/').map(encodeURIComponent).join('/')
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function waitPlaying(minT, timeout = 30000) {
  try {
    await page.waitForFunction(
      (t) => { const v = document.querySelector('video'); return v && !v.paused && v.currentTime > t },
      minT, { timeout })
    return true
  } catch { return false }
}
const curTime = () => page.evaluate(() => document.querySelector('video')?.currentTime ?? -1)
const duration = () => page.evaluate(() => { const v = document.querySelector('video'); return v && isFinite(v.duration) ? v.duration : 0 })

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)

// —— 1. direct mp4：播放→seek→离开→重进续播 ——
console.log('== direct mp4 续播')
await page.goto(`${BASE}/play${enc(MP4)}`)
check('起播', await waitPlaying(0.5))
const dur = await duration()
const target = Math.max(8, Math.min(15, Math.round(dur * 0.4))) // 落在片中，避开片尾归零
await page.evaluate((t) => { document.querySelector('video').currentTime = t }, target)
await waitPlaying(target - 0.5) // seek 后续播，触发 video:seeked 上报
await sleep(600) // 等上报落库
// 离开到库页，再重进播放页
await page.goto(`${BASE}/library/video`)
await sleep(300)
await page.goto(`${BASE}/play${enc(MP4)}`)
check('重进起播', await waitPlaying(0.3))
await sleep(1200) // 等续播定位生效（direct 在 ready 里 seek）
const resumed = await curTime()
check(`从原位续播（≈${target}s）`, Math.abs(resumed - target) <= 4, `实际 ${resumed.toFixed(1)}s`)

// —— 2. 最近播放货架进度条 ——
console.log('== 最近播放货架')
await page.goto(`${BASE}/library/video`)
await page.waitForSelector('.shelf', { timeout: 10000 })
// 定位到「最近播放」section（其 h2 文本），只在该货架内找卡片（最近添加货架无进度数据）
const shelfProg = await page.evaluate((name) => {
  const sections = [...document.querySelectorAll('.shelf')]
  const sec = sections.find((s) => s.querySelector('.shelf-head h2')?.textContent?.includes('最近播放'))
  if (!sec) return { section: false }
  const card = [...sec.querySelectorAll('.v-card')].find((c) => c.querySelector('.v-name')?.title?.includes(name))
  if (!card) return { section: true, found: false }
  const span = card.querySelector('.prog-bar span')
  return { section: true, found: true, hasBar: !!span, width: span ? parseFloat(span.style.width) : 0 }
}, '星际漫游')
check('存在「最近播放」货架', shelfProg.section)
check('最近播放出现该视频', shelfProg.found)
check('卡片有进度条且宽度>0', shelfProg.hasBar && shelfProg.width > 0, `width=${shelfProg.width}%`)

// —— 3. 详情卡「继续观看」+ 进度行 + 从头播放 ——
console.log('== 详情卡续播 UI')
await page.evaluate((name) => {
  const sections = [...document.querySelectorAll('.shelf')]
  const sec = sections.find((s) => s.querySelector('.shelf-head h2')?.textContent?.includes('最近播放'))
  const card = [...(sec || document).querySelectorAll('.v-card')].find((c) => c.querySelector('.v-name')?.title?.includes(name))
  card?.click()
}, '星际漫游')
await page.waitForSelector('.vdc-body', { timeout: 8000 })
await sleep(800) // 等 /media/progress 回填
const dlg = await page.evaluate(() => {
  const txt = document.querySelector('.vdc-ops')?.innerText || ''
  return {
    hasResumeBtn: txt.includes('继续观看'),
    hasRestartBtn: txt.includes('从头播放'),
    hasProgRow: !!document.querySelector('.vdc-prog'),
    progText: [...document.querySelectorAll('.vdc-row')].some((r) => r.innerText.includes('观看进度')),
  }
})
check('按钮显示「继续观看」', dlg.hasResumeBtn)
check('有「从头播放」按钮', dlg.hasRestartBtn)
check('大图有进度条', dlg.hasProgRow)
check('信息区有「观看进度」行', dlg.progText)

// 点「从头播放」→ restart 语义，不续播
await page.click('.vdc-ops button:has-text("从头播放")')
await page.waitForURL(/\/play\//, { timeout: 8000 })
check('起播', await waitPlaying(0.3))
await sleep(1000)
const fromStart = await curTime()
check('从头播放未跳到续播点', fromStart < 6, `实际 ${fromStart.toFixed(1)}s`)

// —— 4. HLS .ts 续播 ——
console.log('== HLS .ts 续播')
await page.goto(`${BASE}/play${enc(TS)}`)
check('起播', await waitPlaying(0.5))
await page.evaluate(() => { document.querySelector('video').currentTime = 12 })
await waitPlaying(11.5)
await sleep(600)
await page.goto(`${BASE}/library/video`)
await sleep(300)
await page.goto(`${BASE}/play${enc(TS)}`)
check('重进起播', await waitPlaying(0.3))
await sleep(1500)
const tsResume = await curTime()
check('从原位续播（≈12s）', Math.abs(tsResume - 12) <= 4, `实际 ${tsResume.toFixed(1)}s`)

// 汇总
console.log(`\n通过 ${pass} / ${pass + fail}`)
const meaningful = errors.filter((e) => !e.includes('ERR_ABORTED'))
if (meaningful.length) { console.log('控制台错误:'); meaningful.forEach((e) => console.log('  ' + e)) }
else console.log('控制台零错误（ERR_ABORTED 离页良性除外）')
await browser.close()
process.exit(fail || meaningful.length ? 1 : 0)
