// 联调冒烟脚本：启动本机 Chrome(CDP) → 登录 → 逐页截图 + 收集控制台错误。
// 用法: node e2e-check.mjs
import { chromium } from 'playwright-core'
import { execSync } from 'node:child_process'

const BASE = 'http://localhost:5243'
const CHROME = 'C:/Program Files/Google/Chrome/Application/chrome.exe'
const OUT = '../_shots'

execSync(`mkdir -p ${OUT}`, { shell: 'bash' })

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })

const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))
page.on('requestfailed', (r) => errors.push(`[requestfailed] ${r.url()} ${r.failure()?.errorText}`))

async function shot(name) {
  await page.waitForTimeout(1200)
  await page.screenshot({ path: `${OUT}/${name}.png` })
  console.log(`shot: ${name}`)
}

// 登录（/ 重定向到视频库）
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)
await page.waitForTimeout(2500) // 等缩略图生成
await shot('01-library-video')

await page.goto(`${BASE}/library/photos`)
await shot('02-library-photos')

// 照片灯箱
await page.click('.photo-grid .cell >> nth=0')
await shot('03-lightbox')
await page.keyboard.press('Escape')

// 播放页（视频库 Hero 的立即播放）
await page.goto(`${BASE}/library/video`)
await page.waitForTimeout(800)
const heroBtn = page.locator('.hero-btn').first()
if (await heroBtn.count()) {
  await heroBtn.click()
  await page.waitForTimeout(3000)
  await shot('04-play')
}

// 文件管理(深层路由直接进,验证 F5 场景)
await page.goto(`${BASE}/files/%E6%9C%AC%E5%9C%B0%E5%AD%98%E5%82%A8`)
await shot('05-files')

// 跨存储转存任务抽屉（需已挂载 /本地存储2）
const hasStorage2 = await page.evaluate(async () => {
  const r = await fetch('/api/fs/get?path=%2F%E6%9C%AC%E5%9C%B0%E5%AD%98%E5%82%A82', {
    headers: { Authorization: 'Bearer ' + localStorage.getItem('nl_token') },
  })
  return (await r.json()).code === 200
})
if (hasStorage2) {
  await page.evaluate(async () => {
    const h = {
      Authorization: 'Bearer ' + localStorage.getItem('nl_token'),
      'Content-Type': 'application/json',
    }
    await fetch('/api/fs/copy', {
      method: 'POST', headers: h,
      body: JSON.stringify({ paths: ['/本地存储/文档'], dst_dir: '/本地存储2' }),
    })
  })
  await page.click('button[title="传输任务"]')
  await shot('05b-tasks-drawer')
  await page.keyboard.press('Escape')
  await page.evaluate(async () => {
    const h = {
      Authorization: 'Bearer ' + localStorage.getItem('nl_token'),
      'Content-Type': 'application/json',
    }
    // 读完响应体再返回，防止随后的 page.goto 把请求中断在半路
    const r1 = await fetch('/api/fs/remove', {
      method: 'POST', headers: h,
      body: JSON.stringify({ paths: ['/本地存储2/文档'] }),
    })
    await r1.json()
    const r2 = await fetch('/api/tasks/done', { method: 'DELETE', headers: h })
    await r2.json()
  })
} else {
  console.log('skip 05b: /本地存储2 未挂载')
}

// md 预览抽屉
await page.goto(`${BASE}/files/%E6%9C%AC%E5%9C%B0%E5%AD%98%E5%82%A8/%E6%96%87%E6%A1%A3`)
await page.waitForTimeout(800)
await page.click('text=说明.md')
await shot('06-md-drawer')
await page.keyboard.press('Escape')

// 搜索
await page.goto(`${BASE}/search`)
await page.fill('input[placeholder="输入文件或目录名…"]', '电影')
await page.keyboard.press('Enter')
await shot('07-search')

// 后台
await page.goto(`${BASE}/@admin`)
await shot('08-admin')
await page.click('.el-tabs__item:has-text("存储管理")')
await shot('09-admin-storage')
// 弹窗须完整浮在页面中央（回归：glass backdrop-filter 裁剪 fixed 弹窗）
await page.click('button:has-text("添加存储")')
await shot('09b-admin-storage-dialog')
await page.keyboard.press('Escape')
await page.click('.el-tabs__item:has-text("用户管理")')
await shot('10-admin-users')
await page.click('button:has-text("添加用户")')
await shot('10b-admin-user-dialog')
await page.keyboard.press('Escape')
await page.click('.el-tabs__item:has-text("索引管理")')
await shot('11-admin-index')

await browser.close()

console.log('\n==== 控制台/网络错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
