// 系统「正在播放」会话回归（utils/mediaSession.js）：
// iOS 灵动岛与锁屏上的片名、封面、进度条、控件全部来自 Media Session API。WebKit 自建的
// 那个会话拿不到页面信息，不接管就只剩一个站名。Chromium 与 WebKit 共用这套标准 API，
// 桌面 Edge 能验的正是「我们究竟交给系统什么」：元数据、注册了哪些控件、进度入参、
// 以及离页后有没有清干净（残留会让灵动岛挂着一个已经不在播的视频）。
// 用法: node media-session-check.mjs （需服务在跑，NL_BASE 可指隔离实例，
//       NL_VIDEO 指一个可播放的视频路径，默认 /本地存储/电影/星际穿越.mp4）
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5299'
const VIDEO = process.env.NL_VIDEO || '/本地存储/电影/星际漫游.mp4'
const segs = VIDEO.split('/').filter(Boolean)
const TITLE = segs[segs.length - 1].replace(/\.[^.]+$/, '')
const DIR = segs[segs.length - 2]
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'

// 无用户手势的自动播放：起播才有 playbackState 与 positionState 可验
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

const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
// 探针：真实记录页面交给系统的入参（这些 API 只有 setter、读不回来）
await ctx.addInitScript(() => {
  const ms = navigator.mediaSession
  if (!ms) return
  window.__msActions = []
  window.__msPos = null
  const setAction = ms.setActionHandler.bind(ms)
  ms.setActionHandler = (action, handler) => {
    setAction(action, handler)
    if (handler) window.__msActions.push(action)
    else window.__msActions = window.__msActions.filter((a) => a !== action)
  }
  if (typeof ms.setPositionState === 'function') {
    const setPos = ms.setPositionState.bind(ms)
    ms.setPositionState = (state) => {
      setPos(state)
      window.__msPos = state ? { ...state } : null
    }
  }
})
const page = await ctx.newPage()
const pageErrors = []
page.on('pageerror', (e) => pageErrors.push(e.message))

await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录"), button:has-text("登录")')
await page.waitForURL(/library/, { timeout: 10000 })
// 直奔播放页：本项要验的是播放器与系统会话，不必牵扯媒体库索引与列表页
await page.goto(`${BASE}/play/${segs.map(encodeURIComponent).join('/')}`)
await page.waitForSelector('.art-video-player', { timeout: 15000 })
await page.waitForFunction(() => {
  const v = document.querySelector('.art-video-player video')
  return v && v.currentTime > 0 && !v.paused
}, { timeout: 15000 })

// ---- 播放中：元数据、控件、进度 ----
const meta = await page.evaluate(() => {
  const m = navigator.mediaSession.metadata
  return m && {
    title: m.title,
    artist: m.artist,
    art: m.artwork.map((a) => ({ src: a.src, type: a.type })),
    state: navigator.mediaSession.playbackState,
    actions: window.__msActions,
    pos: window.__msPos,
  }
})
ok('metadata 已交给系统（灵动岛/锁屏不再是空壳）', !!meta)
ok(`片名取自文件名去扩展名（${meta?.title}）`, meta?.title === TITLE)
ok(`所在目录当作专辑（${meta?.artist}）`, meta?.artist === DIR)
ok(`封面指向 480 档缩略图（${meta?.art?.[0]?.src?.split('?')[0]}）`,
  /\/api\/thumb\//.test(meta?.art?.[0]?.src || '') && /size=480/.test(meta?.art?.[0]?.src || ''))
ok('播放态同步为 playing', meta?.state === 'playing')
for (const a of ['play', 'pause', 'seekbackward', 'seekforward', 'seekto']) {
  ok(`控件已接管：${a}`, meta?.actions?.includes(a))
}
ok(`进度已上报（duration ${Math.round(meta?.pos?.duration || 0)}s / rate ${meta?.pos?.playbackRate}）`,
  meta?.pos?.duration > 0 && meta.pos.position >= 0 && meta.pos.position <= meta.pos.duration && meta.pos.playbackRate > 0)

// 封面 URL 得真能取到图，否则灵动岛依旧空着
const artRes = await page.evaluate(async (src) => {
  const r = await fetch(src)
  return { status: r.status, type: r.headers.get('content-type') }
}, meta?.art?.[0]?.src)
ok(`封面可取（HTTP ${artRes.status} ${artRes.type}）`, artRes.status === 200 && /image\//.test(artRes.type || ''))

// ---- 暂停：播放态跟随 ----
await page.evaluate(() => document.querySelector('.art-video-player video').pause())
await page.waitForTimeout(300)
ok('暂停后播放态同步为 paused',
  await page.evaluate(() => navigator.mediaSession.playbackState) === 'paused')

// ---- 离页：会话清干净，别在灵动岛挂着一个已经不在播的视频 ----
await page.goBack()
await page.waitForTimeout(600)
const after = await page.evaluate(() => ({
  meta: navigator.mediaSession.metadata,
  state: navigator.mediaSession.playbackState,
  actions: window.__msActions,
  pos: window.__msPos,
}))
ok('离页后 metadata 已清空', after.meta === null)
ok(`离页后播放态归 none（现 ${after.state}）`, after.state === 'none')
ok(`离页后控件全部摘除（残留 ${after.actions?.length ?? '?'} 个）`, after.actions?.length === 0)
ok('离页后进度状态已清空', after.pos === null)

ok(`全程无未捕获异常（${pageErrors.length ? pageErrors[0] : '无'}）`, pageErrors.length === 0)

await ctx.close()
await browser.close()
console.log(`\n${failed === 0 ? '✅' : '❌'} ${passed} passed, ${failed} failed`)
process.exit(failed === 0 ? 0 : 1)
