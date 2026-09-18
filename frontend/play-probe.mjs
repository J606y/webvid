// 播放拉流请求时序探针：把每条拉流请求拆成 排队 / 连接 / 等服务器 / 下载 四段。
//
// 起因：DevTools 的 Size/Time 两列区分不了「在排队」和「在等服务器」。实测同一文件的
// 8.5 kB 与 148 kB 请求耗时同在 1~3 秒量级，且 (disk cache) 命中也要 951 ms~2.96 s——
// 本地读盘不可能要三秒，说明时间没花在传字节上。究竟卡在哪一段，Size/Time 看不出来，
// 必须拿 CDP 的完整 timing。
//
// 用法：
//   NL_BASE=https://域名 NL_PASS=密码 node play-probe.mjs
// 可选：
//   NL_USER   登录名，默认 admin
//   NL_PATH   指定视频路径（如 /某盘/某目录/片子.mp4）。不给则列出最近 30 个视频后退出，
//             照着挑一个再传进来——卡的和不卡的各跑一次才有对照。
//   NL_SEEKS  快进次数，默认 3
//   NL_CHROME 浏览器可执行文件路径，默认系统 Edge
import { chromium } from 'playwright-core'
import { existsSync } from 'node:fs'

const BASE = (process.env.NL_BASE || '').replace(/\/+$/, '')
const USER = process.env.NL_USER || 'admin'
const PASS = process.env.NL_PASS || ''
const WANT = process.env.NL_PATH || ''
const URL_ = process.env.NL_URL || ''
const SEEKS = Number(process.env.NL_SEEKS || 3)
const CHROME = process.env.NL_CHROME || 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'

if (!BASE) bail('必须给 NL_BASE，例如 NL_BASE=https://你的域名')
if (!PASS) bail('必须给 NL_PASS（测完记得 webvid reset-password 换掉）')
if (!existsSync(CHROME)) bail(`找不到浏览器：${CHROME}\n用 NL_CHROME 指定 Edge/Chrome 的 exe 路径`)
if (!Number.isFinite(SEEKS) || SEEKS < 1) bail('NL_SEEKS 必须是正整数')

// 只在启动浏览器之前用——之后一律 throw，好让 finally 关掉浏览器，不留僵尸进程
function bail(msg) {
  console.error(msg)
  process.exit(1)
}

const browser = await chromium.launch({
  executablePath: CHROME,
  headless: true,
  // 无头下没有用户手势，不放开这条 video.play() 会被拒，探针一个字节都抓不到
  args: ['--autoplay-policy=no-user-gesture-required'],
})
let failed = false
try {
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 }, ignoreHTTPSErrors: true })
  const page = await ctx.newPage()
  page.on('pageerror', (e) => console.error('[页面异常]', e.message))

  await page.goto(`${BASE}/login`, { waitUntil: 'domcontentloaded', timeout: 30000 })
  await page.fill('input[placeholder="用户名"]', USER)
  await page.fill('input[placeholder="密码"]', PASS)
  await page.click('button:has-text("登 录"), button:has-text("登录")')
  await page.waitForURL(/library/, { timeout: 20000 })

  if (URL_) {
    // 直接吃浏览器地址栏复制来的整条 URL：全是百分号编码，绕开 Git Bash 弄坏中文参数的老坑
    await probe(ctx, page, URL_, decodeURIComponent(new URL(URL_).pathname.replace(/^\/play/, '')))
  } else if (WANT) {
    await probe(ctx, page, `${BASE}/play/` + WANT.split('/').filter(Boolean).map(encodeURIComponent).join('/'), WANT)
  } else {
    await listVideos(page)
  }
} catch (err) {
  failed = true
  console.error('\n探测失败：', err.message)
} finally {
  await browser.close()
}
process.exit(failed ? 1 : 0)

// ── 没指定文件：列清单，让用户挑了再回来 ─────────────────────────────────────
async function listVideos(page) {
  const items = await page.evaluate(async () => {
    const r = await fetch('/api/media/list?kind=video&limit=30&sort=modified&order=desc', {
      headers: { Authorization: 'Bearer ' + localStorage.getItem('nl_token') },
    })
    if (!r.ok) throw new Error(`列表接口 HTTP ${r.status}`)
    return (await r.json()).data.items.map((x) => ({ path: x.path, size: x.size }))
  })
  console.log('最近的视频（挑一个卡的、一个不卡的，各跑一次）：\n')
  for (const it of items) console.log(`  ${fmtSize(it.size).padStart(9)}  ${it.path}`)
  console.log('\n再跑一次并带上 NL_PATH=<上面某一行的路径>')
}

