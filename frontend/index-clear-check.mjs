// 索引管理的两个删除动作：「删除索引」与「删除缓存」——确认弹窗、取消不发请求、
// 确认后发对路由、删完卡片归零。
// 两个 clear 接口在浏览器里打桩：打真后端会把这台实例的索引与封面缓存真删掉，
// 重新扫云盘的代价太大（后端行为由 internal/index、internal/preload 的单测覆盖）。
// 路由是否真的注册，开头用未鉴权 POST 验：注册了返 401，没注册返 404。
// 用法: NL_BASE=http://localhost:5299 node index-clear-check.mjs （需服务在跑）
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const SHOTS = process.env.TEMP || '.'

let passed = 0, failed = 0
const ok = (name, cond, extra = '') => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}${extra ? ` (${extra})` : ''}`) }
}

console.log('0. 路由已注册（未鉴权应 401，而非 404）')
for (const p of ['/api/admin/index/clear', '/api/admin/preload/clear']) {
  const r = await fetch(BASE + p, { method: 'POST' })
  ok(`POST ${p} 已注册`, r.status === 401, `status=${r.status}`)
}

const idle = { running: false, scanned: 1234, current: '', err: '' }
let idx = { ...idle, running: true, current: '/OneDrive/影视' }
const preload = {
  running: false, total: 0, done: 0, covers: 12, probes: 5, current: '', err: '',
  finished_at: '2026-07-28T09:00:00Z', snoozed: false, resume_at: '', pending: 0,
}

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

const posts = []
page.on('request', (r) => {
  if (r.method() === 'POST' && r.url().includes('/clear')) posts.push(new URL(r.url()).pathname)
})
const json = (data) => ({
  status: 200, contentType: 'application/json',
  body: JSON.stringify({ code: 200, data, message: 'success' }),
})
await page.route('**/api/admin/index/progress', (route) => route.fulfill(json(idx)))
await page.route('**/api/admin/preload/progress', (route) => route.fulfill(json(preload)))
await page.route('**/api/admin/index/clear', (route) => {
  idx = { ...idle, scanned: 0 }
  route.fulfill(json(idx))
})
await page.route('**/api/admin/preload/clear', (route) =>
  route.fulfill(json({ covers: 2, bytes: 2097152 })))

const idxCard = page.locator('.index-card:has-text("文件索引")')
const preCard = page.locator('.index-card:has-text("封面与源信息预载")')
const btn = (card, name) => card.getByRole('button', { name, exact: true })
const dialogBtn = (name) => page.locator('.el-message-box__btns').getByRole('button', { name, exact: true })
const toast = () => page.locator('.el-message--success').last()
// 截图前等蒙层退场：确认弹窗的淡出还没走完就拍，拍到的是被压暗模糊的卡片，看不出真实观感
const shot = async (card, name) => {
  await page.locator('.el-overlay').waitFor({ state: 'hidden', timeout: 5000 }).catch(() => {})
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${SHOTS}/${name}.png`, clip: await card.boundingBox() })
}

// 登录 → 后台 → 索引管理
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)
await page.goto(`${BASE}/@admin`)
await page.click('.el-tabs__item:has-text("索引管理")')
await idxCard.waitFor({ timeout: 10000 })

console.log('\n1. 重建进行中：「删除索引」不可点')
ok('重建中「删除索引」禁用', await btn(idxCard, '删除索引').isDisabled())
idx = { ...idle } // 重建结束，轮询（1.5s）会带回常态
await page.waitForFunction(
  () => [...document.querySelectorAll('.index-card')]
    .some((c) => c.textContent.includes('文件索引') && c.textContent.includes('索引共')),
  null, { timeout: 8000 })
ok('结束后「删除索引」可点', !(await btn(idxCard, '删除索引').isDisabled()))
ok('卡片显示索引条数', (await idxCard.innerText()).includes('1234'), await idxCard.innerText())

console.log('\n2. 取消确认：不发请求')
await btn(idxCard, '删除索引').click()
await dialogBtn('取消').waitFor({ timeout: 5000 })
const boxText = (await page.locator('.el-message-box').innerText()).replace(/\s+/g, ' ')
ok('弹窗讲清文件不受影响', boxText.includes('文件本身不受影响'), boxText)
await dialogBtn('取消').click()
await page.waitForTimeout(300)
ok('取消后未发请求', posts.length === 0, posts.join(','))

console.log('\n3. 删除索引：发 POST，卡片归零')
await btn(idxCard, '删除索引').click()
await dialogBtn('删除').click()
await toast().waitFor({ timeout: 5000 })
ok('调了 POST /api/admin/index/clear', posts.includes('/api/admin/index/clear'), posts.join(','))
ok('提示「索引已删除」', (await toast().innerText()).includes('索引已删除'), await toast().innerText())
ok('卡片回到 0 项', (await idxCard.innerText()).includes('索引共 0 项'), await idxCard.innerText())
await shot(idxCard, 'index-cleared')

console.log('\n4. 删除缓存：发 POST，提示删了多少、释放多少')
await btn(preCard, '删除缓存').click()
await dialogBtn('删除').click()
await page.waitForFunction(
  () => [...document.querySelectorAll('.el-message--success')]
    .some((m) => m.textContent.includes('已删除')), null, { timeout: 5000 })
ok('调了 POST /api/admin/preload/clear', posts.includes('/api/admin/preload/clear'), posts.join(','))
const msg = await toast().innerText()
ok('提示封面数与释放空间', msg.includes('已删除 2 个封面') && msg.includes('2.0 MB'), msg)
await shot(preCard, 'preload-cleared')

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
console.log(`截图: ${SHOTS}/index-cleared.png · ${SHOTS}/preload-cleared.png`)
console.log(`${failed === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 && errors.length === 0 ? 0 : 1)
