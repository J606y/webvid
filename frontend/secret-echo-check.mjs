// 存储 secret 明文回显验证：列表接口脱敏 ***，编辑弹窗取单条明文，
// 密码框默认圆点、点「眼睛」显示完整原文。
// 用法: node secret-echo-check.mjs （需服务在跑，admin/admin123，可用 NL_BASE 指定实例）
// 前置条件：实例里有任意一个带密钥字段的存储（OneDrive / Telegram / Google Drive / PikPak 均可）。
// 一个都没有时跳过而非报错——密钥字段名与标签从 /api/admin/drivers 现取，不写死驱动。
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

const authGet = (path) => page.evaluate(async (p) => {
  const r = await fetch(p, { headers: { Authorization: 'Bearer ' + localStorage.getItem('nl_token') } })
  return (await r.json()).data
}, path)

// 各驱动的第一个密钥字段（名称 + 表单标签）
const secretOf = {}
for (const d of (await authGet('/api/admin/drivers')) || []) {
  const f = (d.fields || []).find((x) => x.secret)
  if (f) secretOf[d.name] = { name: f.name, label: f.label }
}

// 列表接口仍脱敏
const list = (await authGet('/api/admin/storages')) || []
const od = list.find((s) => secretOf[s.driver] && s.config[secretOf[s.driver].name])
if (!od) {
  console.log('跳过：当前实例没有任何带密钥字段的存储，无从验证脱敏与明文回显')
  await browser.close()
  process.exit(0)
}
const sec = secretOf[od.driver]
console.log(`目标存储：${od.mount_path}（${od.driver}） 密钥字段：${sec.label}`)
ok(`列表接口 ${sec.name} 脱敏为 ***`, od.config[sec.name] === '***')

// 单条接口返回原文
const one = await authGet(`/api/admin/storages/${od.id}`)
const realSecret = one?.config?.[sec.name]
ok('单条接口返回明文（非 ***、非空）', !!realSecret && realSecret !== '***')

// 编辑弹窗：密码框回显明文值，默认圆点，点眼睛可见原文
await page.goto(`${BASE}/@admin?tab=storage`).catch(() => {})
await page.goto(`${BASE}/@admin`)
await page.click('.el-tabs__item:has-text("存储管理")')
const row = page.locator('.el-table__row', { hasText: od.mount_path })
// 编辑按钮 = 该行第一个不带 title 的按钮：telegram/googledrive 行前面还有
// 「验证码登录」「授权 Google」两个带 title 的图标钮，直接取 .first() 会点错。
await row.locator('.el-button:not([title])').first().click()
const dlg = page.locator('.el-dialog:visible')
await dlg.waitFor()
// 密钥字段所在表单项（标签取自驱动元数据，不写死某个驱动的叫法）
const item = dlg.locator('.el-form-item', { has: page.locator(`label:has-text("${sec.label}")`) })
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