// ── 指定了文件：挂 CDP 抓完整 timing，起播 + 快进若干次 ───────────────────────
async function probe(ctx, page, playURL, relPath) {
  const cdp = await ctx.newCDPSession(page)
  await cdp.send('Network.enable')

  const reqs = new Map() // requestId → 记录
  let phase = 0 // 0 = 起播与顺序播放；n = 第 n 次快进之后

  cdp.on('Network.requestWillBeSent', (e) => {
    if (!isStreamURL(e.request.url)) return
    reqs.set(e.requestId, {
      sentAt: e.timestamp,
      range: e.request.headers.Range || e.request.headers.range || '(无)',
      phase,
      bytes: 0,
    })
  })
  cdp.on('Network.responseReceived', (e) => {
    const r = reqs.get(e.requestId)
    if (!r) return
    r.status = e.response.status
    r.fromCache = e.response.fromDiskCache || false
    r.timing = e.response.timing || null
  })
  // 被取消的请求走 loadingFailed，那条路上没有 encodedDataLength。只认 loadingFinished
  // 会把「传了一半被取消」误记成 0 字节——而「取消时到底传了多少」正是判断服务器是不是
  // 在白干的唯一依据。所以逐块累加。
  cdp.on('Network.dataReceived', (e) => {
    const r = reqs.get(e.requestId)
    if (!r) return
    r.recv = (r.recv || 0) + (e.encodedDataLength || e.dataLength || 0)
  })
  cdp.on('Network.loadingFinished', (e) => {
    const r = reqs.get(e.requestId)
    if (!r) return
    r.bytes = e.encodedDataLength
    r.finishedAt = e.timestamp
  })
  cdp.on('Network.loadingFailed', (e) => {
    const r = reqs.get(e.requestId)
    if (!r) return
    r.finishedAt = e.timestamp
    r.bytes = r.recv || 0 // 取消时已落地的字节，就是这条请求真正的产出
    r.failed = e.canceled ? '(canceled)' : e.errorText || '(failed)'
  })

  console.log(`探测：${relPath}\n`)
  const t0 = Date.now()
  await page.goto(playURL, { waitUntil: 'domcontentloaded', timeout: 60000 })
  await page.waitForSelector('video', { timeout: 30000 })
  await page.waitForFunction(() => document.querySelector('video')?.readyState >= 1, { timeout: 60000 })
  console.log(`元数据就绪 ${Date.now() - t0} ms`)

  const dur = await page.evaluate(() => document.querySelector('video').duration)
  if (!Number.isFinite(dur) || dur <= 0) throw new Error('拿不到视频时长，播放器没起来')

  // 实测发现同一文件的 Range 分两串各自按 32KB 独立推进，像是有两个读取者。
  // 页面上真有几个 <video> 在拉流，数一下就知道。
  await dumpVideos(page, '起播后')

  await page.evaluate(() => document.querySelector('video').play().catch(() => {}))
  console.log('顺序播放 8 秒')
  await page.waitForTimeout(8000)

  for (let i = 1; i <= SEEKS; i++) {
    phase = i
    const target = (dur * (i + 1)) / (SEEKS + 2)
    const res = await page.evaluate(async (t) => {
      const v = document.querySelector('video')
      const start = performance.now()
      v.currentTime = t
      // 等到画面真的又开始前进。只等 seeked 事件会漏掉「seek 完成但缓冲喂不上、
      // 播半秒又停」那一段——而那恰恰是要测的现象。
      await new Promise((resolve) => {
        const deadline = start + 45000
        const tick = () => {
          if (v.currentTime > t + 0.4 || performance.now() > deadline) resolve()
          else setTimeout(tick, 50)
        }
        v.play().catch(() => {})
        tick()
      })
      return { ms: Math.round(performance.now() - start) }
    }, target)
    console.log(`快进 #${i} → ${fmtTime(target)}：${res.ms} ms 后画面才动`)
    await page.waitForTimeout(6000)
    await dumpVideos(page, `快进 #${i} 后`)
  }

  await page.evaluate(() => document.querySelector('video').pause())
  report([...reqs.values()], SEEKS)
}

// 页面上到底有几个 <video> 在拉同一个文件。两个读取者互相抢带宽、各自超时取消，
// 单看网络面板只会以为是「服务器慢」。
async function dumpVideos(page, when) {
  const info = await page.evaluate(() => ({
    pip: !!document.pictureInPictureElement,
    vids: [...document.querySelectorAll('video')].map((v) => ({
      src: (v.currentSrc || v.src || '(空)').split('/').pop().slice(-42),
      t: Math.round(v.currentTime),
      paused: v.paused,
      rs: v.readyState,
      net: v.networkState, // 2 = NETWORK_LOADING，正在拉流
      buf: v.buffered.length
        ? [...Array(v.buffered.length)].map((_, i) => `${Math.round(v.buffered.start(i))}-${Math.round(v.buffered.end(i))}`).join(',')
        : '空',
    })),
  }))
  const tag = info.vids.length > 1 ? ` ⚠ 有 ${info.vids.length} 个 <video>` : ''
  console.log(`  [${when}]${tag}${info.pip ? ' 画中画开启' : ''}`)
  for (const v of info.vids) {
    console.log(
      `    video t=${v.t}s ${v.paused ? '暂停' : '播放'} readyState=${v.rs} ` +
        `networkState=${v.net}${v.net === 2 ? '(拉流中)' : ''} buffered=[${v.buf}] src…${v.src}`,
    )
  }
}

