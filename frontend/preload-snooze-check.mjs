// 「封面与源信息预载」的推迟/继续：预载运行中出「不是现在」，点后卡片转推迟态、
// 出「继续」，再点回到常态。进度接口用桩驱动三种状态（真实预载几毫秒就跑完，
// 稳不住 running），snooze/resume 两个 POST 打真后端，验路由与前端接线。
// 用法: NL_BASE=http://localhost:5299 node preload-snooze-check.mjs （需服务在跑）
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const SHOTS = process.env.TEMP || '.'

const base = {
  running: false, total: 0, done: 0, covers: 12, probes: 5, current: '', err: '',
  finished_at: '2026-07-25T20:00:00Z', snoozed: false, resume_at: '', pending: 0,
}
const tomorrow = new Date(Date.now() + 24 * 3600 * 1000)
const states = {
  running: { ...base, running: true, total: 40, done: 9, current: '/OneDrive/影视/沙丘.mkv' },
  snoozed: { ...base, snoozed: true, resume_at: tomorrow.toISOString(), pending: 31 },
  idle: base,
}
let state = 'running'

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

const posts = []
page.on('request', (r) => {
  if (r.method() === 'POST' && r.url().includes('/api/admin/preload/')) posts.push(r.url().split('/preload/')[1])
})
await page.route('**/api/admin/preload/progress', (route) => route.fulfill({
  status: 200, contentType: 'application/json',
  body: JSON.stringify({ code: 200, data: states[state], message: 'success' }),
}))

let passed = 0, failed = 0
const ok = (name, cond, extra = '') => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}${extra ? ` (${extra})` : ''}`) }
}
const card = page.locator('.index-card:has-text("封面与源信息预载")')
const btn = (name) => card.getByRole('button', { name, exact: true })

// 登录 → 后台 → 索引管理
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)
await page.goto(`${BASE}/@admin`)
await page.click('.el-tabs__item:has-text("索引管理")')
await card.waitFor({ timeout: 10000 })

console.log('1. 预载运行中：出「不是现在」')
await btn('不是现在').waitFor({ timeout: 8000 })
ok('运行中出现「不是现在」', true)
ok('运行中「重新预载」不可点', await btn('重新预载').isDisabled())
await page.screenshot({ path: `${SHOTS}/preload-running.png`, clip: await card.boundingBox() })

console.log('2. 点「不是现在」→ 转推迟态')
state = 'snoozed'
await btn('不是现在').click()
await btn('继续').waitFor({ timeout: 8000 })
ok('调了 POST /preload/snooze', posts.includes('snooze'), posts.join(','))
ok('推迟态出现「继续」', true)
ok('推迟态收起「不是现在」', (await btn('不是现在').count()) === 0)
ok('推迟态收起「重新预载」', (await btn('重新预载').count()) === 0)
const text = (await card.innerText()).replace(/\s+/g, ' ')
const hh = `${tomorrow.getHours()}:${String(tomorrow.getMinutes()).padStart(2, '0')}`
ok('文案给出自动继续的时刻', text.includes(`已推迟，明天 ${hh} 自动继续。`), text)
ok('文案给出剩余项数', text.includes('还剩 31 项'), text)
await page.screenshot({ path: `${SHOTS}/preload-snoozed.png`, clip: await card.boundingBox() })

console.log('3. 点「继续」→ 回到常态')
state = 'idle'
await btn('继续').click()
await btn('重新预载').waitFor({ timeout: 8000 })
ok('调了 POST /preload/resume', posts.includes('resume'), posts.join(','))
ok('回到常态：「重新预载」可点', !(await btn('重新预载').isDisabled()))
ok('回到常态：无「继续」', (await btn('继续').count()) === 0)

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
console.log(`截图: ${SHOTS}/preload-running.png · ${SHOTS}/preload-snoozed.png`)
console.log(`${failed === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 && errors.length === 0 ? 0 : 1)
