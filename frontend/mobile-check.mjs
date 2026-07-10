// 移动端 UI 适配验证（390×844 触屏视口）：底部 Tab 栏导航、各页无横向溢出、
// 轮播/网格移动档生效、弹窗与抽屉收进屏幕、表格时间列隐藏、搜索条换行、
// 后台 Tab 头与存储/用户表格无内部横向溢出（操作列留在屏内）。
// 用法: node mobile-check.mjs （需服务在跑）
import { chromium } from 'playwright-core'
import { execSync } from 'node:child_process'

const BASE = 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const OUT = '../_shots'
execSync(`mkdir -p ${OUT}`, { shell: 'bash' })

const VW = 390, VH = 844

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const ctx = await browser.newContext({
  viewport: { width: VW, height: VH },
  isMobile: true, hasTouch: true, deviceScaleFactor: 2,
})
const page = await ctx.newPage()
const errors = []
const benign = [] // thumb 404（随机无封面视频，fallback 兜底）等已知良性
page.on('console', (m) => {
  if (m.type() !== 'error') return
  const t = m.text()
  const url = m.location()?.url || ''
  // thumb 404/500 = 随机抽中无封面视频或云端缩略图瞬时波动（基线已知，fallback 兜底）
  if (/api\/thumb/.test(t + url) || /ERR_ABORTED|404/.test(t)) benign.push(`[console] ${t} ${url}`)
  else errors.push(`[console] ${t} ${url}`)
})
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

