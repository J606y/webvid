// 滚动位置保持验证：列表页 keep-alive + router scrollBehavior——
// 打开视频再返回时不退回顶部、列表数据不重拉（主页随机网格内容不变）、
// body.infuse-mode 随激活切换、teleport 弹窗切走自动关闭。
// 用法: node scroll-check.mjs （需服务在跑）
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

let mediaListReqs = 0, fsListReqs = 0
page.on('request', (r) => {
  if (r.url().includes('/api/media/list')) mediaListReqs++
  if (r.url().includes('/api/fs/list')) fsListReqs++
})

let passed = 0, failed = 0
const ok = (name, cond, extra = '') => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}${extra ? ` (${extra})` : ''}`) }
}
const scrollY = () => page.evaluate(() => window.scrollY)
const gridNames = () => page.$$eval('.v-grid .v-name', (els) => els.map((e) => e.textContent))
const hasInfuse = () => page.evaluate(() => document.body.classList.contains('infuse-mode'))
// 找到当前视口内完整可见的网格卡片序号（滚动后直接 nth=0 可能在视口外）
const visibleCardIdx = () => page.evaluate(() => {
  const cards = [...document.querySelectorAll('.v-grid .v-card')]
  return cards.findIndex((c) => {
    const r = c.getBoundingClientRect()
    return r.top >= 80 && r.bottom <= innerHeight
  })
})

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })

// ---- 1. 主页：滚下去 → 悬停播放图标进播放页 → 返回 ----
console.log('1. 主页 滚动→播放→返回')
await page.evaluate(() => window.scrollTo(0, 900))
await page.waitForTimeout(300)
const y1 = await scrollY()
const names1 = await gridNames()
const reqs1 = mediaListReqs
const idx1 = await visibleCardIdx()
ok('主页可滚动且有可见卡片', y1 > 500 && idx1 >= 0, `y=${y1} idx=${idx1}`)
await page.hover(`.v-grid .v-card >> nth=${idx1}`)
await page.click(`.v-grid .v-card >> nth=${idx1} >> .play`)
await page.waitForURL(/\/play\//, { timeout: 5000 })
await page.waitForTimeout(800)
ok('播放页 body 无 infuse-mode', !(await hasInfuse()))
await page.click('.play-page .back')
await page.waitForURL(`${BASE}/library/video`, { timeout: 5000 })
await page.waitForTimeout(500)
const y1b = await scrollY()
ok('返回后滚动位置保持', Math.abs(y1b - y1) < 50, `期望≈${y1} 实际=${y1b}`)
ok('返回后随机网格内容不变（未重拉）', JSON.stringify(await gridNames()) === JSON.stringify(names1))
ok('返回后未发起新的 media/list 请求', mediaListReqs === reqs1, `${reqs1}→${mediaListReqs}`)
ok('返回后 body 恢复 infuse-mode', await hasInfuse())

// ---- 2. 查看全部：进入回顶，深滚→详情卡片播放→返回 ----
console.log('2. 查看全部 深滚→详情卡片播放→返回')
await page.evaluate(() => window.scrollTo(0, 0))
await page.click('.see-all')
await page.waitForURL(/all=1/)
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })
await page.waitForTimeout(400)
ok('进入查看全部时回到顶部', (await scrollY()) === 0, `y=${await scrollY()}`)
await page.evaluate(() => window.scrollTo(0, 2200))
await page.waitForTimeout(400)
const y2 = await scrollY()
const names2 = await gridNames()
const idx2 = await visibleCardIdx()
// 深度阈值只要求"确实滚开了"（>200）：位置保持的效力在下方 ±50 对比，
// 与绝对深度无关；开发库样本少时（如 /test 换 TG 收藏夹后仅 30 余项）滚不到 1500
ok('查看全部可深滚', y2 > 200 && idx2 >= 0, `y=${y2} idx=${idx2}`)
await page.click(`.v-grid .v-card >> nth=${idx2}`)
await page.waitForSelector('.el-dialog.vdc', { timeout: 5000 })
// 稳定类 .vdc-play：有续播进度时按钮文案变「继续观看」，has-text 会失配（同 mobile-check）
await page.click('.el-dialog.vdc .vdc-play')
await page.waitForURL(/\/play\//, { timeout: 5000 })
await page.waitForTimeout(800)
ok('播放页无残留详情弹窗', !(await page.locator('.el-dialog.vdc').isVisible().catch(() => false)))
const reqs2 = mediaListReqs
await page.click('.play-page .back')
await page.waitForURL(/all=1/, { timeout: 5000 })
await page.waitForTimeout(500)
const y2b = await scrollY()
ok('返回后滚动位置保持', Math.abs(y2b - y2) < 50, `期望≈${y2} 实际=${y2b}`)
ok('返回后列表内容不变（未重拉）', JSON.stringify(await gridNames()) === JSON.stringify(names2))
ok('返回后未发起新的 media/list 请求', mediaListReqs === reqs2, `${reqs2}→${mediaListReqs}`)

// ---- 3. 站内导航仍正常：返回按钮回主页应回顶并重新渲染主页 ----
console.log('3. 查看全部→返回按钮回主页')
await page.click('.lib-head .back-btn')
await page.waitForURL(`${BASE}/library/video`)
await page.waitForSelector('.see-all', { timeout: 15000 })
await page.waitForTimeout(400)
ok('回主页后在顶部', (await scrollY()) < 5, `y=${await scrollY()}`)

// ---- 4. teleport 弹窗守卫：开着详情卡片按浏览器后退切到别的页面，弹窗不残留 ----
// （modal 遮罩挡住导航栏，点击切页本就不可能；只有浏览器后退/前进能带着弹窗切走）
console.log('4. 开着详情卡片按浏览器后退切走')
await page.click('.nav a:has-text("文件")')
await page.waitForURL(/\/files/)
await page.click('.nav a:has-text("视频库")')
await page.waitForURL(`${BASE}/library/video`)
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })
await page.click('.v-grid .v-card >> nth=0')
await page.waitForSelector('.el-dialog.vdc', { timeout: 5000 })
await page.goBack()
await page.waitForURL(/\/files/)
await page.waitForTimeout(500)
ok('后退到文件页后详情弹窗不残留', !(await page.locator('.el-dialog.vdc').isVisible().catch(() => false)))
ok('文件页 body 无 infuse-mode', !(await hasInfuse()))

// ---- 5. 文件页：深滚→进播放页→返回，保持滚动位置且不重拉目录 ----
console.log('5. 文件页 深滚→播放→返回')
// 直达一个必然存在的视频目录（列表视图表格）。旧路径写死在 /test 深层目录，
// 07-10 起 /test 换成 Telegram 收藏夹（扁平只读）后目录 404 卡死整套检查；
// 改用本地样本目录——文件不够滚动时下方 y5 分支会优雅跳过滚动位断言
const VDIR = '/files/本地存储/电影'
await page.goto(BASE + VDIR.split('/').map((s, i) => i < 2 ? s : encodeURIComponent(s)).join('/'))
await page.waitForSelector('.el-table__row', { timeout: 15000 })
await page.waitForTimeout(400)
await page.evaluate(() => window.scrollTo(0, 1600))
await page.waitForTimeout(400)
const y5 = await scrollY()
if (y5 > 800) {
  const fsReqs = fsListReqs
  // 点当前视口内可见的一行视频
  const rowIdx = await page.evaluate(() => {
    const rows = [...document.querySelectorAll('.el-table__row')]
    return rows.findIndex((r) => {
      const b = r.getBoundingClientRect()
      return b.top >= 80 && b.bottom <= innerHeight && /\.(mp4|mkv|webm|mov|m4v)/i.test(r.textContent)
    })
  })
  ok('文件页可深滚且有可见视频行', rowIdx >= 0, `y=${y5} rowIdx=${rowIdx}`)
  await page.click(`.el-table__row >> nth=${rowIdx}`)
  await page.waitForURL(/\/play\//, { timeout: 5000 })
  await page.waitForTimeout(800)
  await page.click('.play-page .back')
  await page.waitForURL(/\/files/, { timeout: 5000 })
  await page.waitForTimeout(500)
  const y5b = await scrollY()
  ok('返回文件页滚动位置保持', Math.abs(y5b - y5) < 50, `期望≈${y5} 实际=${y5b}`)
  ok('返回后未发起新的 fs/list 请求', fsListReqs === fsReqs, `${fsReqs}→${fsListReqs}`)
} else {
  console.log(`  ⏭️ 该目录不足以滚动 (y=${y5})，跳过`)
}

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
console.log(`\n${failed === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 ? 0 : 1)
