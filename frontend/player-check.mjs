// 播放器回归（反馈#51）：
// 1) 暂停态大图标：svg 三角有非零渲染尺寸且带显式 width/height 属性（只有 viewBox 的
//    svg 在 iOS WebKit 百分比链里解析成 0 高、三角消失）+ .art-icon 无 filter（iOS 雷区）
// 2) 移动端（iPhone UA 触发 .art-mobile）底栏控件：七颗全落进播放器不越界、胶囊收一号、
//    ArtPlayer 的 .art-mobile 负边距被压回；桌面尺度不回归
// 用法: node player-check.mjs （需服务在跑，NL_BASE 可指隔离实例）
import { chromium } from 'playwright-core'
import { execSync } from 'node:child_process'

const BASE = process.env.NL_BASE || 'http://localhost:5299'
const CHROME = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
const OUT = '../_shots'
execSync(`mkdir -p ${OUT}`, { shell: 'bash' })

const browser = await chromium.launch({ executablePath: CHROME, headless: true })
let passed = 0, failed = 0
const ok = (name, cond) => {
  if (cond) { passed++; console.log(`  ✅ ${name}`) }
  else { failed++; console.error(`  ❌ ${name}`) }
}

async function toPlay(page) {
  await page.goto(`${BASE}/login`)
  await page.fill('input[placeholder="用户名"]', 'admin')
  await page.fill('input[placeholder="密码"]', 'admin123')
  await page.click('button:has-text("登 录"), button:has-text("登录")')
  await page.waitForURL(/library/, { timeout: 10000 })
  await page.goto(`${BASE}/library/video?dir=${encodeURIComponent('/本地存储/电影')}`)
  await page.waitForSelector('.v-grid .v-card', { timeout: 15000 })
  await page.click('.v-grid .v-card >> nth=0')
  await page.waitForSelector('.el-dialog.vdc .vdc-play', { timeout: 8000 })
  await page.click('.el-dialog.vdc .vdc-play')
  await page.waitForURL(/\/play\//, { timeout: 8000 })
  await page.waitForSelector('.art-video-player', { timeout: 15000 })
  // 暂停让 .art-state 浮现
  await page.evaluate(() => document.querySelector('.art-video-player video').pause())
  await page.waitForTimeout(600)
}

// ---- 移动端：iPhone UA（触发 .art-mobile）+ 390×844 ----
{
  const ctx = await browser.newContext({
    viewport: { width: 390, height: 844 },
    isMobile: true, hasTouch: true, deviceScaleFactor: 2,
    userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1',
  })
  const page = await ctx.newPage()
  await toPlay(page)

  ok('art-mobile 已触发（UA 生效，环境同真机）', await page.evaluate(() =>
    document.querySelector('.art-video-player').classList.contains('art-mobile')))

  const st = await page.evaluate(() => {
    const icon = document.querySelector('.art-state .art-icon')
    const svg = icon?.querySelector('svg')
    const ir = icon?.getBoundingClientRect(), sr = svg?.getBoundingClientRect()
    return {
      iconW: ir?.width, iconH: ir?.height, svgW: sr?.width, svgH: sr?.height,
      svgAttrW: svg?.getAttribute('width'), svgAttrH: svg?.getAttribute('height'),
      filter: icon ? getComputedStyle(icon).filter : '',
    }
  })
  ok(`state svg 渲染尺寸非零（${st.svgW}×${st.svgH}）`, st.svgW > 20 && st.svgH > 20)
  ok(`state svg 带显式 width/height 属性（${st.svgAttrW}×${st.svgAttrH}）`, st.svgAttrW === '32' && st.svgAttrH === '32')
  ok('state .art-icon 已无 filter（iOS 雷区清除）', st.filter === 'none')

  const bar = await page.evaluate(() => {
    const p = document.querySelector('.art-video-player')
    const controls = p.querySelector('.art-controls')
    const left = p.querySelector('.art-controls-left')
    const right = p.querySelector('.art-controls-right')
    const caps = [...controls.querySelectorAll('.art-control')].filter((c) => c.offsetParent)
    const pr = p.getBoundingClientRect()
    const overflow = caps.some((c) => {
      const r = c.getBoundingClientRect()
      return r.left < pr.left - 0.5 || r.right > pr.right + 0.5
    })
    const gap = right.getBoundingClientRect().left - left.getBoundingClientRect().right
    const one = caps[0].getBoundingClientRect()
    return {
      count: caps.length, overflow, groupGap: Math.round(gap),
      capH: Math.round(one.height),
      leftMargin: getComputedStyle(left).marginLeft,
      iconSize: getComputedStyle(p).getPropertyValue('--art-control-icon-size').trim(),
      timeFont: getComputedStyle(p.querySelector('.art-control-time')).fontSize,
    }
  })
  ok(`控件 ${bar.count} 颗全部落在播放器内不越界`, !bar.overflow)
  ok(`左右分组间有呼吸空间（间距 ${bar.groupGap}px ≥ 12）`, bar.groupGap >= 12)
  ok(`胶囊收一号（高 ${bar.capH}px ≤ 36）`, bar.capH <= 36)
  ok(`图标收到 20px（现 ${bar.iconSize}）`, bar.iconSize === '20px')
  ok(`时间字号 12px（现 ${bar.timeFont}）`, bar.timeFont === '12px')
  ok(`.art-mobile 负边距已压回（margin-left=${bar.leftMargin}）`, bar.leftMargin === '0px')

  // 让控件栏可见状态下截图（点一下画面唤起控制层）
  await page.evaluate(() => document.querySelector('.art-video-player').classList.add('art-hover'))
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${OUT}/player-fix-mobile.png` })
  console.log('shot: player-fix-mobile')
  await ctx.close()
}

// ---- 桌面回归：1280×800 无 mobile UA ----
{
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  const page = await ctx.newPage()
  await toPlay(page)
  const st = await page.evaluate(() => {
    const svg = document.querySelector('.art-state .art-icon svg')
    const r = svg?.getBoundingClientRect()
    return { w: r?.width, h: r?.height }
  })
  ok(`桌面 state svg 渲染尺寸不回归（${st.w}×${st.h}）`, st.w > 20 && st.h > 20)
  const cap = await page.evaluate(() => {
    const c = document.querySelector('.art-controls .art-control')
    const s = getComputedStyle(c)
    return { minW: s.minWidth, minH: s.minHeight }
  })
  ok(`桌面胶囊尺度不变（min ${cap.minW}×${cap.minH}）`, cap.minW === '42px' && cap.minH === '38px')
  await page.evaluate(() => document.querySelector('.art-video-player').classList.add('art-hover'))
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${OUT}/player-fix-desktop.png` })
  console.log('shot: player-fix-desktop')
  await ctx.close()
}

await browser.close()
console.log(`\n${failed === 0 ? '✅' : '❌'} ${passed} passed, ${failed} failed`)
process.exit(failed === 0 ? 0 : 1)
