// iPhone 长按弹选词放大镜的现场探针（2026-09-18）。
//
// 为什么要它：这个 bug 只在真机复现，Chromium 模拟不出来；而「放大镜出来了」这一个现象
// 对应两种完全不同的机制，改法南辕北辙 ——
//   A. DOM 文字选区：iOS 长按时 WebKit 取离触点最近的可选文字起选区（播放器禁了就往外抓
//      页面标题）。这种会派发 selectionchange，能读出选中了谁。
//   B. 系统识别（实况文本 / Live Text）：直接在视频画面上认字并选中，**不是 DOM 选区**，
//      selectionchange 一次都不会来。
// 两者都长成「放大镜 + 两枚选择柄」，肉眼分不出。这里把两边的证据都打到屏幕上，
// 手机没有开发者工具也能读。
//
// 用法：
//   node select-probe.mjs          然后用 iPhone 打开它打印的局域网地址
//   NL_PORT=5299                   换端口
//
// 页面结构与播放页一致：上面一行文件名（页面里唯一的可选文字）、下面播放器。
// 页面上的开关可以现场切「整页禁选」的开/关，同一台手机上对比两次，结论才站得住。
import { createServer } from 'node:http'
import { existsSync, readFileSync } from 'node:fs'
import { networkInterfaces } from 'node:os'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const HERE = dirname(fileURLToPath(import.meta.url))
const PORT = Number(process.env.NL_PORT || 5299)
const SAMPLE = join(HERE, '_gesture-sample.mp4')

if (!existsSync(SAMPLE)) {
  console.error(`缺样本视频：${SAMPLE}\n先跑一次 node gesture-check.mjs，它会用 ffmpeg 造出来`)
  process.exit(1)
}

