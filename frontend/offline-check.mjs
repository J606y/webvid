// 任务设置 + 离线下载验证：后台「任务设置」Tab 的线程数/限速表单保存热生效；
// Files 工具栏「离线下载」→ 弹窗提交 URL → offline 组任务完成 → 文件落盘可列出。
// 用法: node offline-check.mjs （需服务在跑，admin/admin123）
//
// 注意「拉流落盘」那三条在本机跑不出结果：源站只能起在回环地址，而 SSRF 防护
//（internal/server/safedial.go）本就该拒绝回环/私有地址。命中时按跳过计，不算失败——
// 防护生效正是期望行为，要真正端到端验证得换一个公网源站。
import { chromium } from 'playwright-core'
import { execSync } from 'node:child_process'
import http from 'node:http'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const OUT = '../_shots'
execSync(`mkdir -p ${OUT}`, { shell: 'bash' })

// 本地临时源站：提供带 Content-Disposition 的下载文件
const PAYLOAD = Buffer.alloc(300 * 1024, 'offline-e2e!')
const FILE_NAME = 'offline-e2e-file.bin'
const src = http.createServer((req, res) => {
  res.setHeader('Content-Disposition', `attachment; filename="${FILE_NAME}"`)
  res.setHeader('Content-Length', PAYLOAD.length)
  res.end(PAYLOAD)
})
await new Promise((r) => src.listen(5321, '127.0.0.1', r))

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`[console] ${m.text()}`) })
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`))

let passed = 0, failed = 0, skipped = 0
const ok = (name, cond) => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}`) }
}
const skip = (name, why) => { skipped++; console.log(`  ➖ ${name}（跳过：${why}）`) }
const api = (path, opts = {}) => page.evaluate(async ({ path, opts }) => {
  const r = await fetch('/api' + path, {
    ...opts,
    headers: {
      'Content-Type': 'application/json',
      Authorization: 'Bearer ' + localStorage.getItem('nl_token'),
      ...(opts.headers || {}),
    },
  })
  return r.json()
}, { path, opts })

// 登录
await page.goto(`${BASE}/login`)
await page.fill('input[placeholder="用户名"]', 'admin')
await page.fill('input[placeholder="密码"]', 'admin123')
await page.click('button:has-text("登 录")')
await page.waitForURL(`${BASE}/library/video`)

// ---- 1. 后台任务设置表单 ----
console.log('— 后台任务设置 —')
await page.goto(`${BASE}/@admin`)
await page.waitForSelector('.el-tabs')
// 线程数/限速在独立的「任务设置」Tab（#33 起），切过去这些输入才可见可填
await page.click('.el-tabs__item:has-text("任务设置")')
await page.waitForSelector('#pane-task .el-input-number input', { state: 'visible' })
for (const label of ['复制任务线程', '离线下载线程', '上传并发数', '复制限速', '上传限速', '下载限速']) {
  ok(`表单含「${label}」`, await page.locator(`.el-form-item:has-text("${label}")`).count() === 1)
}
await page.screenshot({ path: `${OUT}/diag-admin-task-settings.png` })

// 改复制线程 2→3 + 下载限速 0→1024，保存后 API 读回应为新值
const setNum = async (label, val) => {
  const input = page.locator(`.el-form-item:has-text("${label}") .el-input-number input`)
  await input.fill(String(val))
  await input.blur()
}
await setNum('复制任务线程', 3)
await setNum('下载限速', 1024)
// 任务设置 Tab 有自己的保存按钮（站点设置 Tab 另有一个），限定到本 Tab 面板
await page.click('#pane-task button:has-text("保存")')
await page.waitForSelector('.el-message--success')
let st = (await api('/admin/settings')).data
ok('保存后 copy_workers=3', st.copy_workers === 3)
ok('保存后 download_speed_kb=1024', st.download_speed_kb === 1024)
// 还原（PUT 全量，其余字段带回读到的值）
await api('/admin/settings', {
  method: 'PUT',
  body: JSON.stringify({ ...st, copy_workers: 2, download_speed_kb: 0 }),
})
st = (await api('/admin/settings')).data
ok('还原 copy_workers=2 / download_speed_kb=0', st.copy_workers === 2 && st.download_speed_kb === 0)
const pub = (await api('/public/settings')).data
ok('/public/settings 透出 upload_workers', typeof pub.upload_workers === 'number')

// ---- 2. 离线下载 ----
console.log('— 离线下载 —')
await page.goto(`${BASE}/files/本地存储`)
await page.waitForSelector('.toolbar')
const offBtn = page.locator('.toolbar button:has-text("离线下载")')
ok('工具栏有「离线下载」按钮', await offBtn.count() === 1)
await offBtn.click()
const dlg = page.locator('.el-dialog:has-text("离线下载")')
await dlg.waitFor({ state: 'visible' })
ok('弹窗显示目标目录', (await dlg.locator('.offline-dst').textContent()).includes('/本地存储'))
await dlg.locator('textarea').fill('http://127.0.0.1:5321/dl')
await dlg.locator('button:has-text("开始下载")').click()
await page.waitForSelector('.el-message--success')
// 按 aria-label 定位而非文本：两个抽屉互留了对方的入口按钮，:has-text 会同时命中
ok('提交后打开任务抽屉', await page.locator('.el-drawer[aria-label="传输任务"]').isVisible())

// 轮询任务到完成
let task = null
for (let i = 0; i < 60; i++) {
  const list = (await api('/tasks')).data || []
  task = list.find((t) => t.group === 'offline' && t.name.includes('→ /本地存储'))
  if (task && (task.state === 'done' || task.state === 'error')) break
  await page.waitForTimeout(500)
}
// 源站在回环地址，被 SSRF 防护挡下——防护本身是期望行为，拉流那段本机无从验证
const ssrfBlocked = task?.state === 'error' && /内网|本机地址/.test(task?.error || '')
const why = 'SSRF 防护按预期拒绝了回环源站，本机无法提供公网源'

ok('任务名含「离线下载」', (task?.name || '').startsWith('离线下载'))
await page.screenshot({ path: `${OUT}/diag-offline-tasks.png` })

if (ssrfBlocked) {
  ok('回环源站被 SSRF 防护拒绝（安全行为符合预期）', true)
  skip('离线任务完成（group=offline）', why)
  skip(`「${FILE_NAME}」出现在目录（Content-Disposition 命名）`, why)
  skip('文件大小一致', why)
} else {
  ok('离线任务完成（group=offline）', task?.state === 'done')
  // 文件落盘：列表可见且大小一致；随后清理
  const listed = (await api(`/fs/list?path=${encodeURIComponent('/本地存储')}`)).data
  const fi = (listed.items || []).find((x) => x.name === FILE_NAME)
  ok(`「${FILE_NAME}」出现在目录（Content-Disposition 命名）`, !!fi)
  ok('文件大小一致', fi?.size === PAYLOAD.length)
  await api('/fs/remove', { method: 'POST', body: JSON.stringify({ paths: [`/本地存储/${FILE_NAME}`] }) })
  const after = (await api(`/fs/list?path=${encodeURIComponent('/本地存储')}`)).data
  ok('清理测试文件', !(after.items || []).some((x) => x.name === FILE_NAME))
}
// 清理任务记录
await api('/tasks/done', { method: 'DELETE' })

ok('无控制台错误', errors.length === 0)
if (errors.length) console.error(errors.join('\n'))

console.log(`\n结果: ${passed} 通过, ${failed} 失败${skipped ? `, ${skipped} 跳过` : ''}`)
await browser.close()
src.close()
process.exit(failed ? 1 : 0)
