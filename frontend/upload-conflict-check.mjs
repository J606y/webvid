// #6 上传同名冲突：改「按 HTTP 409 状态码分支」而非匹配错误文案 includes('已存在')。
// 验证真实链路：后端 fsUpload → driver.ErrExist → fsError 返 409 → http.js 拦截器把
// status 挂到 error（httpError）→ UploadDrawer 按 e.status===409 进入 conflict 态（出「覆盖上传」）。
// 且上传请求 silent:true，冲突由队列行内呈现，不再弹全局 error toast。
// 用法: NL_BASE=http://localhost:5299 node upload-conflict-check.mjs （需服务在跑）
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const DIR = '本地存储'
const FNAME = `e2e-409-${Date.now()}.txt`

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => {
  if (m.type() !== 'error') return
  // 浏览器对 4xx 资源加载自动记 console error；上传冲突 409 是预期行为（功能正常），非应用错误。
  if (m.text().includes('status of 409')) return
  errors.push(`[console] ${m.text()}`)
})
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

let uploadStatuses = []
page.on('response', (r) => {
  if (r.url().includes('/api/fs/upload')) uploadStatuses.push(r.status())
})

let passed = 0, failed = 0
const ok = (name, cond, extra = '') => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}${extra ? ` (${extra})` : ''}`) }
}

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)

// 进文件页 → 本地存储（可写）
await page.goto(`${BASE}/files`)
await page.waitForSelector('.el-table__row', { timeout: 15000 })
await page.click(`.el-table__row:has-text("${DIR}") .cell-name`)
await page.waitForSelector('.el-table__row', { timeout: 15000 })

// 打开上传抽屉（工具栏「上传」按钮，精确名避开「覆盖上传」/「上传队列」）
console.log('1. 打开上传抽屉并首传（应成功 200）')
await page.getByRole('button', { name: '上传', exact: true }).click()
await page.waitForSelector('.el-drawer input[type=file]', { state: 'attached', timeout: 8000 })
const setFile = () => page.setInputFiles('.el-drawer input[type=file]', {
  name: FNAME, mimeType: 'text/plain', buffer: Buffer.from('hello-409\n'),
})
await setFile()
// 首传完成：任务行出现「完成」
await page.waitForFunction(() => {
  const els = [...document.querySelectorAll('.el-drawer .task .state')]
  return els.some((e) => e.textContent.includes('完成'))
}, { timeout: 20000 })
ok('首传成功（任务显示「完成」）', true)
ok('首传 HTTP 200', uploadStatuses.includes(200), `statuses=${uploadStatuses}`)

// 再传同名（不覆盖）→ 应进 conflict 态：出现「覆盖上传」按钮
console.log('2. 再传同名（应 409 → conflict，出「覆盖上传」）')
const errToastsBefore = await page.locator('.el-message--error').count()
await setFile()
await page.waitForSelector('.el-drawer button:has-text("覆盖上传")', { timeout: 20000 })
ok('冲突进入 conflict 态（出现「覆盖上传」按钮）', true)
ok('第二次上传 HTTP 409', uploadStatuses.includes(409), `statuses=${uploadStatuses}`)
ok('冲突任务不误判为 error（无「重试」按钮）',
  (await page.locator('.el-drawer button:has-text("重试")').count()) === 0)
// silent:true → 冲突不弹全局 error toast（由队列行内呈现）
await page.waitForTimeout(500)
const errToastsAfter = await page.locator('.el-message--error').count()
ok('冲突未弹全局 error toast（silent 生效）', errToastsAfter === errToastsBefore,
  `${errToastsBefore}→${errToastsAfter}`)

// 点「覆盖上传」→ 应成功完成（overwrite=1 → 200）
console.log('3. 点「覆盖上传」应成功')
await page.click('.el-drawer button:has-text("覆盖上传")')
await page.waitForFunction(() => {
  const done = [...document.querySelectorAll('.el-drawer .task .state')]
    .filter((e) => e.textContent.includes('完成'))
  return done.length >= 2 // 首传 + 覆盖后各一条完成
}, { timeout: 20000 })
ok('覆盖上传成功（第二条任务转「完成」）', true)
ok('覆盖上传 HTTP 200（statuses 至少两个 200）',
  uploadStatuses.filter((s) => s === 200).length >= 2, `statuses=${uploadStatuses}`)

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
console.log(`\n上传状态码序列: ${uploadStatuses}`)
console.log(`${failed === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 && errors.length === 0 ? 0 : 1)
