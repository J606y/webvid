// 后台「传输任务」文件清单的翻页记忆：翻到第 3 页，关掉再打开还在第 3 页；
// 换个任务从头开始；记住的页码若已不存在（清单变短）回落到最后一页。
// 任务与文件清单全部打桩——要翻页得有 100 条以上文件，跑真实转存造这么多太重
//（真实链路由 admin-tasks-check.mjs 覆盖）。
// 用法: NL_BASE=http://localhost:5299 node tasks-page-memory-check.mjs （需服务在跑）
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const SHOTS = process.env.TEMP || '.'

let passed = 0, failed = 0
const ok = (name, cond, extra = '') => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}${extra ? ` (${extra})` : ''}`) }
}

// 两个任务：1「剧集」250 个文件（3 页），2「电影」150 个（2 页）
const totals = { 1: 250, 2: 150 }
const task = (id, name) => ({
  id, name, state: 'done', owner: 1, done: 1024, total: 1024, speed: 0,
  cur_file: '', error: '', active_files: 0,
  files: { total: totals[id], done: totals[id], skipped: 0, error: 0, running: 0, pending: 0 },
})

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

const json = (data) => ({
  status: 200, contentType: 'application/json',
  body: JSON.stringify({ code: 200, data, message: 'success' }),
})
await page.route('**/api/tasks', (route) => route.fulfill(json([task(1, '剧集'), task(2, '电影')])))
await page.route('**/api/tasks/*/files*', (route) => {
  const u = new URL(route.request().url())
  const id = u.pathname.split('/').at(-2)
  const total = totals[id]
  const offset = Number(u.searchParams.get('offset') || 0)
  const limit = Number(u.searchParams.get('limit') || 100)
  const items = []
  for (let i = offset; i < Math.min(offset + limit, total); i++) {
    items.push({ path: `第一季/第 ${i + 1} 集.mkv`, size: 1024, done: 1024, state: 'done', error: '' })
  }
  route.fulfill(json({
    items, total,
    counts: { total, pending: 0, running: 0, done: total, skipped: 0, error: 0 },
  }))
})

const drawer = page.locator('.el-drawer')
const firstFile = () => drawer.locator('.f-table .el-table__row:visible').first()
const activePage = () => drawer.locator('.el-pager li.is-active').innerText()
const openTask = async (name) => {
  await page.locator(`.task-table .el-table__row:visible:has-text("${name}")`)
    .locator('button:has-text("查看文件")').click()
  await drawer.waitFor({ state: 'visible', timeout: 8000 })
  await firstFile().waitFor({ timeout: 8000 })
}
const closeDrawer = async () => {
  await drawer.locator('.el-drawer__close-btn').click()
  await drawer.waitFor({ state: 'hidden', timeout: 8000 })
}

await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)
await page.goto(`${BASE}/@admin`)
await page.click('.el-tabs__item:has-text("传输任务")')
await page.locator('.task-table .el-table__row:visible').first().waitFor({ timeout: 10000 })

console.log('1. 打开清单：从第 1 页开始')
await openTask('剧集')
ok('首页第一条是第 1 集', (await firstFile().innerText()).includes('第 1 集'), await firstFile().innerText())
ok('分页器停在第 1 页', (await activePage()) === '1')

console.log('2. 翻到第 3 页')
await drawer.locator('.el-pager li:has-text("3")').click()
await page.waitForFunction(
  () => document.querySelector('.f-table .el-table__row')?.innerText.includes('第 201 集'),
  null, { timeout: 8000 })
ok('第 3 页第一条是第 201 集', true)

console.log('3. 关掉再打开：还在第 3 页')
await closeDrawer()
await openTask('剧集')
ok('重开仍是第 3 页', (await activePage()) === '3', `当前第 ${await activePage()} 页`)
ok('内容也是第 3 页', (await firstFile().innerText()).includes('第 201 集'), await firstFile().innerText())
await page.waitForTimeout(400)
await drawer.screenshot({ path: `${SHOTS}/tasks-page-kept.png` })

console.log('4. 换个任务：从第 1 页开始，不串台')
await closeDrawer()
await openTask('电影')
ok('另一个任务从第 1 页开始', (await activePage()) === '1', `当前第 ${await activePage()} 页`)
await closeDrawer()
await openTask('剧集')
ok('回到原任务仍记得第 3 页', (await activePage()) === '3', `当前第 ${await activePage()} 页`)

console.log('5. 清单变短：记住的页码不存在了，回落到最后一页')
await closeDrawer()
totals[1] = 120 // 3 页缩到 2 页
await openTask('剧集')
ok('回落到最后一页', (await activePage()) === '2', `当前第 ${await activePage()} 页`)
ok('页面不是空的', (await drawer.locator('.f-table .el-table__row:visible').count()) > 0)

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
console.log(`截图: ${SHOTS}/tasks-page-kept.png`)
console.log(`${failed === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 && errors.length === 0 ? 0 : 1)
