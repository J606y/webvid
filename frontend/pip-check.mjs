// 系统画中画回归（utils/pip.js + utils/playerHost.js）：
//
// iPhone 的画中画走 WebKit 的 webkitSetPresentationMode，桌面 Chromium 没有这套 API，
// 所以这里给 HTMLVideoElement.prototype 打桩把 iPhone 的形状装出来（同时把标准 API 的
// document.pictureInPictureEnabled 按 iOS 的实际情况置假），验的是我们自己的逻辑：
// 按钮按真实能力显隐、进出小窗的调用与状态同步、离开播放页后播放器寄存不销毁、
// 还原键回播放页接回、关闭键收摊并补报进度。
// 真机行为（系统小窗本身）只能在 iPhone 上确认，本脚本守的是它周围的全部逻辑。
//
// 用法: node pip-check.mjs （需服务在跑，NL_BASE 可指隔离实例，
//       NL_VIDEO 指直连视频，NL_HLS 指需转码的视频）
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5299'
const VIDEO = process.env.NL_VIDEO || '/本地存储/电影/星际漫游.mp4'
const HLS = process.env.NL_HLS || '/本地存储/电影/转码样片/山川印象_remux.mkv'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const playURL = (p) => `${BASE}/play/${p.split('/').filter(Boolean).map(encodeURIComponent).join('/')}`

