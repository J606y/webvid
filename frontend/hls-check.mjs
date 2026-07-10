// M10 转码播放验证：对 转码样片/ 目录逐个格式验证「能播能拖」，
// mp4 验证 direct 不请求 HLS。用法: node hls-check.mjs（需服务在跑）
import { chromium } from 'playwright-core'
import { execSync } from 'node:child_process'

const BASE = 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const OUT = '../_shots'
const DIR = '/本地存储/电影/转码样片'

execSync(`mkdir -p ${OUT}`, { shell: 'bash' })

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })

const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

let pass = 0, fail = 0
function check(name, ok, extra = '') {
  if (ok) { pass++; console.log(`  ✓ ${name} ${extra}`) }
  else { fail++; console.log(`  ✗ ${name} ${extra}`) }
}

const enc = (p) => p.split('/').map(encodeURIComponent).join('/')

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)

// 等 video.currentTime 前进到 > t
async function waitPlaying(minT, timeout = 30000) {
  try {
    await page.waitForFunction(
      (t) => { const v = document.querySelector('video'); return v && !v.paused && v.currentTime > t },
      minT, { timeout },
    )
    return true
  } catch { return false }
}

async function playAndSeek(file, expectHls, seekTo, shotName) {
  console.log(`== ${file}`)
  const hlsReqs = []
  const onReq = (r) => { if (r.url().includes('/api/video/hls/')) hlsReqs.push(r.url()) }
  page.on('request', onReq)

  await page.goto(`${BASE}/play${enc(DIR + '/' + file)}`)
  check('起播', await waitPlaying(0.5), `(currentTime>0.5)`)
  check(expectHls ? '走 HLS' : '不走 HLS(direct)', expectHls === hlsReqs.length > 0, `hls请求=${hlsReqs.length}`)

  if (seekTo != null) {
    await page.evaluate((t) => { document.querySelector('video').currentTime = t }, seekTo)
    const ok = await page.waitForFunction(
      (t) => { const v = document.querySelector('video'); return v && !v.paused && v.currentTime > t + 0.5 },
      seekTo, { timeout: 45000 },
    ).then(() => true).catch(() => false)
    check(`拖动到 ${seekTo}s 后续播`, ok)
    // 回拖开头再验证一次（vod 模式触发 -ss 重启回放早段）
    await page.evaluate(() => { document.querySelector('video').currentTime = 1 })
    const ok2 = await page.waitForFunction(
      () => { const v = document.querySelector('video'); return v && !v.paused && v.currentTime > 1.5 && v.currentTime < 15 },
      { timeout: 45000 },
    ).then(() => true).catch(() => false)
    check('回拖到 1s 后续播', ok2)
  }
  if (shotName) await page.screenshot({ path: `${OUT}/${shotName}.png` })
  page.off('request', onReq)
}

// 各格式逐个验证（30s 样片，拖到 24s 接近末尾）
await playAndSeek('山川印象_remux.mkv', true, 24, '15-hls-remux')
await playAndSeek('河谷风光_h265.mkv', true, 24, '16-hls-h265')
await playAndSeek('环绕声测试_dts.mkv', true, 24, null)
await playAndSeek('老式录像.avi', true, 24, '17-hls-avi')
await playAndSeek('街头速写.flv', true, 24, null)
await playAndSeek('都市剪影.wmv', true, 24, null)
// mpegts：AAC 是 ADTS 帧，event remux 须挂 aac_adtstoasc（曾起播即死只出 0.28s 截断片）
await playAndSeek('转播录像.ts', true, 24, '18-hls-ts')
// mp4 direct：不发任何 /api/video/hls 请求
await playAndSeek('../星际漫游.mp4', false, null, null)

// 汇总
console.log(`\n通过 ${pass} / ${pass + fail}`)
const meaningful = errors.filter((e) => !e.includes('ERR_ABORTED'))
if (meaningful.length) { console.log('控制台错误:'); meaningful.forEach((e) => console.log('  ' + e)) }
else console.log('控制台零错误（ERR_ABORTED 离页良性除外）')
await browser.close()
process.exit(fail || meaningful.length ? 1 : 0)
