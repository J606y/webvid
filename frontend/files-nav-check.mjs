// 文件页导航体验验证（点文件夹延迟反馈修复）：
// 1. 点文件夹立即有反馈：旧列表马上清空并亮「加载中…」，而不是停在旧目录等请求回来
// 2. 已访问目录回退/重进秒开：缓存先上屏不等网络（stale-while-revalidate）
// 3. 缓存上屏后仍后台发 fs/list 校正数据
// 网络层给 /api/fs/list 注入 800ms 延迟放大观感，断言在 250ms 内完成 → 命中的只可能是缓存/即时反馈。
// 用法: node files-nav-check.mjs （需服务在跑；NL_BASE 覆盖地址，默认 5243）
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const DELAY = 800

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

let fsListReqs = 0
page.on('request', (r) => { if (r.url().includes('/api/fs/list')) fsListReqs++ })

let passed = 0, failed = 0
const ok = (name, cond, extra = '') => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}${extra ? ` (${extra})` : ''}`) }
}
const rowCount = () => page.locator('.el-table__row').count()
const tipVisible = () => page.locator('.loading-tip').first().isVisible().catch(() => false)

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)

// 进文件页（根目录），等首屏列表
await page.goto(`${BASE}/files`)
await page.waitForSelector('.el-table__row', { timeout: 15000 })

// 之后所有 fs/list 加 800ms 网络延迟
await page.route('**/api/fs/list*', async (route) => {
  await new Promise((r) => setTimeout(r, DELAY))
  await route.continue()
})

// ---- 1. 首次进入未缓存目录：点文件夹应立即清旧列表亮加载态 ----
console.log('1. 点文件夹的即时反馈（未缓存目录）')
await page.click('.el-table__row:has-text("本地存储") .cell-name')
await page.waitForTimeout(200) // 远小于 800ms 延迟，此刻数据必然未返回
ok('URL 立即切到子目录', page.url().includes(encodeURIComponent('本地存储')), page.url())
ok('旧目录列表立即清空', (await rowCount()) === 0, `rows=${await rowCount()}`)
ok('显示「加载中…」', await tipVisible())
await page.waitForSelector('.el-table__row', { timeout: 15000 })
ok('数据返回后列表出现', (await rowCount()) > 0)
ok('加载态消失', !(await tipVisible()))

// ---- 2. 再进一层（也未缓存），为回退测试铺路 ----
console.log('2. 再进一层子目录')
await page.click('.el-table__row:has-text("电影") .cell-name')
await page.waitForTimeout(200)
ok('二层导航同样即时反馈', (await rowCount()) === 0 && (await tipVisible()))
await page.waitForSelector('.el-table__row', { timeout: 15000 })

// ---- 3. 回退到已访问目录：缓存秒开，不等 800ms 网络 ----
console.log('3. 回退秒开（缓存先上屏）')
const reqsBeforeBack = fsListReqs
await page.goBack()
await page.waitForTimeout(250) // < 800ms：此刻能看到内容只可能来自缓存
const backRows = await rowCount()
const backHasDirs = await page.locator('.el-table__row:has-text("电影")').count()
ok('回退 250ms 内列表已上屏（未等网络）', backRows > 0 && backHasDirs > 0, `rows=${backRows}`)
ok('缓存上屏时无加载态', !(await tipVisible()))
await page.waitForTimeout(DELAY + 500) // 等后台校正请求落地
ok('缓存上屏后仍后台发 fs/list 校正', fsListReqs > reqsBeforeBack, `${reqsBeforeBack}→${fsListReqs}`)
ok('校正后内容仍正确', (await page.locator('.el-table__row:has-text("电影")').count()) > 0)

// ---- 4. 面包屑回根目录：同样缓存秒开 ----
console.log('4. 面包屑回根目录秒开')
await page.click('.crumbs .crumb >> nth=0')
await page.waitForTimeout(250)
ok('根目录 250ms 内上屏', (await page.locator('.el-table__row:has-text("本地存储")').count()) > 0)

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
console.log(`\n${failed === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 ? 0 : 1)
