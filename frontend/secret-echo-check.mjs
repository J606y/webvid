// 存储 secret 明文回显验证：列表接口脱敏 ***，编辑弹窗取单条明文，
// 密码框默认圆点、点「眼睛」显示完整原文。
// 用法: node secret-echo-check.mjs （需服务在跑，admin/admin123；
//       需存在一个 onedrive 存储且 client_secret 非空，可用 NL_BASE 指定实例）
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'

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

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL((u) => !u.pathname.startsWith('/login'))

// 列表接口仍脱敏
const list = await page.evaluate(async () => {
  const r = await fetch('/api/admin/storages', {
    headers: { Authorization: 'Bearer ' + localStorage.getItem('nl_token') },
  })
  return (await r.json()).data || []
})
const od = list.find((s) => s.driver === 'onedrive' && s.config.client_secret)
ok('存在 onedrive 存储（前置条件）', !!od)
ok('列表接口 client_secret 脱敏为 ***', od?.config.client_secret === '***')

// 单条接口返回原文
const one = await page.evaluate(async (id) => {
  const r = await fetch(`/api/admin/storages/${id}`, {
    headers: { Authorization: 'Bearer ' + localStorage.getItem('nl_token') },
  })
  return (await r.json()).data
}, od.id)
const realSecret = one?.config?.client_secret
ok('单条接口返回明文（非 ***、非空）', !!realSecret && realSecret !== '***')

// 编辑弹窗：密码框回显明文值，默认圆点，点眼睛可见原文
await page.goto(`${BASE}/@admin?tab=storage`).catch(() => {})
await page.goto(`${BASE}/@admin`)
await page.click('.el-tabs__item:has-text("存储管理")')
const row = page.locator('.el-table__row', { hasText: od.mount_path })
await row.locator('.el-button').first().click()
const dlg = page.locator('.el-dialog:visible')
await dlg.waitFor()
// 客户端密码所在表单项
const item = dlg.locator('.el-form-item', { has: page.locator('label:has-text("客户端密码")') })
const input = item.locator('input')
await page.waitForFunction(
  (el) => el && el.value && el.value !== '***',
  await input.elementHandle(), { timeout: 5000 }
).catch(() => {})
ok('弹窗密码框值为明文原文', (await input.inputValue()) === realSecret)
ok('默认 type=password（显示圆点）', (await input.getAttribute('type')) === 'password')
// 点眼睛
await item.locator('.el-input__suffix .el-icon').last().click()
ok('点眼睛后 type=text（可见原文）', (await input.getAttribute('type')) === 'text')
ok('点眼睛后输入框内容=完整原文', (await input.inputValue()) === realSecret)
ok('无控制台错误', errors.length === 0)
if (errors.length) console.error(errors.join('\n'))

await browser.close()
console.log(`\n${passed} 通过 / ${failed} 失败`)
process.exit(failed ? 1 : 0)