// 无用户手势的自动播放：寄存要验的是"人走了视频还在播"，得先真的播起来
const browser = await chromium.launch({
  executablePath: CHROME,
  headless: true,
  args: ['--autoplay-policy=no-user-gesture-required'],
})
let passed = 0, failed = 0
const ok = (name, cond) => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}`) }
}

// iPhone 形状的桩：只有 WebKit 那套 API，标准 API 关掉。
// __pipUnsupported 用来模拟"系统设置里关掉了画中画"——能力为假，按钮就该消失。
const stub = () => {
  Object.defineProperty(document, 'pictureInPictureEnabled', { value: false, configurable: true })
  const modes = new WeakMap()
  window.__pipCalls = []
  Object.defineProperty(HTMLVideoElement.prototype, 'webkitPresentationMode', {
    configurable: true,
    get() { return modes.get(this) || 'inline' },
  })
  HTMLVideoElement.prototype.webkitSupportsPresentationMode = function (mode) {
    return !window.__pipUnsupported && (mode === 'picture-in-picture' || mode === 'inline')
  }
  HTMLVideoElement.prototype.webkitSetPresentationMode = function (mode) {
    window.__pipCalls.push(mode)
    modes.set(this, mode)
    this.dispatchEvent(new Event('webkitpresentationmodechanged'))
  }
}

async function login(page) {
  await page.goto(`${BASE}/login`)
  await page.fill('input[placeholder="用户名"]', 'admin')
  await page.fill('input[placeholder="密码"]', 'admin123')
  await page.click('button:has-text("登 录"), button:has-text("登录")')
  await page.waitForURL(/library/, { timeout: 10000 })
}

// 起播到有画面：寄存/接回的断言全靠 currentTime 在走
async function waitPlaying(page) {
  await page.waitForSelector('.wv-player .art-video-player', { timeout: 15000 })
  await page.waitForFunction(() => {
    const v = document.querySelector('.wv-player video')
    return v && !v.paused && v.currentTime > 0
  }, { timeout: 15000 })
}

// 点画中画按钮（控件闲置会淡出，先把鼠标移进播放器）
async function clickPip(page) {
  await page.hover('.wv-player')
  await page.waitForTimeout(200)
  await page.click('.wv-player .art-control-pip')
  await page.waitForTimeout(200)
}

const pageErrors = []

// ---- 主流程：直连 mp4 ----
{
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  await ctx.addInitScript(stub)
  const page = await ctx.newPage()
  page.on('pageerror', (e) => pageErrors.push(e.message))
  const played = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url().includes('/api/media/played')) {
      try { played.push(JSON.parse(r.postData() || '{}')) } catch { /* 非 JSON 不计 */ }
    }
  })

  await login(page)
  await page.goto(playURL(VIDEO))
  await waitPlaying(page)

  ok('画中画按钮已出现（能力为真）', await page.$('.wv-player .art-control-pip') !== null)
  ok('播放器样式已全局化生效（控件仍是液态玻璃胶囊）', await page.evaluate(() => {
    const c = document.querySelector('.wv-player .art-controls .art-control')
    const s = c && getComputedStyle(c)
    return !!s && s.minWidth === '42px' && s.borderRadius === '13px'
  }))

  // 1) 进小窗
  await clickPip(page)
  ok('点按钮 → 进系统小窗', await page.evaluate(() => window.__pipCalls.at(-1) === 'picture-in-picture'))

  // 2) 离开播放页 → 寄存不销毁
  await page.click('.topbar .nav-item >> nth=0')
  await page.waitForURL(/library\/video/, { timeout: 10000 })
  const parked = await page.evaluate(() => {
    const v = document.querySelector('#wv-player-park .wv-player video')
    return { has: !!v, paused: v ? v.paused : true, t: v ? v.currentTime : 0 }
  })
  ok('离开播放页后播放器寄存在隐藏宿主里', parked.has)
  ok('寄存中仍在播（小窗不断）', parked.has && !parked.paused)
  await page.waitForTimeout(1200)
  const advanced = await page.evaluate(() => document.querySelector('#wv-player-park video')?.currentTime || 0)
  ok(`寄存中进度继续走（${parked.t.toFixed(1)}s → ${advanced.toFixed(1)}s）`, advanced > parked.t)
  ok('寄存宿主没被 display:none 藏起来（那会让 WebKit 收掉小窗）', await page.evaluate(() => {
    const s = getComputedStyle(document.getElementById('wv-player-park'))
    return s.display !== 'none' && s.visibility !== 'hidden'
  }))

  // 3) 回播放页 → 接回，不重建、不回到零
  await page.goBack()
  await page.waitForURL(/\/play\//, { timeout: 10000 })
  await page.waitForTimeout(400)
  const back = await page.evaluate(() => {
    const v = document.querySelector('.player-slot .wv-player video')
    return {
      inSlot: !!v,
      t: v ? v.currentTime : 0,
      parkEmpty: !document.querySelector('#wv-player-park .wv-player'),
      lastCall: window.__pipCalls.at(-1),
    }
  })
  ok('回播放页后播放器搬回页面插槽', back.inSlot)
  ok('寄存宿主已腾空', back.parkEmpty)
  ok('接回时退出小窗（webkitSetPresentationMode inline）', back.lastCall === 'inline')
  ok(`接回后进度连续、没有重新起播（${back.t.toFixed(1)}s）`, back.t >= advanced)

  // 4) 小窗还原键：寄存中退出小窗且还在播 → 自动回播放页接回
  await clickPip(page)
  await page.click('.topbar .nav-item >> nth=0')
  await page.waitForURL(/library\/video/, { timeout: 10000 })
  await page.evaluate(() => document.querySelector('#wv-player-park video').webkitSetPresentationMode('inline'))
  await page.waitForURL(/\/play\//, { timeout: 10000 })
  await page.waitForTimeout(400)
  ok('小窗还原键 → 回到播放页并接回', await page.evaluate(() =>
    !!document.querySelector('.player-slot .wv-player video')))

  // 5) 小窗关闭键：WebKit 关小窗会顺带暂停 → 收摊并补报末次进度
  await clickPip(page)
  await page.click('.topbar .nav-item >> nth=0')
  await page.waitForURL(/library\/video/, { timeout: 10000 })
  const lastT = await page.evaluate(() => document.querySelector('#wv-player-park video').currentTime)
  played.length = 0
  await page.evaluate(() => {
    const v = document.querySelector('#wv-player-park video')
    v.pause()
    v.webkitSetPresentationMode('inline')
  })
  await page.waitForTimeout(600)
  ok('小窗关闭键 → 播放器收摊，页面无残留', await page.evaluate(() =>
    !document.querySelector('.wv-player')))
  const last = played.at(-1)
  ok(`收摊时补报末次进度（${last?.position}s / 断点 ${lastT.toFixed(1)}s）`,
    !!last && last.path === VIDEO && Math.abs(last.position - lastT) <= 2)

  await ctx.close()
}

// ---- 能力为假：不给按钮，而不是给一颗点了报错的按钮 ----
{
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  await ctx.addInitScript(stub)
  await ctx.addInitScript(() => { window.__pipUnsupported = true })
  const page = await ctx.newPage()
  page.on('pageerror', (e) => pageErrors.push(e.message))
  await login(page)
  await page.goto(playURL(VIDEO))
  await waitPlaying(page)
  await page.waitForTimeout(500)
  ok('系统关掉画中画时按钮不出现', await page.$('.wv-player .art-control-pip') === null)
  await ctx.close()
}

// ---- 转码播放：customType 重构后 hls.js 分支照常起播、按钮照常给 ----
{
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  await ctx.addInitScript(stub)
  const page = await ctx.newPage()
  page.on('pageerror', (e) => pageErrors.push(e.message))
  await login(page)
  await page.goto(playURL(HLS))
  await waitPlaying(page)
  ok('转码播放（hls.js）照常起播', await page.evaluate(() =>
    document.querySelector('.wv-player video').currentTime > 0))
  ok('转码播放也给画中画按钮', await page.$('.wv-player .art-control-pip') !== null)
  await ctx.close()
}

ok(`无控制台异常（${pageErrors.length} 条）`, pageErrors.length === 0)
if (pageErrors.length) console.error(pageErrors.join('\n'))

await browser.close()
console.log(`\n通过 ${passed} / 失败 ${failed}`)
process.exit(failed ? 1 : 0)
