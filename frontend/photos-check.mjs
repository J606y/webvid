// 反馈#11 照片墙 Infuse 化验证：横幅/货架/网格/查看全部/目录视图/灯箱
// 反馈#20：「最近添加」货架改「最近查看」——点开照片记查看历史，重进主页应出现该货架
import { chromium } from 'playwright-core'

const BASE = 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))
page.on('requestfailed', (r) => errors.push(`[requestfailed] ${r.url()} ${r.failure()?.errorText}`))

const ok = (name, cond) => console.log(`${cond ? 'PASS' : 'FAIL'}  ${name}`)
// 开场动画期间 Escape 可能被 PhotoSwipe 忽略 → 重试直到 .pswp 消失
async function closeLightbox() {
  for (let i = 0; i < 6; i++) {
    await page.keyboard.press('Escape')
    try {
      await page.waitForSelector('.pswp', { state: 'detached', timeout: 2000 })
      return true
    } catch {}
  }
  return false
}

await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)

// 主页三区块
await page.goto(`${BASE}/library/photos`)
await page.waitForTimeout(1500)
ok('body 有 infuse-mode', await page.evaluate(() => document.body.classList.contains('infuse-mode')))
ok('Featured 横幅存在', await page.locator('.feat').count() === 1)
ok('横幅 kicker=随机推荐', (await page.locator('.feat-kicker').first().textContent()).trim() === '随机推荐')
ok('横幅 5 张', await page.locator('.feat-item').count() === 5)
// 「最近查看」货架为查看历史驱动，首次访问可能为空——此处不强断言，先点开照片记录查看
const gridN = await page.locator('.photo-grid .cell').count()
ok(`照片网格有内容且≤200 (${gridN})`, gridN > 0 && gridN <= 200)
ok('查看全部按钮在', await page.locator('.see-all').count() === 1)
await page.screenshot({ path: '../_shots/02-library-photos.png' })

// 网格点击开灯箱（e2e 依赖的 .photo-grid .cell 保持可用）→ 顺带记录一次查看历史
// 灯箱 init 前会预载原图，OneDrive 真实图片走 302 下载较慢 → 用长超时等待
await page.click('.photo-grid .cell >> nth=0')
ok('灯箱打开', await page.waitForSelector('.pswp', { timeout: 20000 }).then(() => true).catch(() => false))
await page.waitForTimeout(900) // 等查看上报（防抖 600ms）发出
ok('灯箱可关闭', await closeLightbox())

// 查看全部视图
await page.click('.see-all')
await page.waitForTimeout(1000)
ok('URL 带 all=1', page.url().includes('all=1'))
ok('大标题=所有照片', (await page.locator('.lib-title').textContent()).trim() === '所有照片')
ok('返回键在', await page.locator('.back-btn').count() === 1)
await page.screenshot({ path: '../_shots/02b-photos-all.png' })
await page.click('.back-btn')
await page.waitForTimeout(800)
ok('返回后回主页(横幅重现)', await page.locator('.feat').count() === 1)

// 目录视图
await page.goto(`${BASE}/library/photos?dir=${encodeURIComponent('/本地存储/图片/风景')}`)
await page.waitForTimeout(1000)
ok('目录大标题=风景', (await page.locator('.lib-title').textContent()).trim() === '风景')
ok('目录视图无横幅', await page.locator('.feat').count() === 0)
const dirN = await page.locator('.photo-grid .cell').count()
ok(`目录网格有内容 (${dirN})`, dirN > 0)
await page.screenshot({ path: '../_shots/02c-photos-dir.png' })

// 「最近查看」货架：前面点开过网格照片已记录查看历史，重进主页应出现该货架
await page.goto(`${BASE}/library/photos`)
await page.waitForTimeout(1500)
const shelfTitles = (await page.locator('.shelf-head h2').allTextContents()).map((t) => t.trim())
ok('最近查看货架标题出现', shelfTitles.includes('最近查看'))
const viewedN = await page.locator('.shelf-card').count()
ok(`最近查看货架有卡片 (${viewedN})`, viewedN > 0)
// 货架卡片点击开灯箱
await page.click('.shelf-card >> nth=0')
ok('货架点击开灯箱', await page.waitForSelector('.pswp', { timeout: 20000 }).then(() => true).catch(() => false))
await closeLightbox()

// 离开页面 infuse-mode 摘除
await page.goto(`${BASE}/files`)
await page.waitForTimeout(600)
ok('离开后 infuse-mode 移除', await page.evaluate(() => !document.body.classList.contains('infuse-mode')))

await browser.close()
console.log('\n==== 控制台/网络错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
