// 反馈#29 格式探测共享 + 播放页占位验证：
//   ① 非直连视频「立即播放」→ 播放页立即有反馈（player / detecting 占位 / unsupported 之一，
//      绝不停在空白），不再看着像「点了没反应」。
//   ② 详情卡的 /video/info 与播放页共用一次探测：card→play 全程只发一次 /video/info。
//   ③ 直连视频进播放页不闪「检测格式中…」占位（起手即 player）。
// 用法: node detect-check.mjs （需服务在跑，库里要有非直连视频如 .mkv/.ts）
import { chromium } from 'playwright-core'

const BASE = 'http://localhost:5243'
const CHROME = 'C:/Program Files/Google/Chrome/Application/chrome.exe'

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

// 统计每条逻辑路径的 /video/info 请求次数（判「共享一次探测」）
const infoHits = []
page.on('request', (r) => {
  const u = new URL(r.url())
  if (u.pathname === '/api/video/info') infoHits.push(u.searchParams.get('path'))
})

let passed = 0, failed = 0
const ok = (name, cond) => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}`) }
}
const dlg = '.el-dialog.vdc'

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })

// 找一个非直连格式的视频（.mkv/.ts/.avi… /video/info 会经历真实探测）
const NONDIRECT = /\.(mkv|ts|avi|flv|wmv|rmvb|m2ts|mpg|mpeg|3gp)$/i
const cards = page.locator('.v-grid .v-card')
const n = await cards.count()
let target = -1
for (let i = 0; i < n; i++) {
  const nm = await cards.nth(i).locator('.v-name').getAttribute('title')
  if (nm && NONDIRECT.test(nm)) { target = i; break }
}

if (target < 0) {
  console.log('  ⏭️ 库中无非直连视频，跳过探测占位断言（仍验直连不闪占位）')
} else {
  // ① 打开详情卡，等它进入「检测格式中…」——趁探测在途立刻点「立即播放」
  infoHits.length = 0
  await cards.nth(target).click()
  await page.waitForSelector(dlg, { timeout: 5000 })
  // 详情卡探测中标记（badge 未出、显示「检测格式中…」）
  await page.click(`${dlg} .vdc-play`) // 稳定类：有续播进度时文案变「继续观看」，勿按文本
  await page.waitForURL(/\/play\//, { timeout: 5000 })

  // 播放页首帧必须有可见反馈：player / detecting 占位 / unsupported 三者之一，不得空白
  const hasFeedback = await page.waitForFunction(() => {
    return !!document.querySelector('.play-page .player, .play-page .detecting, .play-page .unsupported')
  }, null, { timeout: 3000 }).then(() => true).catch(() => false)
  ok('播放页立即有反馈（不空白）', hasFeedback)

  // 探测最终收敛为 player 或 unsupported（detecting 只是过渡）
  await page.waitForFunction(() => {
    return !!document.querySelector('.play-page .player, .play-page .unsupported')
  }, null, { timeout: 25000 }).catch(() => {})

  // ② card→play 全程该路径只探一次（共享 utils/videoInfo 记忆）
  const cnt = infoHits.filter(Boolean).length
  ok(`card→play 共享探测（/video/info 命中 ${cnt} 次 ≤1）`, cnt <= 1)

  await page.goBack()
  await page.waitForURL(`${BASE}/library/video`)
  await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })
}

// ③ 直连视频（.mp4 等）进播放页不经「检测格式中…」占位
const DIRECT = /\.(mp4|m4v|mov|webm)$/i
let dt = -1
for (let i = 0; i < n; i++) {
  const nm = await cards.nth(i).locator('.v-name').getAttribute('title')
  if (nm && DIRECT.test(nm)) { dt = i; break }
}
if (dt < 0) {
  console.log('  ⏭️ 库中无直连视频，跳过直连断言')
} else {
  await cards.nth(dt).click()
  await page.waitForSelector(dlg, { timeout: 5000 })
  await page.click(`${dlg} .vdc-play`) // 稳定类：有续播进度时文案变「继续观看」，勿按文本
  await page.waitForURL(/\/play\//, { timeout: 5000 })
  // 直连起手即 player：不应出现 detecting 占位
  const sawDetecting = await page.locator('.play-page .detecting').count()
  ok('直连视频不闪检测占位', sawDetecting === 0)
}

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
console.log(`\n${failed === 0 && errors.length === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 ? 0 : 1)