const PAGE = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<title>长按探针</title>
<style>
  html,body{margin:0;background:#000;color:#fff;font:14px/1.4 -apple-system,PingFang SC,sans-serif}
  .play-page{padding:12px}
  /* 整页禁选：与 Play.vue 里那条一字不差，靠 body.no-select 现场开关 */
  body.no-select .play-page{-webkit-touch-callout:none;-webkit-user-select:none;user-select:none}
  html.ns-root,html.ns-root body{-webkit-touch-callout:none;-webkit-user-select:none;user-select:none}
  .head{display:flex;align-items:center;gap:8px;margin-bottom:8px}
  .title{margin:0;font-size:15px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  .player{aspect-ratio:16/9;width:100%;border-radius:14px;overflow:hidden}
  .ops{display:flex;flex-wrap:wrap;gap:8px;margin:10px 0}
  button{font:inherit;padding:8px 14px;border-radius:10px;border:1px solid #555;background:#222;color:#fff}
  #mark{background:#a11;border-color:#d33}
  /* 样式读数固定在这儿，不进滚动日志 —— 它是全场的前提，不能被后面的事件顶走 */
  #css{margin:0 0 8px;padding:8px;background:#111;border-radius:10px;
       font:11px/1.45 ui-monospace,Menlo,monospace;white-space:pre-wrap;
       -webkit-user-select:none;user-select:none}
  #log{margin:0;padding:8px;background:#111;border-radius:10px;height:42vh;overflow:auto;
       font:11px/1.35 ui-monospace,Menlo,monospace;white-space:pre-wrap;word-break:break-all;
       -webkit-user-select:none;user-select:none}
  ${readFileSync(join(HERE, 'src/assets/player.css'), 'utf8')}
</style>
<script src="/artplayer.js"></script></head>
<body class="no-select">
<div class="play-page">
  <div class="head"><span>←</span><h1 class="title">这行文件名是页面上唯一的可选文字.mkv</h1></div>
  <div id="app" class="player"></div>
  <div class="ops">
    <button id="toggle">整页禁选：开</button>
    <button id="mark">刚才弹放大镜了</button>
    <button id="clear">清空日志</button>
  </div>
  <!-- 四种拦法，一次只开一个试：放大镜是 WebKit 的「放光标」手势，user-select 管不住它，
       下面这些是能从网页这侧够到的所有闸门，逐个验哪扇真能关上。 -->
  <div class="ops" id="tries"></div>
  <pre id="css">（样式读数加载中）</pre>
  <pre id="log"></pre>
</div>
<script type="module">
  import { attachPlayerGestures } from '/playerGestures.js'
  const $log = document.getElementById('log')
  const stamp = () => new Date().toTimeString().slice(3, 8) + '.' + String(Date.now() % 1000).padStart(3, '0')
  const log = (s) => { $log.textContent = stamp() + ' ' + s + '\\n' + $log.textContent }

  // 选区落在谁身上：文本节点要连它父元素一起报，光看 nodeName 全是 #text，分不出是标题还是别处
  function where(n) {
    if (!n) return 'null'
    if (n.nodeType === 3) {
      const p = n.parentElement
      return \`#text("\${(n.textContent || '').trim().slice(0, 20)}") in <\${p ? p.tagName.toLowerCase() : '?'}.\${p ? (p.className || '-') : '?'}>\`
    }
    return \`<\${n.nodeName.toLowerCase()}.\${n.className || '-'}>\`
  }

  document.addEventListener('selectionchange', () => {
    const sel = document.getSelection()
    log(\`selectionchange 空选=\${sel.isCollapsed} 选中="\${sel.toString().slice(0, 28)}" @ \${where(sel.anchorNode)}\`)
  })
  document.addEventListener('contextmenu', (e) => log('contextmenu @ ' + where(e.target)))
  for (const n of ['touchstart', 'touchend', 'touchcancel']) {
    document.addEventListener(n, (e) => log(n + ' @ ' + where(e.target)), { passive: true })
  }

  const art = new Artplayer({
    container: '#app', url: '/sample.mp4', volume: 0, muted: true,
    theme: '#ff0000', backdrop: false, setting: true, playbackRate: true,
    fullscreen: false, fullscreenWeb: true, hotkey: true, autoSize: false, autoplay: true,
    // 与 Play.vue 同一颗纯三角：探针要和线上长一个样，不然会把「探针的默认图标」看成回归
    icons: { state: '<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 24 24"><path d="M8 5v14l11-7z"></path></svg>' },
  })
  // 样式读数写在固定区块里，随禁选开关实时重算 —— 属性没落到元素上，后面所有推断都不成立
  function dumpCss() {
    const rows = [['播放器', '.art-video-player'], ['video', '.art-video'], ['标题', '.title'], ['页面', '.play-page']]
    document.getElementById('css').textContent = rows.map(([name, sel]) => {
      const el = document.querySelector(sel)
      if (!el) return \`\${name}：缺元素\`
      const c = getComputedStyle(el)
      return \`\${name.padEnd(4, '　')} user-select=\${c.webkitUserSelect || c.userSelect || '-'}  callout=\${c.webkitTouchCallout || '-'}\`
    }).join('\\n')
  }

  art.on('ready', () => {
    attachPlayerGestures(art)
    dumpCss()
    // 倍速真进了没有：光看 touchstart/touchend 分不出长按够不够时长
    const $boost = document.querySelector('.ges-boost')
    if ($boost) {
      new MutationObserver(() => log($boost.classList.contains('is-on') ? '>>> 进入 2 倍速' : '<<< 退出倍速'))
        .observe($boost, { attributes: true, attributeFilter: ['class'] })
    }
    log('[就绪] 长按画面进倍速；放大镜一出现，立刻点红色那颗按钮打个标记')
  })

  document.getElementById('toggle').onclick = () => {
    const on = document.body.classList.toggle('no-select')
    document.getElementById('toggle').textContent = '整页禁选：' + (on ? '开' : '关')
    log('[切换] 整页禁选 ' + (on ? '开' : '关'))
    dumpCss()
  }
  // 人工标记：放大镜出现的那一刻打个桩，才能和上面几行事件对上时间
  document.getElementById('mark').onclick = () => {
    const sel = document.getSelection()
    log(\`★★★ 放大镜出现 —— 此刻选区：空选=\${sel.isCollapsed} "\${sel.toString().slice(0, 28)}" @ \${where(sel.anchorNode)}\`)
  }

  // ---- 四种拦法 ----
  // 日志已经证明：user-select 全是 none，WebKit 照样在播放器上放了个折叠光标（空选=true）。
  // 能从网页这侧够到这个手势的只剩下面几扇门，逐个开关验，哪扇管用就按哪扇去改生产代码。
  const onSelStart = (e) => e.preventDefault()
  const onTouchStart = (e) => e.preventDefault()
  let clearing = false
  const onSelChange = () => {
    if (clearing) return
    clearing = true // removeAllRanges 自己会再派发一次 selectionchange，别递归
    try { document.getSelection().removeAllRanges() } catch { /* 忽略 */ }
    clearing = false
  }
  const tries = [
    ['拦 selectstart', {
      on: () => document.addEventListener('selectstart', onSelStart),
      off: () => document.removeEventListener('selectstart', onSelStart),
    }],
    ['html/body 也禁选', {
      on: () => document.documentElement.classList.add('ns-root'),
      off: () => document.documentElement.classList.remove('ns-root'),
    }],
    ['选区一变就清掉', {
      on: () => document.addEventListener('selectionchange', onSelChange),
      off: () => document.removeEventListener('selectionchange', onSelChange),
    }],
    ['拦 touchstart（会废掉双击快进）', {
      on: () => document.addEventListener('touchstart', onTouchStart, { passive: false }),
      off: () => document.removeEventListener('touchstart', onTouchStart),
    }],
  ]
  const $tries = document.getElementById('tries')
  for (const [name, t] of tries) {
    const b = document.createElement('button')
    let on = false
    b.textContent = name + '：关'
    b.onclick = () => {
      on = !on
      ;(on ? t.on : t.off)()
      b.textContent = name + '：' + (on ? '开' : '关')
      b.style.background = on ? '#164' : '#222'
      log(\`[试] \${name} \${on ? '开' : '关'}\`)
    }
    $tries.appendChild(b)
  }
  document.getElementById('clear').onclick = () => { $log.textContent = '' }
</script></body></html>`

const files = {
  '/artplayer.js': [join(HERE, 'node_modules/artplayer/dist/artplayer.js'), 'text/javascript'],
  '/playerGestures.js': [join(HERE, 'src/utils/playerGestures.js'), 'text/javascript'],
  '/sample.mp4': [SAMPLE, 'video/mp4'],
}

createServer((req, res) => {
  const path = req.url.split('?')[0]
  if (path === '/') {
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' })
    return res.end(PAGE)
  }
  const hit = files[path]
  if (!hit) { res.writeHead(404); return res.end('no') }
  const body = readFileSync(hit[0])
  res.writeHead(200, { 'content-type': hit[1], 'content-length': body.length, 'accept-ranges': 'bytes', 'cache-control': 'no-store' })
  res.end(body)
}).listen(PORT, '0.0.0.0', () => {
  const ips = Object.values(networkInterfaces()).flat()
    .filter((i) => i && i.family === 'IPv4' && !i.internal).map((i) => i.address)
  console.log('探针已起，用 iPhone 打开：')
  for (const ip of ips) console.log(`  http://${ip}:${PORT}`)
  console.log('Ctrl+C 结束')
})
