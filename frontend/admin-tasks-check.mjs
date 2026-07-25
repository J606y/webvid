// 后台「传输任务」：比顶栏抽屉多一层——能看到文件夹内的逐文件状态
//（等待/传输中/已完成/已跳过/失败 + 搜索 + 筛选 + 分页）。
// 跑真实跨存储转存：第一轮看在传，第二轮全部命中断点续传看「已跳过」。
// 用法: NL_BASE=http://localhost:5299 NL_SRC=C:\...\a NL_DST=C:\...\b node admin-tasks-check.mjs
//   NL_SRC 需含一个名为「剧集」的文件夹（内含若干文件），NL_DST 为空目标盘目录。
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const SHOTS = process.env.TEMP || '.'
const SRC = process.env.NL_SRC
const DST = process.env.NL_DST
const SRC_MOUNT = '/转存源'
const DST_MOUNT = '/转存目标'
const FOLDER = '剧集'

if (!SRC || !DST) {
  console.error('需要 NL_SRC / NL_DST（存储根目录的 Windows 路径）')
  process.exit(2)
}

// ---- 准备：登录拿 token，确保两个本地存储在位，清空目标，触发一轮转存 ----
const api = async (path, init = {}, token) => {
  const r = await fetch(`${BASE}/api${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init.headers || {}),
    },
  })
  const body = await r.json()
  if (body.code !== 200) throw new Error(`${path} -> ${body.code} ${body.message}`)
  return body.data
}

const { token } = await api('/auth/login', {
  method: 'POST',
  body: JSON.stringify({ username: 'admin', password: 'admin123' }),
})
const storages = await api('/admin/storages', {}, token)
for (const [mount, root] of [[SRC_MOUNT, SRC], [DST_MOUNT, DST]]) {
  if (storages.some((s) => s.mount_path === mount)) continue
  await api('/admin/storages', {
    method: 'POST',
    body: JSON.stringify({ mount_path: mount, driver: 'local', config: { root_path: root }, enabled: true }),
  }, token)
}
// 目标残留会让第一轮就全跳过，测不到「在传」；先清干净
await api('/fs/remove', {
  method: 'POST', body: JSON.stringify({ paths: [`${DST_MOUNT}/${FOLDER}`] }),
}, token).catch(() => {})
// 旧任务记录会顶在列表最前，断言就落到别的行上；终态的先清掉
for (const t of await api('/tasks', {}, token)) {
  if (['done', 'error', 'canceled'].includes(t.state)) {
    await api(`/tasks/${t.id}/remove`, { method: 'POST' }, token).catch(() => {})
  }
}
// 限速让转存慢到能观察：41 个文件约 10MB，512KB/s 跑约 20 秒
const settings = await api('/admin/settings', {}, token)
const restoreSpeed = settings.copy_speed_kb
await api('/admin/settings', {
  method: 'PUT', body: JSON.stringify({ ...settings, copy_speed_kb: 512 }),
}, token)
const copy = () => api('/fs/copy', {
  method: 'POST', body: JSON.stringify({ paths: [`${SRC_MOUNT}/${FOLDER}`], dst_dir: DST_MOUNT }),
}, token)
await copy()

// ---- 界面 ----
const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

let passed = 0, failed = 0
const ok = (name, cond, extra = '') => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}${extra ? ` (${extra})` : ''}`) }
}

await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)
await page.goto(`${BASE}/@admin`)
await page.click('.el-tabs__item:has-text("传输任务")')

// el-table 会渲染用于测量的隐藏行，一律限定在可见的任务表内取
const rows = page.locator('.task-table .el-table__row:visible')
const row = rows.first()
const drawer = page.locator('.el-drawer')
const viewFiles = () => page.locator('.task-table .el-table__row:visible button:has-text("查看文件")').first()

console.log('1. 任务表格：转存任务在列，文件数在列')
await row.waitFor({ timeout: 10000 })
await viewFiles().waitFor({ timeout: 10000 })
const rowText = () => row.innerText().then((s) => s.replace(/\s+/g, ' '))
ok('任务行出现且带任务名', (await rowText()).includes(FOLDER), await rowText())
ok('发起人列给出用户名', (await rowText()).includes('admin'), await rowText())
ok('文件列给出「已完成/总数」', /\d+ \/ \d+/.test(await rowText()), await rowText())
ok('进度列给出已传/总量', /[\d.]+ (B|KB|MB|GB) \/ [\d.]+ (B|KB|MB|GB)/.test(await rowText()),
  await rowText())