// 拉流请求：/api/raw 直出，或转码路径下的 m3u8/ts 分片
function isStreamURL(u) {
  return u.includes('/api/raw') || u.includes('/api/media/hls') || /\.(m3u8|ts)(\?|$)/.test(u)
}

// 把 CDP timing 拆成四段（ms）。timing 内各字段是相对 requestTime 的毫秒偏移，
// requestTime 与事件 timestamp 同为秒。
function split(r) {
  if (!r.timing) {
    // 缓存命中等情况没有 timing：只有总耗时可算，整段记进「排队」——本来就是等位等出来的
    const total = r.finishedAt ? (r.finishedAt - r.sentAt) * 1000 : 0
    return { queue: total, conn: 0, ttfb: 0, down: 0, total }
  }
  const t = r.timing
  const conn = t.connectEnd > 0 ? t.connectEnd - Math.max(0, t.connectStart) : 0
  // sendStart 已包含 dns/connect/ssl，减掉才是纯等位
  const queue = Math.max(0, (t.requestTime - r.sentAt) * 1000 + Math.max(0, t.sendStart) - conn)
  const ttfb = Math.max(0, t.receiveHeadersEnd - t.sendEnd)
  const end = r.finishedAt ? (r.finishedAt - t.requestTime) * 1000 : t.receiveHeadersEnd
  const down = Math.max(0, end - t.receiveHeadersEnd)
  const total = r.finishedAt ? (r.finishedAt - r.sentAt) * 1000 : queue + conn + ttfb
  return { queue, conn, ttfb, down, total }
}

function report(all, seeks) {
  const done = all.filter((r) => r.finishedAt)
  if (!done.length) {
    console.log('\n没抓到任何拉流请求——检查 isStreamURL 是否覆盖了这套播放链路的 URL')
    return
  }
  console.log(`\n${'='.repeat(78)}`)
  console.log(`共 ${all.length} 条拉流请求（完成 ${done.length}，未完成/取消 ${all.length - done.length}）`)

  for (let p = 0; p <= seeks; p++) {
    const grp = done.filter((r) => r.phase === p)
    if (!grp.length) continue
    console.log(`\n── ${p === 0 ? '起播 + 顺序播放' : `快进 #${p} 之后`} ── ${grp.length} 条请求`)
    console.log('     大小        总耗时      排队      连接  等服务器      下载   Range')
    for (const r of grp.slice(0, 12)) {
      const s = split(r)
      console.log(
        `  ${fmtSize(r.bytes).padStart(9)}  ${ms(s.total)}  ${ms(s.queue)}  ${ms(s.conn)}  ${ms(s.ttfb)}  ${ms(s.down)}  ` +
          `${r.range}${r.fromCache ? ' [cache]' : ''}${r.failed ? ' ' + r.failed : ''}`,
      )
    }
    if (grp.length > 12) console.log(`  …另有 ${grp.length - 12} 条`)
    const sums = grp.map(split)
    const agg = (k) => sums.reduce((a, s) => a + s[k], 0)
    const tot = agg('total') || 1
    console.log(
      `  小计：排队 ${pct(agg('queue'), tot)} | 连接 ${pct(agg('conn'), tot)} | ` +
        `等服务器 ${pct(agg('ttfb'), tot)} | 下载 ${pct(agg('down'), tot)}` +
        `（共 ${fmtSize(grp.reduce((a, r) => a + r.bytes, 0))}）`,
    )
  }

  // 结论：哪一段占的时间最多，瓶颈就在那儿
  const sums = done.map(split)
  const agg = (k) => sums.reduce((a, s) => a + s[k], 0)
  const parts = [
    ['排队（浏览器连接池占满，请求在等位）', agg('queue')],
    ['连接（每条请求重建 TCP/TLS）', agg('conn')],
    ['等服务器（VPS 收到请求到吐出第一个字节）', agg('ttfb')],
    ['下载（真的在传字节）', agg('down')],
  ].sort((a, b) => b[1] - a[1])
  const tot = parts.reduce((a, p) => a + p[1], 0) || 1
  console.log(`\n${'='.repeat(78)}\n时间都花在哪：`)
  for (const [name, v] of parts) console.log(`  ${pct(v, tot).padStart(6)}  ${name}`)
  const bytes = done.reduce((a, r) => a + r.bytes, 0)
  console.log(`\n合计拉了 ${fmtSize(bytes)}，折合 ${fmtSize(bytes / Math.max(tot / 1000, 0.001))}/s`)
  console.log(`请求数 ${done.length} 条，平均每条 ${fmtSize(bytes / done.length)} / ${ms(tot / done.length).trim()}`)
}

function fmtSize(b) {
  if (!b) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB']
  const i = Math.min(Math.floor(Math.log(b) / Math.log(1024)), u.length - 1)
  return `${(b / 1024 ** i).toFixed(1)} ${u[i]}`
}

function fmtTime(s) {
  return `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, '0')}`
}

function ms(v) {
  return `${Math.round(v)} ms`.padStart(9)
}

function pct(v, tot) {
  return `${((v / tot) * 100).toFixed(1)}%`
}
