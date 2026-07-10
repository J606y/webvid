// 反馈#23 最近播放「查看更多」验证：货架头按钮 → ?played=1 视图（大标题/无排序下拉/
// ≤50 条播放时间副文本）→ 返回主页；卡片弹详情、空态文案由有历史的 admin 断言不到，跳过。
// 用法: node history-check.mjs （需服务在跑）
import { chromium } from 'playwright-core'
import { execSync } from 'node:child_process'

const BASE = 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const OUT = '../_shots'
execSync(`mkdir -p ${OUT}`, { shell: 'bash' })

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
await page.waitForURL(`${BASE}/library/video`)

// 预置播放历史：直接上报三个本地样片（幂等，重复上报只更新时间）
const plays = ['/本地存储/电影/星际漫游.mp4', '/本地存储/电影/暗夜迷城.mp4', '/本地存储/电影/落日之城.mp4']
for (const p of plays) {
  const st = await page.evaluate(async (path) => {
    const r = await fetch('/api/media/played', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + localStorage.getItem('nl_token') },
      body: JSON.stringify({ path }),
    })
    return r.status
  }, p)
  if (st !== 200) console.log(`  ⚠️ 上报播放 ${p} => ${st}`)
}
await page.reload()
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })

// 1. 货架头「查看更多」按钮存在
await page.waitForSelector('.shelf-head:has(h2:text("最近播放"))', { timeout: 15000 })
ok('最近播放货架有查看更多按钮', await page.locator('.see-more').count() === 1)

// 2. 点击进入 ?played=1 视图
await page.click('.see-more')
await page.waitForURL(/played=1/, { timeout: 5000 })
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })
ok('URL 带 played=1', page.url().includes('played=1'))
ok('大标题为最近播放', (await page.locator('.lib-title').textContent())?.trim() === '最近播放')
ok('无排序下拉', await page.locator('.lib-head .el-select').count() === 0)
ok('无 Featured 横幅', await page.locator('.feat').count() === 0)
ok('无货架区块', await page.locator('.shelf-head h2:text("最近添加")').count() === 0)

// 3. 网格条数 ≤50 且与 /media/history?limit=50 一致，副文本带「播放」
const apiCount = await page.evaluate(async () => {
  const r = await fetch('/api/media/history?kind=video&limit=50', {
    headers: { Authorization: 'Bearer ' + localStorage.getItem('nl_token') },
  })
  return (await r.json()).data.items.length
})
const cells = await page.locator('.v-grid .v-card').count()
ok(`网格条数=${cells} 与接口一致且 ≤50`, cells === apiCount && cells <= 50 && cells >= 3)
const sub = (await page.locator('.v-grid .v-card .v-sub').first().textContent())?.trim() || ''
ok(`副文本为播放时间（"${sub}"）`, /播放$/.test(sub))
await page.screenshot({ path: `${OUT}/16-played-view.png` })
console.log('shot: 16-played-view')

// 4. 卡片点击弹详情（与库内一致）
await page.click('.v-grid .v-card >> nth=0')
await page.waitForSelector('.el-dialog.vdc', { timeout: 5000 })
ok('卡片点击弹详情', true)
await page.keyboard.press('Escape')
await page.waitForSelector('.el-dialog.vdc', { state: 'detached', timeout: 5000 })

// 5. 返回键回主页（货架恢复）
await page.click('.back-btn')
await page.waitForURL((u) => !u.href.includes('played=1'), { timeout: 5000 })
await page.waitForSelector('.shelf-head h2:text("最近添加")', { timeout: 15000 })
ok('返回键回到主页', true)

// 6. 深链直达 ?played=1（刷新可用）
await page.goto(`${BASE}/library/video?played=1`)
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })
ok('深链直达最近播放视图', (await page.locator('.lib-title').textContent())?.trim() === '最近播放')

// 7. 从视图进播放页再返回，视图与滚动保持（keep-alive 冻结 query 回归）
await page.click('.v-grid .v-card >> nth=0')
await page.waitForSelector('.el-dialog.vdc', { timeout: 5000 })
// 播放按钮文案随续播特性变化（有进度=「继续观看」，否则「立即播放」），两者皆匹配
await page.locator('.el-dialog.vdc .el-button--primary').first().click()
await page.waitForURL(/\/play\//, { timeout: 5000 })
await page.goBack()
await page.waitForURL(/played=1/, { timeout: 5000 })
await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })
ok('播放后返回仍在最近播放视图', (await page.locator('.lib-title').textContent())?.trim() === '最近播放')

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
console.log(`\n${failed === 0 && errors.length === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 ? 0 : 1)
