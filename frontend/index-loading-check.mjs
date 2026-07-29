// 索引管理面板的加载态：进度是内存态、默认值全是 0，首帧直接渲染会先说「索引共 0 项」
// 再跳成真实条数——远程实例上就是「打开后等一会数字才出现」的观感。三件事各验一遍：
// 1) 高 RTT（每个 /api 加 800ms）下，数据未回来时只占位、不得出现 0，动作按钮不可点
// 2) 数据到位后数字正确、占位撤走、按钮恢复
// 3) 接口失败时就地说明 + 有重试，且只影响出错那张卡、不弹 toast 刷屏
// 用法: NL_BASE=http://localhost:5243 node index-loading-check.mjs （需服务在跑）
import { chromium } from 'playwright-core'
const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const SHOTS = process.env.TEMP || '.'
const DELAY = Number(process.env.DELAY || 800)

let passed = 0, failed = 0
const ok = (name, cond, extra = '') => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}${extra ? ` (${extra})` : ''}`) }
}
const flat = (s) => s.replace(/\s+/g, ' ').trim()

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

async function login() {
  await page.goto(`${BASE}/login`)
  await page.fill('input[placeholder="用户名"]', 'admin')
  await page.fill('input[placeholder="密码"]', 'admin123')
  await page.click('button:has-text("登 录")')
  await page.waitForURL(`${BASE}/library/video`)
}
await login()

// ---- 1. 慢网：数据未回来时的观感 ----
console.log('\n1. 高 RTT（+800ms）下的首帧')
let slow = true
await page.route('**/api/**', async (route) => {
  if (slow) await new Promise((r) => setTimeout(r, DELAY))
  route.continue()
})
await page.goto(`${BASE}/@admin`)
await page.click('.el-tabs__item:has-text("索引管理")')
await page.waitForSelector('.index-card')
const idxCard = page.locator('.index-card:has-text("文件索引")')
const preCard = page.locator('.index-card:has-text("封面与源信息预载")')
const a = flat(await idxCard.innerText())
const b = flat(await preCard.innerText())
console.log('  索引卡片:', JSON.stringify(a))
console.log('  预载卡片:', JSON.stringify(b))
ok('未回来时不说「索引共 0 项」', !a.includes('索引共 0 项'), a)
ok('未回来时不说「封面 0」', !/封面 0 /.test(b), b)
ok('占位条已渲染（两卡共 3 处）', (await page.locator('.index-card .ph').count()) === 3)
ok('未读到状态时「重建索引」不可点', await idxCard.getByRole('button', { name: '重建索引' }).isDisabled())
ok('未读到状态时「删除缓存」不可点', await preCard.getByRole('button', { name: '删除缓存' }).isDisabled())
await page.screenshot({ path: `${SHOTS}/index-loading.png`, clip: { x: 0, y: 120, width: 1000, height: 420 } })

// ---- 2. 数据到位 ----
console.log('\n2. 数据到位')
await page.waitForFunction(() => {
  const c = [...document.querySelectorAll('.index-card')].find((x) => x.textContent.includes('文件索引'))
  return c && !c.querySelector('.ph')
}, null, { timeout: 20000 }).catch(() => {})
const a2 = flat(await idxCard.innerText())
const b2 = flat(await preCard.innerText())
console.log('  索引卡片:', JSON.stringify(a2))
console.log('  预载卡片:', JSON.stringify(b2))
ok('索引条数出现', /索引共 \d+ 项/.test(a2), a2)
ok('封面/源信息数出现', /封面 \d+( \/ \d+)? · 视频源信息 \d+( \/ \d+)? 已缓存/.test(b2), b2)
ok('占位条已撤走', (await page.locator('.index-card .ph').count()) === 0)
ok('按钮恢复可点', !(await idxCard.getByRole('button', { name: '重建索引' }).isDisabled()))
await page.screenshot({ path: `${SHOTS}/index-loaded.png`, clip: { x: 0, y: 120, width: 1000, height: 420 } })

// ---- 3. 首次就失败：就地说明 + 重试 ----
console.log('\n3. 接口失败')
slow = false
let broken = true
await page.route('**/api/admin/index/progress', (route) =>
  broken ? route.fulfill({ status: 500, contentType: 'application/json',
    body: JSON.stringify({ code: 500, message: '数据库忙，请稍后重试' }) }) : route.continue())
await page.goto(`${BASE}/@admin`)
await page.click('.el-tabs__item:has-text("索引管理")')
await page.waitForSelector('.index-card .err', { timeout: 10000 }).catch(() => {})
const a3 = flat(await idxCard.innerText())
const b3 = flat(await preCard.innerText())
console.log('  索引卡片:', JSON.stringify(a3))
console.log('  预载卡片:', JSON.stringify(b3))
ok('说清读不到 + 带原因', a3.includes('读不到索引信息') && a3.includes('数据库忙'), a3)
ok('只影响出错那张卡，预载照常显示', /封面 \d+( \/ \d+)? · 视频源信息 \d+( \/ \d+)? 已缓存/.test(b3), b3)
ok('失败时不弹 toast 刷屏', (await page.locator('.el-message--error').count()) === 0)
await page.screenshot({ path: `${SHOTS}/index-failed.png`, clip: { x: 0, y: 120, width: 1000, height: 420 } })

broken = false
await idxCard.getByRole('button', { name: '重试' }).click()
await page.waitForFunction(() => {
  const c = [...document.querySelectorAll('.index-card')].find((x) => x.textContent.includes('文件索引'))
  return c && /索引共 \d+ 项/.test(c.textContent)
}, null, { timeout: 10000 }).catch(() => {})
ok('重试后拿到数字', /索引共 \d+ 项/.test(flat(await idxCard.innerText())), flat(await idxCard.innerText()))

await browser.close()
console.log('\n==== 控制台错误 ====')
errors.length ? errors.forEach((e) => console.log(e)) : console.log('无错误 ✔')
console.log(`截图: ${SHOTS}/index-loading.png · index-loaded.png · index-failed.png`)
console.log(`${failed === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 ? 0 : 1)
