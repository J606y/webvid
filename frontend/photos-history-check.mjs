// 反馈#26 照片墙最近查看「查看更多」验证：货架头按钮 → ?viewed=1 视图（大标题/无排序下拉/
// ≤50 张查看历史网格）→ 灯箱打开 → 返回主页 → 深链直达。
// 用法: node photos-history-check.mjs （需服务在跑）
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
// PhotoSwipe 开场动画期间 Escape 可能被忽略 → 重试直到 .pswp 消失
async function closeLightbox() {
  for (let i = 0; i < 6; i++) {
    await page.keyboard.press('Escape')
    try { await page.waitForSelector('.pswp', { state: 'detached', timeout: 2000 }); return true } catch {}
  }
  return false
}

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)

// 预置查看历史：直接上报若干本地图片（幂等，重复上报只更新时间）
const views = [
  '/本地存储/图片/风景/晨雾山谷.png', '/本地存储/图片/风景/极光之夜.png',
  '/本地存储/图片/风景/沙漠斜阳.jpg', '/本地存储/图片/风景/湖畔黄昏.jpg',
  '/本地存储/图片/风景/翠谷清泉.jpg',
]
for (const p of views) {
  const st = await page.evaluate(async (path) => {
    const r = await fetch('/api/media/played', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + localStorage.getItem('nl_token') },
      body: JSON.stringify({ path }),
    })
    return r.status
  }, p)
  if (st !== 200) console.log(`  ⚠️ 上报查看 ${p} => ${st}`)
}

await page.goto(`${BASE}/library/photos`)
await page.waitForTimeout(1200)
await page.waitForSelector('.photo-grid .cell', { timeout: 15000 })

// 1. 「最近查看」货架头有查看更多按钮，且 .see-all（查看全部）仍唯一
await page.waitForSelector('.shelf-head:has(h2:text("最近查看"))', { timeout: 15000 })
ok('最近查看货架有查看更多按钮', await page.locator('.see-more').count() === 1)
ok('查看全部按钮仍唯一（.see-all 不被撞类）', await page.locator('.see-all').count() === 1)

// 2. 点击进入 ?viewed=1 视图
await page.click('.see-more')
await page.waitForURL(/viewed=1/, { timeout: 5000 })
await page.waitForSelector('.photo-grid .cell', { timeout: 15000 })
ok('URL 带 viewed=1', page.url().includes('viewed=1'))
ok('大标题为最近查看', (await page.locator('.lib-title').textContent())?.trim() === '最近查看')
ok('无排序下拉', await page.locator('.lib-head .el-select').count() === 0)
ok('无 Featured 横幅', await page.locator('.feat').count() === 0)
ok('无货架区块', await page.locator('.shelf-head h2:text("最近查看")').count() === 0)

// 3. 网格张数 ≤50 且与 /media/history?limit=50 一致
const apiCount = await page.evaluate(async () => {
  const r = await fetch('/api/media/history?kind=image&limit=50', {
    headers: { Authorization: 'Bearer ' + localStorage.getItem('nl_token') },
  })
  return (await r.json()).data.items.length
})
const cells = await page.locator('.photo-grid .cell').count()
ok(`网格张数=${cells} 与接口一致且 ≤50`, cells === apiCount && cells <= 50 && cells >= 3)
await page.screenshot({ path: `${OUT}/17-photos-viewed.png` })
console.log('shot: 17-photos-viewed')

// 4. 点 cell 开灯箱（cell 可点）
await page.click('.photo-grid .cell >> nth=0')
ok('网格照片可开灯箱', await page.waitForSelector('.pswp', { timeout: 20000 }).then(() => true).catch(() => false))
await closeLightbox()

// 5. 返回键回主页（货架恢复）
await page.click('.back-btn')
await page.waitForURL((u) => !u.href.includes('viewed=1'), { timeout: 5000 })
await page.waitForSelector('.shelf-head h2:text("最近查看")', { timeout: 15000 })
ok('返回键回到主页', await page.locator('.feat').count() === 1)

// 6. 深链直达 ?viewed=1（刷新可用）
await page.goto(`${BASE}/library/photos?viewed=1`)
await page.waitForSelector('.photo-grid .cell', { timeout: 15000 })
ok('深链直达最近查看视图', (await page.locator('.lib-title').textContent())?.trim() === '最近查看')

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
console.log(`\n${failed === 0 && errors.length === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 ? 0 : 1)
