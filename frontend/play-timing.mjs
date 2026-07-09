// 播放页时延测量：导航→ArtPlayer 挂载→视频元数据就绪
import { chromium } from 'playwright-core'
const BASE = 'http://localhost:5243'
const browser = await chromium.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)

// 找一个 OneDrive 上的 mp4
const p = await page.evaluate(async () => {
  const r = await fetch('/api/media/list?kind=video&limit=200&sort=modified&order=desc', {
    headers: { Authorization: 'Bearer ' + localStorage.getItem('nl_token') },
  })
  const items = (await r.json()).data.items
  const v = items.find(x => x.path.startsWith('/test/') && x.path.endsWith('.mp4'))
  return v ? v.path : null
})
if (!p) { console.log('未找到 /test 下的 mp4'); process.exit(1) }
console.log('测试视频:', p)

for (let round = 1; round <= 2; round++) {
  await page.goto(`${BASE}/library/video`)
  await page.waitForTimeout(1000)
  const url = `${BASE}/play/` + p.split('/').filter(Boolean).map(encodeURIComponent).join('/')
  const t0 = Date.now()
  await page.goto(url)
  await page.waitForSelector('.art-video-player', { timeout: 30000 })
  const tPlayer = Date.now() - t0
  await page.waitForFunction(() => {
    const v = document.querySelector('video')
    return v && v.readyState >= 1
  }, { timeout: 30000 })
  const tMeta = Date.now() - t0
  console.log(`第${round}次: 播放器挂载 ${tPlayer}ms, 元数据就绪 ${tMeta}ms`)
}
await browser.close()
