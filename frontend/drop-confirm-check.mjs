// 拖放上传：落区只剩上传队列抽屉，且必须接得住系统（桌面/资源管理器）拖进来的文件。
//
// 背景：原先整个窗口都是落区，拖别的东西路过就全屏泛蓝、误触一松手就传，且 dragleave
// 不可靠导致遮罩收不回去。现在整页落区已删，落区只有打开着的上传队列抽屉。
//
// 本脚本验证：
//   1. 整页落区确已移除，落区之外的文件拖放被吞掉——不上传，也不会让浏览器把文件
//      当导航打开（那会直接离开当前页，正在进行的上传全部中断）。
//   2. 抽屉是落区且铺满抽屉体：dragenter 与 dragover 都被 preventDefault（这才是
//      「这个元素收不收放置」的真正开关），框高亮，高亮靠心跳自动收起。
//   3. 落下先确认，取消则一个字节都不传；确认后照常上传。
//
// 两项本脚本测不到、只能真机拖一次才算数：
//   · dropEffect='copy'。Chromium 对脚本构造的 DataTransfer 把 effectAllowed 与
//     dropEffect 的写入一律当无操作（恒为 'none'，isTrusted=false）。它影响的是系统
//     拖拽时的光标与源程序行为——真实拖拽里没设成 copy 就会显禁止符号、drop 不触发。
//   · 拖进来的是文件夹时跳过。合成 DataTransfer 的 items 没有 webkitGetAsEntry 目录项。
//
// 用法: NL_BASE=http://localhost:5243 node drop-confirm-check.mjs （需服务在跑）
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const DIR = '本地存储'
const FNAME = `e2e-drop-${Date.now()}.txt`

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

const uploads = []
page.on('response', (r) => {
  if (r.url().includes('/api/fs/upload')) uploads.push(r.status())
})

let passed = 0, failed = 0
const ok = (name, cond, extra = '') => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}${extra ? ` (${extra})` : ''}`) }
}

// 造一次拖拽，DataTransfer 存在 window 上跨调用复用（同一次拖拽的多个事件共用一个）。
const startDrag = (names) => page.evaluate((names) => {
  window.__dt = new DataTransfer()
  for (const n of names) window.__dt.items.add(new File(['drop-check'], n, { type: 'text/plain' }))
}, names)
// 往指定元素派发拖拽事件，返回 defaultPrevented。
//
// 这里刻意不断言 dropEffect：实测 Chromium 对脚本构造的 DataTransfer 把 effectAllowed
// 与 dropEffect 的写入一律当无操作（两者恒为 'none'，isTrusted=false），合成事件无从验证。
// 而真正决定「这个元素收不收这次放置」的是 dragenter/dragover 有没有被 preventDefault，
// 这个合成事件测得准。dropEffect='copy' 影响的是系统拖拽时的光标与源程序行为，
// 只能真机拖一次才算数（见脚本头部说明）。
const fire = (sel, type) => page.evaluate(([sel, type]) => {
  const el = document.querySelector(sel)
  const ev = new DragEvent(type, { dataTransfer: window.__dt, bubbles: true, cancelable: true })
  el.dispatchEvent(ev)
  return ev.defaultPrevented
}, [sel, type])

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)

await page.goto(`${BASE}/files/${encodeURIComponent(DIR)}`)
await page.waitForSelector('.el-table__row', { timeout: 15000 })
await page.waitForTimeout(500)

console.log('\n【1】整页落区已移除，落区之外的拖放被吞掉')
ok('页面上不再有全屏落区遮罩', (await page.$('.drop-mask')) === null)
await startDrag([FNAME])
ok('落区之外 dragover 仍 preventDefault（不让浏览器把文件当导航打开）',
  await fire('body', 'dragover'))
await fire('body', 'drop')
await page.waitForTimeout(500)
ok('落区之外松手不上传', uploads.length === 0, `uploads=${JSON.stringify(uploads)}`)
ok('落区之外松手不会导航离开当前页', page.url().includes('/files/'), page.url())

console.log('\n【2】落区是整个抽屉体，接得住拖进来的文件')
await page.click('.toolbar button:has-text("上传")')
await page.waitForSelector('.el-drawer .dz', { state: 'visible', timeout: 10000 })
await page.waitForTimeout(400)

const box = await page.$('.el-drawer .dz')
const body = await page.$('.el-drawer .el-drawer__body')
const [dzH, bodyH] = await Promise.all([
  box.evaluate((el) => el.getBoundingClientRect().height),
  body.evaluate((el) => el.clientHeight),
])
ok('落区铺满抽屉体（队列为空时下方空白也能落）', dzH >= bodyH - 40, `dz=${dzH} body=${bodyH}`)

await startDrag([FNAME])
ok('dragenter 即被接受（不是只挡 dragover）', await fire('.el-drawer .dz', 'dragenter'))
ok('dragover 被接受', await fire('.el-drawer .dz', 'dragover'))
await page.waitForTimeout(100)
ok('框高亮为落区态', await page.isVisible('.el-drawer .pick.over'))
ok('框内文案改为「松开以上传到 …」', (await page.textContent('.el-drawer .pick'))?.includes('松开以上传到'))

// 只停 dragover、不发 dragleave/drop——等价于拖着文件离开窗口后松手
await page.waitForTimeout(800)
ok('心跳超时后高亮自行收起（不依赖 dragleave）', (await page.$('.el-drawer .pick.over')) === null)

console.log('\n【3】落下先确认：取消不传')
await startDrag([FNAME])
await fire('.el-drawer .dz', 'dragover')
await fire('.el-drawer .dz', 'drop')
await page.waitForSelector('.el-message-box', { timeout: 5000 })
const boxText = await page.textContent('.el-message-box')
ok('确认框写明文件名', boxText.includes(FNAME), boxText)
ok('确认框写明目标目录', boxText.includes(DIR), boxText)
ok('确认前一个字节都没传', uploads.length === 0, `uploads=${JSON.stringify(uploads)}`)
// 按类名定位而非文案：el-button 对中文按钮文字会自动插空格（「取消」→「取 消」），按文案匹配会漂
await page.click('.el-message-box__btns button:not(.el-button--primary)')
await page.waitForTimeout(800)
ok('取消后仍未发起上传', uploads.length === 0, `uploads=${JSON.stringify(uploads)}`)

console.log('\n【4】确认则照常上传')
await startDrag([FNAME])
await fire('.el-drawer .dz', 'dragover')
await fire('.el-drawer .dz', 'drop')
await page.waitForSelector('.el-message-box', { timeout: 5000 })
await page.click('.el-message-box__btns .el-button--primary')
await page.waitForTimeout(2500)
ok('确认后发起上传', uploads.length === 1, `uploads=${JSON.stringify(uploads)}`)
ok('上传返回 2xx', uploads[0] >= 200 && uploads[0] < 300, `status=${uploads[0]}`)
ok('队列里能看到该文件', (await page.textContent('.el-drawer'))?.includes(FNAME))

// 清理：删掉刚传上去的测试文件，不在用户的存储里留垃圾
const cleaned = await page.evaluate(async ([dir, name]) => {
  const r = await fetch('/api/fs/remove', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${localStorage.getItem('nl_token')}` },
    body: JSON.stringify({ paths: [`/${dir}/${name}`] }),
  })
  return r.status
}, [DIR, FNAME])
ok('测试文件已清理', cleaned === 200, `status=${cleaned}`)

ok('无 console/page 错误', errors.length === 0, errors.join(' | '))

console.log(`\n通过 ${passed}，失败 ${failed}`)
await browser.close()
process.exit(failed ? 1 : 0)