let passed = 0, failed = 0
const ok = (name, cond) => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}`) }
}
// 横向溢出检查：页面内容宽不得超过视口
const noOverflow = () => page.evaluate(() =>
  document.documentElement.scrollWidth <= window.innerWidth + 1)
const shot = async (name) => {
  await page.screenshot({ path: `${OUT}/${name}.png` })
  console.log(`shot: ${name}`)
}

// 随机推荐横幅（.feat = el-carousel 根）当前激活指示器下标
const featActive = () => page.evaluate(() =>
  [...document.querySelectorAll('.feat .el-carousel__indicator')]
    .findIndex((n) => n.classList.contains('is-active')))
// 在横幅上模拟一次触屏横向滑动：dx<0 左滑（下一张），dx>0 右滑（上一张）。
// 先 mouseenter 暂停自动轮播（pauseOnHover 默认开），让方向断言不受 6s 定时器干扰。
const swipeFeat = (dx) => page.evaluate((dx) => {
  const el = document.querySelector('.feat')
  el.dispatchEvent(new MouseEvent('mouseenter', { bubbles: true }))
  const fire = (type, x) => {
    const touch = new Touch({ identifier: 0, target: el, clientX: x, clientY: 120 })
    el.dispatchEvent(new TouchEvent(type, {
      bubbles: true, cancelable: true,
      touches: type === 'touchend' ? [] : [touch], changedTouches: [touch],
    }))
  }
  fire('touchstart', 200)
  fire('touchend', 200 + dx)
}, dx)

// ---- 1. 登录页 ----
await page.goto(`${BASE}/login`)
await page.waitForSelector('.login-card')
ok('登录页无横向溢出', await noOverflow())
ok('登录卡片收进屏幕', await page.evaluate(() =>
  document.querySelector('.login-card').getBoundingClientRect().width <= window.innerWidth - 24))
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)

// ---- 2. 视频库主页 ----
await page.waitForSelector('.v-grid .v-card', { timeout: 20000 })
ok('底部 Tab 栏可见', await page.locator('.tabbar').isVisible())
ok('Tab 栏 4 项', await page.locator('.tabbar .tab-item').count() === 4)
ok('顶栏导航已隐藏', !(await page.locator('header .nav').isVisible()))
ok('视频库无横向溢出', await noOverflow())
ok('轮播移动档高度 240', await page.evaluate(() => {
  const c = document.querySelector('.feat .el-carousel__container')
  return c && Math.abs(c.offsetHeight - 240) <= 1
}))
// 随机推荐可左右滑动（el-carousel 无原生 swipe，靠 utils/swipe 补触屏手势）
{
  const n = await page.locator('.feat .el-carousel__indicator').count()
  const before = await featActive()
  await swipeFeat(-160)          // 左滑 → 下一张
  await page.waitForTimeout(500)
  const afterNext = await featActive()
  ok('随机推荐左滑切下一张', afterNext === (before + 1) % n)
  await swipeFeat(160)           // 右滑 → 回上一张
  await page.waitForTimeout(500)
  ok('随机推荐右滑切上一张', await featActive() === before)
}
ok('视频网格 2 列', await page.evaluate(() =>
  getComputedStyle(document.querySelector('.v-grid')).gridTemplateColumns.split(' ').length === 2))
ok('Tab 栏不遮内容（page 底部留白 ≥ 88px）', await page.evaluate(() =>
  parseFloat(getComputedStyle(document.querySelector('.page')).paddingBottom) >= 88))
await page.waitForTimeout(800)
await shot('m01-video-home')

// ---- 3. 视频详情卡片 ----
await page.click('.v-grid .v-card >> nth=0')
await page.waitForSelector('.el-dialog.vdc', { timeout: 8000 })
ok('详情卡片收进屏幕', await page.evaluate(() => {
  const r = document.querySelector('.el-dialog.vdc').getBoundingClientRect()
  return r.width <= window.innerWidth - 20 && r.left >= 0
}))
ok('详情操作按钮可见', await page.locator('.vdc-ops button:has-text("立即播放")').isVisible())
await page.waitForTimeout(400)
await shot('m02-video-detail')
await page.keyboard.press('Escape')
await page.waitForSelector('.el-dialog.vdc', { state: 'detached', timeout: 5000 })

// ---- 4. 底部 Tab 切照片墙 ----
await page.click('.tabbar .tab-item:has-text("照片墙")')
await page.waitForURL(/\/library\/photos/)
await page.waitForSelector('.photo-grid .cell', { timeout: 20000 })
ok('Tab 切换到照片墙', true)
ok('照片墙无横向溢出', await noOverflow())
ok('照片网格 3 列', await page.evaluate(() =>
  getComputedStyle(document.querySelector('.photo-grid')).gridTemplateColumns.split(' ').length === 3))
// 照片墙随机推荐同样可左右滑动
{
  const n = await page.locator('.feat .el-carousel__indicator').count()
  const before = await featActive()
  await swipeFeat(-160)          // 左滑 → 下一张
  await page.waitForTimeout(500)
  const afterNext = await featActive()
  ok('照片随机推荐左滑切下一张', afterNext === (before + 1) % n)
  await swipeFeat(160)           // 右滑 → 回上一张
  await page.waitForTimeout(500)
  ok('照片随机推荐右滑切上一张', await featActive() === before)
}
await page.waitForTimeout(800)
await shot('m03-photos-home')

// 灯箱开合（触屏点击）
await page.click('.photo-grid .cell >> nth=0')
await page.waitForSelector('.pswp', { timeout: 20000 })
ok('灯箱打开', true)
for (let i = 0; i < 5 && await page.locator('.pswp').count(); i++) { // 开场动画期 Escape 可能被吞，重试
  await page.keyboard.press('Escape')
  await page.waitForTimeout(500)
}
ok('灯箱关闭', await page.locator('.pswp').count() === 0)

// ---- 5. 文件管理 ----
await page.click('.tabbar .tab-item:has-text("文件")')
await page.waitForURL(/\/files/)
await page.waitForSelector('.el-table', { timeout: 15000 })
ok('文件页无横向溢出', await noOverflow())
ok('移动端隐藏修改时间列', await page.evaluate(() =>
  ![...document.querySelectorAll('.el-table th')].some((th) => th.textContent.includes('修改时间'))))
// 选中一行 → 批量操作按钮出现且工具栏换行不溢出
await page.click('.el-table .el-checkbox >> nth=1')
await page.waitForSelector('.toolbar button:has-text("删除")', { timeout: 5000 })
ok('选中后批量按钮出现且不溢出', await noOverflow())
await page.waitForTimeout(400)
await shot('m04-files')

// 传输任务抽屉收进屏幕（页面同时挂着上传/文本抽屉，按 aria-label 定位）
const taskDrawer = '.el-drawer[aria-label="传输任务"]'
await page.click('.toolbar button[title="传输任务"]')
await page.waitForSelector(taskDrawer, { timeout: 5000 })
await page.waitForTimeout(400)
ok('任务抽屉收进屏幕', await page.evaluate((sel) => {
  const r = document.querySelector(sel).getBoundingClientRect()
  return r.width <= window.innerWidth - 20 && r.left >= 0
}, taskDrawer))
await shot('m05-tasks-drawer')
await page.keyboard.press('Escape')
await page.waitForSelector(taskDrawer, { state: 'hidden', timeout: 5000 }).catch(() => {})

// ---- 6. 搜索页 ----
await page.click('.tabbar .tab-item:has-text("搜索")')
await page.waitForURL(/\/search/)
await page.waitForSelector('.bar input')
ok('搜索输入框独占整行', await page.evaluate(() => {
  const bar = document.querySelector('.bar')
  const input = bar.querySelector('.el-input')
  return input.getBoundingClientRect().width > bar.clientWidth * 0.85
}))
await page.fill('.bar input', '电影')
await page.click('.bar button:has-text("搜索")')
await page.waitForSelector('.results .result', { timeout: 10000 })
ok('搜索结果无横向溢出', await noOverflow())
await page.waitForTimeout(300)
await shot('m06-search')

// ---- 7. 播放页 ----
await page.goto(`${BASE}/library/video?dir=${encodeURIComponent('/本地存储/电影')}`)
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })
await page.click('.v-grid .v-card >> nth=0')
await page.waitForSelector('.el-dialog.vdc', { timeout: 8000 })
// 主播放按钮：有续播进度时文案为「继续观看」，否则「立即播放」（反馈#25）
await page.click('.el-dialog.vdc .vdc-play')
await page.waitForURL(/\/play\//, { timeout: 8000 })
await page.waitForSelector('.art-video-player', { timeout: 15000 })
ok('播放器挂载', true)
ok('播放页无横向溢出', await noOverflow())
// 沉浸式聚焦：顶栏与底部 Tab 栏在播放页隐藏，页面自管留白（顶部 < 68px 库页值）
ok('播放页隐藏顶栏', !(await page.locator('header.topbar').isVisible()))
ok('播放页隐藏底部 Tab 栏', !(await page.locator('.tabbar').isVisible()))
ok('播放页顶部留白收窄（< 68px）', await page.evaluate(() =>
  parseFloat(getComputedStyle(document.querySelector('.play-page')).paddingTop) < 68))
// 播放器在头部之下垂直居中：其上、下留白大致相当（差 ≤ 视口高 8%）
ok('播放器垂直居中', await page.evaluate(() => {
  const r = document.querySelector('.player').getBoundingClientRect()
  const above = r.top, below = window.innerHeight - r.bottom
  return above > 20 && below > 20 && Math.abs(above - below) <= window.innerHeight * 0.08
}))
await page.waitForTimeout(800)
await shot('m07-play')

// ---- 8. 后台（用户下拉入口 + 弹窗收进屏幕） ----
// 播放页沉浸模式隐藏顶栏，先回库页让 user-chip 重新可见再进后台
await page.goto(`${BASE}/library/video`)
await page.waitForSelector('.user-chip', { state: 'visible', timeout: 10000 })
await page.click('.user-chip')
await page.click('.el-dropdown-menu__item:has-text("后台管理")')
await page.waitForURL(/@admin/)
await page.waitForSelector('.el-tabs', { timeout: 10000 })
ok('后台页无横向溢出', await noOverflow())
// Tab 头五项挤进窄屏不出现左右滚动箭头（导航内容宽 ≤ 可视宽）
ok('后台 Tab 头无溢出', await page.evaluate(() => {
  const wrap = document.querySelector('.el-tabs__nav-wrap')
  const nav = document.querySelector('.el-tabs__nav')
  return nav.scrollWidth <= wrap.clientWidth + 1
}))
// 可见 tab 内表格的内容宽不得超过表格可视宽（否则单元格内部要横向滑动，操作列被挤出屏外）
const tableFits = () => page.evaluate(() => {
  const tbl = [...document.querySelectorAll('.el-table')].find((t) => t.offsetParent !== null)
  if (!tbl) return false
  const inner = tbl.querySelector('table')?.scrollWidth || tbl.scrollWidth
  return inner <= tbl.clientWidth + 1
})
await page.click('.el-tabs__item:has-text("存储管理")')
await page.waitForTimeout(400)
ok('存储表格无内部横向溢出', await tableFits())
await page.click('.el-tabs__item:has-text("用户管理")')
await page.waitForTimeout(400)
ok('用户表格无内部横向溢出', await tableFits())
await page.click('.el-tabs__item:has-text("存储管理")')
await page.waitForTimeout(300)
await page.click('button:has-text("添加存储")')
await page.waitForSelector('.el-overlay .el-dialog', { timeout: 5000 })
await page.waitForTimeout(400)
ok('存储弹窗收进屏幕', await page.evaluate(() => {
  const dlgs = [...document.querySelectorAll('.el-dialog')]
  const r = dlgs[dlgs.length - 1].getBoundingClientRect()
  return r.width <= window.innerWidth - 20 && r.left >= 0 && r.right <= window.innerWidth
}))
await shot('m08-admin-storage-dialog')

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
if (benign.length) console.log(`（另有 ${benign.length} 条已知良性：thumb 404/翻页中断）`)
console.log(`\n${failed === 0 && errors.length === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 ? 0 : 1)
