// 右键菜单诊断：登录 → 视频库 → 模拟右键，把「事件有没有被接住、菜单有没有渲染出来」
// 分开报出来。两者分不清就只能靠猜，而这两种的修法完全相反。
//   node menu-probe.mjs
import { chromium } from 'playwright-core'

const BASE = process.env.NL_BASE || 'http://127.0.0.1:5243'
const PASS = process.env.NL_PASS || 'admin123'
const CHROME = process.env.NL_CHROME || 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
  const errs = []
  page.on('pageerror', (e) => errs.push(e.message))
  // warn 也要抓：fallthrough 失败时 Vue 报的是 warning（Extraneous non-emits event listeners），
  // 只盯 error 会把最关键的线索漏掉
  page.on('console', (m) => {
    if (m.type() === 'error' || m.type() === 'warning') errs.push(`[${m.type()}] ` + m.text())
  })

  await page.goto(`${BASE}/login`, { waitUntil: 'domcontentloaded', timeout: 30000 })
  await page.fill('input[placeholder="用户名"]', 'admin')
  await page.fill('input[placeholder="密码"]', PASS)
  await page.click('button:has-text("登 录"), button:has-text("登录")')
  await page.waitForURL(/library/, { timeout: 20000 })

  await page.goto(`${BASE}/library/video`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.v-card', { timeout: 20000 })

  // 1) 事件层：派发 contextmenu，看有没有人调用 preventDefault
  const synth = await page.evaluate(() => {
    const c = document.querySelector('.v-card')
    if (!c) return { found: false }
    const prevented = !c.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
    return { found: true, prevented, menuAfter: !!document.querySelector('.cm') }
  })
  console.log('派发 contextmenu：', JSON.stringify(synth))

  // 2) 真实右键：走浏览器的完整派发路径
  await page.click('.v-card', { button: 'right' })
  await page.waitForTimeout(300)
  const real = await page.evaluate(() => ({
    menu: !!document.querySelector('.cm'),
    mask: !!document.querySelector('.cm-mask'),
    items: [...document.querySelectorAll('.cm-item')].map((x) => x.textContent.trim()),
  }))
  console.log('真实右键后：', JSON.stringify(real))

  // 3) Vue 把事件处理器存在 DOM 节点的 _vei 上 —— 直接看它到底挂了哪些，
  //    比从现象反推快得多。同时看父级：v-on 若落到了别的层，会显示在那里。
  const vei = await page.evaluate(() => {
    const c = document.querySelector('.v-card')
    const dump = (el) => (el ? { tag: el.tagName + '.' + (el.className || ''), vei: Object.keys(el._vei || {}) } : null)
    return { self: dump(c), parent: dump(c?.parentElement), firstChild: dump(c?.firstElementChild) }
  })
  console.log('视频卡 _vei：', JSON.stringify(vei))

  // 3.5) 事件已经进来了，菜单却没出来 —— 嫌疑在 menu ref 是空的（`menu.value?.show()`
  //      的可选链会静默跳过）。Vue 在 DOM 上挂了 __vueParentComponent，顺着它读 setupState。
  const state = await page.evaluate(() => {
    let inst = document.querySelector('.v-card')?.__vueParentComponent
    let hops = 0
    while (inst && !inst.setupState?.openMenu && hops++ < 8) inst = inst.parent
    const s = inst?.setupState
    if (!s) return { reached: false }
    const menu = s.menu
    return {
      reached: true,
      menuIsRef: !!menu && typeof menu === 'object' && 'value' in menu,
      menuValue: String((menu && menu.value) ?? menu ?? 'null'),
      detailValue: String((s.detail && s.detail.value) ?? 'null'),
      openMenuType: typeof s.openMenu,
    }
  })
  console.log('页面 setupState：', JSON.stringify(state))

  // 4) 照片墙的 .cell 是原生 div，不牵涉组件 fallthrough —— 两边对照能分清病因
  await page.goto(`${BASE}/library/photos`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.cell, .p-card', { timeout: 20000 })
  const photo = await page.evaluate(() => {
    const c = document.querySelector('.cell') || document.querySelector('.p-card')
    if (!c) return { found: false }
    return {
      found: true,
      tag: c.tagName + '.' + (c.className || ''),
      vei: Object.keys(c._vei || {}),
      prevented: !c.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true })),
    }
  })
  console.log('照片墙：', JSON.stringify(photo))

  if (errs.length) console.log('页面报错：\n  ' + errs.join('\n  '))
  else console.log('页面无 JS 报错')
} catch (e) {
  console.error('诊断失败：', e.message)
} finally {
  await browser.close()
}
