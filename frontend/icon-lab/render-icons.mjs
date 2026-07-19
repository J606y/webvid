// WebVid 图标资产生成管线：icon-master.svg（全出血方版母版）→ public/ 下全套落地资产。
// 改图标只改 icon-master.svg 后重跑本脚本 + 手动同步 public/favicon.svg 的圆角包装
// （见本脚本 favicon 段），再 npm run build + go build 重嵌。
// 用法: cd frontend && node icon-lab/render-icons.mjs
import { chromium } from 'playwright-core'
import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const pub = join(here, '..', 'public')
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'

const master = readFileSync(join(here, 'icon-master.svg'), 'utf8')

// 圆角 favicon.svg：母版包一层 rx 22.4% 裁切（iOS 瓦片圆角），标签页/顶栏/登录页共用
const defs = master.match(/<defs>[\s\S]*<\/defs>/)[0]
const body = master.match(/<rect[\s\S]*(?=<\/svg>)/)[0].trimEnd()
writeFileSync(join(pub, 'favicon.svg'),
  '<svg xmlns="http://www.w3.org/2000/svg" width="512" height="512" viewBox="0 0 512 512">\n' +
  '  <!-- WebVid favicon（由 icon-lab/icon-master.svg 经 render-icons.mjs 生成，勿手改）\n' +
  '       圆角 22.4% 的 app 瓦片形态，浏览器标签/顶栏/登录页共用 -->\n' +
  '  <clipPath id="tile"><rect width="512" height="512" rx="115"/></clipPath>\n' +
  defs.replace(/^/gm, '  ') + '\n  <g clip-path="url(#tile)">\n' +
  body.replace(/^/gm, '    ') + '\n  </g>\n</svg>\n')
console.log('favicon.svg')

// 全出血 PNG（PWA any+maskable / apple-touch-icon 由系统自己裁圆角）+ 32px 标签兜底
const browser = await chromium.launch({ executablePath: CHROME, headless: true })
for (const [name, size] of [
  ['icon-512.png', 512], ['icon-192.png', 192],
  ['apple-touch-icon.png', 180], ['favicon-32.png', 32],
]) {
  const page = await browser.newPage({ viewport: { width: size, height: size }, deviceScaleFactor: 1 })
  await page.setContent(`<body style="margin:0"><img src="data:image/svg+xml;base64,${
    Buffer.from(master).toString('base64')}" width="${size}" height="${size}" style="display:block"></body>`)
  await page.waitForTimeout(250)
  await page.screenshot({ path: join(pub, name) })
  await page.close()
  console.log(name)
}
await browser.close()

// favicon.ico：PNG-in-ICO 容器（Vista+ 全平台认），兜住老 UA 硬编码请求 /favicon.ico
const png = readFileSync(join(pub, 'favicon-32.png'))
const ico = Buffer.alloc(22)
ico.writeUInt16LE(0, 0)            // reserved
ico.writeUInt16LE(1, 2)            // type: icon
ico.writeUInt16LE(1, 4)            // count
ico.writeUInt8(32, 6)              // width
ico.writeUInt8(32, 7)              // height
ico.writeUInt8(0, 8)               // palette
ico.writeUInt8(0, 9)               // reserved
ico.writeUInt16LE(1, 10)           // planes
ico.writeUInt16LE(32, 12)          // bpp
ico.writeUInt32LE(png.length, 14)  // bytes
ico.writeUInt32LE(22, 18)          // offset
writeFileSync(join(pub, 'favicon.ico'), Buffer.concat([ico, png]))
console.log('favicon.ico')