await page.screenshot({ path: `${SHOTS}/admin-tasks-list.png`, clip: { x: 0, y: 120, width: 1400, height: 420 } })

console.log('2. 文件清单抽屉：逐文件状态')
await viewFiles().click()
await drawer.waitFor({ timeout: 8000 })
await drawer.locator('.f-table .el-table__row:visible').first().waitFor({ timeout: 10000 })
const counts = () => drawer.locator('.f-counts').innerText()
ok('给出清单总数', /共 \d+ 个文件/.test(await counts()), await counts())
const bodyText = () => drawer.innerText().then((s) => s.replace(/\s+/g, ' '))
ok('列出子目录层级的路径', (await bodyText()).includes('第一季/'), (await bodyText()).slice(0, 200))
// 限速 512KB/s + 4 并发：这时必定有在传的，也必定有还没轮到的
ok('同时看得到「传输中」与「等待」',
  /传输中 \d+/.test(await counts()) && /等待 \d+/.test(await counts()), await counts())
await page.waitForTimeout(500) // 等抽屉滑入动画结束，否则截到半截
await drawer.screenshot({ path: `${SHOTS}/admin-tasks-files.png` })

console.log('3. 搜索与状态筛选')
const totalOf = async () => Number((await counts()).match(/共 (\d+) 个文件/)[1])
const all = await totalOf()
const rowsCount = () => drawer.locator('.f-table .el-table__row:visible').count()
await drawer.locator('.f-search input').fill('花絮')
await page.waitForTimeout(800)
const searched = await rowsCount()
ok('搜索按路径过滤', searched > 0 && searched < all, `命中 ${searched} / 共 ${all}`)
ok('搜索不影响那排全量计数', (await totalOf()) === all, await counts())
await drawer.locator('.f-search input').fill('不存在的文件名xyz')
await page.waitForTimeout(800)
ok('搜不到时给出空态提示', (await bodyText()).includes('没有符合条件的文件'))
await drawer.locator('.f-search input').fill('')
await page.waitForTimeout(800)

await drawer.locator('.f-state').click()
await page.locator('.el-select-dropdown__item:has-text("等待")').last().click()
await page.waitForTimeout(800)
const pendingRows = await drawer.locator('.f-table .el-table__row:visible').allInnerTexts()
ok('按「等待」筛选后只剩等待项',
  pendingRows.length > 0 && pendingRows.every((t) => t.includes('等待')),
  pendingRows.slice(0, 3).join(' | '))

console.log('4. 跑完后：全部完成')
await page.waitForFunction(() => {
  const t = document.querySelector('.f-counts')?.innerText || ''
  const m = t.match(/共 (\d+) 个文件.*已完成 (\d+)/)
  return m && m[1] === m[2]
}, { timeout: 120000 })
ok('清单全部转为「已完成」', true)
// 焦点在筛选控件里时 Escape 未必冒泡到抽屉，点关闭按钮更稳
await drawer.locator('.el-drawer__close-btn').click()
await drawer.waitFor({ state: 'hidden', timeout: 8000 })
// 后面只看跳过，不需要再压速度；把限速还回去
await api('/admin/settings', {
  method: 'PUT', body: JSON.stringify({ ...settings, copy_speed_kb: restoreSpeed }),
}, token)

console.log('5. 再转存一轮：断点续传全部「已跳过」')
const before = await rows.count()
await copy()
for (let i = 0; i < 30 && (await rows.count()) <= before; i++) await page.waitForTimeout(500)
ok('新一轮任务出现在列表最前', (await rows.count()) > before)
await viewFiles().click()
await drawer.waitFor({ timeout: 8000 })
await page.waitForFunction(() => {
  const t = document.querySelector('.f-counts')?.innerText || ''
  const m = t.match(/共 (\d+) 个文件.*已跳过 (\d+)/)
  return m && m[1] === m[2]
}, { timeout: 60000 })
ok('全部命中断点续传，记为「已跳过」', true, await counts())
ok('清单里能看到「已跳过」标记', (await bodyText()).includes('已跳过'))
await page.waitForTimeout(500)
await drawer.screenshot({ path: `${SHOTS}/admin-tasks-skipped.png` })

await browser.close()
console.log('\n==== 控制台错误 ====')
if (errors.length === 0) console.log('无错误 ✔')
else errors.forEach((e) => console.log(e))
console.log(`截图: ${SHOTS}/admin-tasks-{list,files,skipped}.png`)
console.log(`${failed === 0 ? '🎉' : '⚠️'} 断言 ${passed}/${passed + failed}`)
process.exit(failed === 0 && errors.length === 0 ? 0 : 1)
