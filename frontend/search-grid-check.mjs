// 验证：搜索页新增列表/方格视图切换（回归 13 项）。
// 用法: node search-grid-check.mjs   （BASE 环境变量可覆盖服务器地址）
import { chromium } from 'playwright-core'
import { execSync } from 'node:child_process'

const BASE = process.env.BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const OUT = '../_shots'
execSync(`mkdir -p ${OUT}`, { shell: 'bash' })

let pass = 0, fail = 0
const ok = (name, cond) => { if (cond) { pass++; console.log('  ✓', name) } else { fail++; console.log('  ✗', name) } }

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(/\/library\/video/)

// 确保默认列表视图（清掉可能残留的偏好）
await page.evaluate(() => localStorage.setItem('nl_view', 'list'))

// 搜索
await page.goto(`${BASE}/search`)
await page.fill('.bar input', '电影')
await page.click('.bar button:has-text("搜索")')
await page.waitForSelector('.results .result', { timeout: 10000 })

// 1) 默认列表视图渲染，结果头与切换器出现
ok('默认渲染列表视图 .results .result', (await page.locator('.results .result').count()) > 0)
ok('结果头显示数量', /找到 \d+ 项/.test(await page.locator('.results-head .count').innerText()))
ok('视图切换器存在两个按钮', (await page.locator('.results-head .el-radio-button').count()) === 2)
ok('默认不渲染方格网', (await page.locator('.poster-grid').count()) === 0)

const listCount = await page.locator('.results .result').count()

// 2) 切到方格网视图
await page.click('.results-head .el-radio-button:last-child')
await page.waitForSelector('.poster-grid .g-card', { timeout: 5000 })
const gridCount = await page.locator('.poster-grid .g-card').count()
ok('方格网渲染 g-card', gridCount > 0)
ok('方格网卡片数与列表一致', gridCount === listCount)
ok('列表视图已隐藏', (await page.locator('.results .result').count()) === 0)
ok('每张卡片有名称', (await page.locator('.poster-grid .g-name').count()) === gridCount)
// 网格无横向溢出
ok('方格网无横向溢出', await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1))
await page.waitForTimeout(600)
await page.screenshot({ path: `${OUT}/search-grid.png` })

// 3) 偏好持久化到 localStorage 并与全站共享
ok('视图偏好写入 localStorage=grid', (await page.evaluate(() => localStorage.getItem('nl_view'))) === 'grid')
await page.goto(`${BASE}/files`)
await page.waitForTimeout(600)
ok('文件页共享偏好=方格视图', (await page.locator('.poster-grid').count()) > 0)

// 4) 切回列表
await page.goto(`${BASE}/search`)
await page.fill('.bar input', '电影')
await page.click('.bar button:has-text("搜索")')
await page.waitForSelector('.poster-grid .g-card', { timeout: 10000 })
await page.click('.results-head .el-radio-button:first-child')
await page.waitForSelector('.results .result', { timeout: 5000 })
ok('切回列表视图', (await page.locator('.results .result').count()) > 0)

ok('无控制台/页面错误', errors.length === 0)
if (errors.length) console.log(errors.join('\n'))

console.log(`\n结果: ${pass} 通过, ${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
