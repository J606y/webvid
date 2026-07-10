// 反馈#16 视频详情二级卡片验证：点卡片弹详情（缩略图+信息）、立即播放/文件位置跳转、
// 悬停播放图标与 Featured 立即播放按钮仍直达播放页。
// 用法: node detail-check.mjs （需服务在跑）
import { chromium } from 'playwright-core'
import { execSync } from 'node:child_process'

const BASE = 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const OUT = '../_shots'
execSync(`mkdir -p ${OUT}`, { shell: 'bash' })

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

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })

// 1. 点网格卡片 → 弹详情卡片
await page.click('.v-grid .v-card >> nth=0')
await page.waitForSelector(dlg, { timeout: 5000 })
ok('点击网格卡片弹出详情', true)
ok('详情有缩略图区', await page.locator(`${dlg} .vdc-art`).count() === 1)
ok('详情有标题', (await page.locator(`${dlg} .vdc-title`).textContent())?.trim().length > 0)
ok('详情有文件名行', await page.locator(`${dlg} .vdc-row:has-text("文件名")`).count() === 1)
ok('详情有所在目录行', await page.locator(`${dlg} .vdc-row:has-text("所在目录")`).count() === 1)
ok('详情有修改时间行', await page.locator(`${dlg} .vdc-row:has-text("修改时间")`).count() === 1)
// 播放策略 badge（/video/info 返回后出现，本地文件秒回）
await page.waitForFunction((sel) => {
  const badges = [...document.querySelectorAll(sel + ' .vdc-badge')]
  return badges.some((b) => /原生直连|转封装播放|转码播放|暂不支持播放/.test(b.textContent))
}, dlg, { timeout: 20000 })
ok('播放策略徽标出现', true)
await page.waitForTimeout(600)
await page.screenshot({ path: `${OUT}/15-video-detail.png` })
console.log('shot: 15-video-detail')

// 2. 立即播放 → 播放页
await page.click(`${dlg} button:has-text("立即播放")`)
await page.waitForURL(/\/play\//, { timeout: 5000 })
ok('立即播放跳转播放页', true)
await page.goBack()
await page.waitForURL(`${BASE}/library/video`)
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })

// 3. Escape 关闭 + 货架卡片同样弹详情
const shelf = page.locator('.shelf-card').first()
if (await shelf.count()) {
  await shelf.click()
  await page.waitForSelector(dlg, { timeout: 5000 })
  ok('货架卡片弹出详情', true)
  await page.keyboard.press('Escape')
  await page.waitForSelector(dlg, { state: 'detached', timeout: 5000 })
  ok('Escape 关闭详情', true)
} else {
  console.log('  ⏭️ 无货架卡片，跳过')
}

// 4. 文件位置 → 文件管理
await page.click('.v-grid .v-card >> nth=0')
await page.waitForSelector(dlg, { timeout: 5000 })
await page.click(`${dlg} button:has-text("文件位置")`)
await page.waitForURL(/\/files\//, { timeout: 5000 })
ok('文件位置跳转文件管理', true)

// 5. 悬停播放图标仍直达播放页（不弹卡片）
await page.goto(`${BASE}/library/video`)
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })
await page.hover('.v-grid .v-card >> nth=0')
await page.click('.v-grid .v-card >> nth=0 >> .play')
await page.waitForURL(/\/play\//, { timeout: 5000 })
ok('悬停播放图标直达播放页', await page.locator(dlg).count() === 0)

// 6. Featured 横幅点击弹详情、立即播放按钮直达（e2e 04-play 依赖）
await page.goto(`${BASE}/library/video`)
await page.waitForSelector('.feat-item', { timeout: 15000 })
await page.click('.feat-item >> visible=true')
await page.waitForSelector(dlg, { timeout: 5000 })
ok('Featured 横幅点击弹详情', true)
await page.keyboard.press('Escape')
await page.waitForSelector(dlg, { state: 'detached', timeout: 5000 })
await page.click('.hero-btn >> visible=true')
await page.waitForURL(/\/play\//, { timeout: 5000 })
ok('Featured 立即播放按钮直达播放页', true)

// 7. 莫奈取色：本地样片封面（ffmpeg 截帧必有）加载后信息区应染上主色渐变
await page.goto(`${BASE}/library/video?dir=${encodeURIComponent('/本地存储/电影')}`)
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })
await page.click('.v-grid .v-card >> nth=0')
await page.waitForSelector(dlg, { timeout: 5000 })
try {
  await page.waitForFunction((sel) => {
    const el = document.querySelector(sel + ' .vdc-info')
    return el && el.style.background.includes('linear-gradient')
  }, dlg, { timeout: 10000 })
  ok('封面莫奈取色染上玻璃', true)
} catch { ok('封面莫奈取色染上玻璃', false) }
await page.waitForTimeout(500)
await page.screenshot({ path: `${OUT}/15b-video-detail-tint.png` })
console.log('shot: 15b-video-detail-tint')

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
console.log(`\n${failed === 0 && errors.length === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 ? 0 : 1)
