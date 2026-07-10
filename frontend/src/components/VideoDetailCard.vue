<template>
  <!-- 视频详情二级卡片（反馈#16）：大缩略图 + 详细信息 + 立即播放 -->
  <el-dialog v-model="visible" class="vdc" width="720px" append-to-body align-center
    :show-close="false" destroy-on-close :before-close="animatedClose">
    <div v-if="video" class="vdc-body">
      <div class="vdc-art">
        <div class="vdc-art-fallback"><el-icon :size="46"><VideoCamera /></el-icon></div>
        <!-- 低清底图：列表 480 缩略图已在浏览器缓存，转场起飞瞬间就有画面；1200 高清加载后盖上 -->
        <img class="vdc-art-lo" :key="'lo:' + video.path" :src="thumbUrl(video.path, 480)"
          alt="" @error="hideImg" />
        <img :key="video.path" :src="thumbUrl(video.path, 1200)" @load="onArtLoad" @error="hideImg" />
        <button class="vdc-close" @click="animatedClose()">
          <el-icon :size="16"><Close /></el-icon>
        </button>
        <div v-if="resumePct" class="vdc-prog"><span :style="{ width: resumePct + '%' }" /></div>
      </div>
      <div class="vdc-info" :style="tintStyle">
        <h2 class="vdc-title">{{ stripExt(video.name) }}</h2>
        <div class="vdc-meta">
          <span class="vdc-badge">{{ ext }}</span>
          <span v-if="strategyText" class="vdc-badge"
            :class="{ warn: info?.strategy === 'unsupported' }">{{ strategyText }}</span>
          <span v-else-if="infoLoading" class="dim">检测格式中…</span>
          <span v-if="durationText" class="dim">时长 {{ durationText }}</span>
          <span class="dim">{{ formatSize(video.size) }}</span>
        </div>
        <div class="vdc-rows">
          <div class="vdc-row"><span class="k">文件名</span><span class="v">{{ video.name }}</span></div>
          <div class="vdc-row">
            <span class="k">所在目录</span>
            <a class="v vdc-link" @click="goDir">{{ dirPath }}</a>
          </div>
          <div class="vdc-row"><span class="k">修改时间</span><span class="v">{{ formatTime(video.modified) }}</span></div>
          <div v-if="video.played_at" class="vdc-row">
            <span class="k">上次播放</span><span class="v">{{ formatTime(video.played_at) }}</span>
          </div>
          <div v-if="resumePct" class="vdc-row">
            <span class="k">观看进度</span>
            <span class="v">看到 {{ formatDuration(progress.position) }}
              <span class="dim">/ {{ formatDuration(progress.duration) }}（{{ resumePct }}%）</span></span>
          </div>
        </div>
        <p v-if="info?.strategy === 'unsupported'" class="dim vdc-note">{{ info.message }}</p>
        <div class="vdc-ops">
          <el-button type="primary" size="large" round :icon="VideoPlay" class="vdc-play"
            @click="play(false)">{{ resumePct ? '继续观看' : '立即播放' }}</el-button>
          <el-button v-if="resumePct" size="large" round :icon="RefreshLeft"
            @click="play(true)">从头播放</el-button>
          <el-button size="large" round :icon="FolderOpened" @click="goDir">文件位置</el-button>
        </div>
      </div>
    </div>
  </el-dialog>
</template>

<script setup>
import { ref, computed, nextTick } from 'vue'
import { useRouter, onBeforeRouteLeave } from 'vue-router'
import { Close, FolderOpened, RefreshLeft, VideoCamera, VideoPlay } from '@element-plus/icons-vue'
import http from '../api/http'
import { fetchVideoInfo } from '../utils/videoInfo'
import { thumbUrl, playRoute, filesRoute, parent } from '../utils/path'
import { formatSize, formatTime, hideImg, stripExt, formatDuration } from '../utils/file'

const router = useRouter()
const visible = ref(false)
const video = ref(null)
const info = ref(null) // /video/info 结果：播放策略与时长
const infoLoading = ref(false)
const progress = ref({ position: 0, duration: 0 }) // 续播位置
const tint = ref(null) // 封面莫奈主色 {r,g,b}，取不到 = 中性玻璃
let seq = 0 // 连开多个卡片时丢弃过期响应

// resumePct 续播百分比（0 = 无进度，不显示进度条/继续观看）
const resumePct = computed(() => {
  const { position, duration } = progress.value
  if (!position || !duration) return 0
  return Math.min(100, Math.round((position / duration) * 100))
})
// 信息区玻璃染色：主色由近及远渐隐（叠在 backdrop 磨砂上，保持玻璃质感）
const tintStyle = computed(() => {
  if (!tint.value) return {}
  const { r, g, b } = tint.value
  return {
    background: `linear-gradient(180deg, rgba(${r}, ${g}, ${b}, 0.30) 0%, ` +
      `rgba(${(r * 0.55) | 0}, ${(g * 0.55) | 0}, ${(b * 0.55) | 0}, 0.14) 100%)`,
  }
})

// 封面加载完取莫奈主色：缩样到 24×24 → 粗量化分桶挑主导色（跳过近黑/近白）→
// HSL 里抬饱和度、钳亮度，得到 Material You 式基调
function onArtLoad(e) {
  const img = e.target
  if (!img.isConnected) return // :key 切换后旧 img 的迟到 load
  try {
    const S = 24
    const c = document.createElement('canvas')
    c.width = S; c.height = S
    const ctx = c.getContext('2d', { willReadFrequently: true })
    ctx.drawImage(img, 0, 0, S, S)
    const d = ctx.getImageData(0, 0, S, S).data // 同源缩略图；跨域 302 兜底会抛错走 catch
    const buckets = new Map()
    for (let i = 0; i < d.length; i += 4) {
      const r = d[i], g = d[i + 1], b = d[i + 2]
      const max = Math.max(r, g, b), min = Math.min(r, g, b)
      if ((max + min) / 2 < 24 || (max + min) / 2 > 235) continue
      const key = ((r >> 5) << 10) | ((g >> 5) << 5) | (b >> 5)
      const e2 = buckets.get(key) || { n: 0, r: 0, g: 0, b: 0, s: 0 }
      e2.n++; e2.r += r; e2.g += g; e2.b += b; e2.s += max - min
      buckets.set(key, e2)
    }
    let best = null, bestScore = -1
    for (const v of buckets.values()) {
      const score = v.n * (1 + v.s / v.n / 64) // 占比为主，饱和度加成防灰底压过主体色
      if (score > bestScore) { bestScore = score; best = v }
    }
    tint.value = best ? vibrant(best.r / best.n, best.g / best.n, best.b / best.n) : null
  } catch { tint.value = null }
}

// rgb→hsl 调整→rgb：s ≥ 0.42（莫奈鲜度）、l 钳 [0.34, 0.58]（深色 UI 上既显色又不刺眼）
function vibrant(r, g, b) {
  r /= 255; g /= 255; b /= 255
  const max = Math.max(r, g, b), min = Math.min(r, g, b)
  let h = 0
  const l = (max + min) / 2
  const dd = max - min
  let s = dd === 0 ? 0 : dd / (1 - Math.abs(2 * l - 1))
  if (dd > 0) {
    if (max === r) h = ((g - b) / dd + (g < b ? 6 : 0)) / 6
    else if (max === g) h = ((b - r) / dd + 2) / 6
    else h = ((r - g) / dd + 4) / 6
  }
  s = Math.max(s, 0.42)
  const l2 = Math.min(Math.max(l, 0.34), 0.58)
  const q = l2 < 0.5 ? l2 * (1 + s) : l2 + s - l2 * s
  const p = 2 * l2 - q
  const f = (t) => {
    t = (t + 1) % 1
    if (t < 1 / 6) return p + (q - p) * 6 * t
    if (t < 1 / 2) return q
    if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6
    return p
  }
  return {
    r: Math.round(f(h + 1 / 3) * 255),
    g: Math.round(f(h) * 255),
    b: Math.round(f(h - 1 / 3) * 255),
  }
}

const ext = computed(() => {
  const i = video.value?.name.lastIndexOf('.') ?? -1
  return i > 0 ? video.value.name.slice(i + 1).toUpperCase() : '视频'
})
const dirPath = computed(() => (video.value ? parent(video.value.path) : ''))
const strategyText = computed(() => {
  switch (info.value?.strategy) {
    case 'direct': return '原生直连'
    case 'hls': return info.value.reason === 'remux' ? '转封装播放' : '转码播放'
    case 'unsupported': return '暂不支持播放'
    default: return ''
  }
})
const durationText = computed(() => {
  const s = Math.round(info.value?.duration || 0)
  return s ? formatDuration(s) : ''
})

// ---- iOS 式 hero 转场：卡片从点击的封面处放大展开、关闭缩回原位 ----
// 全程只动 transform 与 overlay opacity（纯合成器属性，iOS WebKit 安全，见 glass.css
// 反馈#35 教训——严禁 filter 参与动画）；EP 自带的 modal-fade/dialog-fade 在 zoomIn 里
// 用内联 animation:none 接管（overlay 是 v-show 持久节点，内联样式常驻，此后每次
// 开合都由这里驱动）。动画期间 .vdc-zooming 临时停掉卡片磨砂（样式见下方全局块）。
const ZOOM_EASE = 'cubic-bezier(0.32, 0.72, 0, 1)' // iOS 卡片/Sheet 手感曲线
let origin = null    // { el, rect }：点击来源缩略图
let cardRect = null  // 弹窗落定矩形（开场量取，供开场中途关闭时反推）
let zoomTimer = 0    // 开场收尾定时器，关场开始前须取消（防半路清样式）
let closing = false

const reduced = () => window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches ?? false

// 以「顶边中点」为锚计算转场变换：缩放比取宽度比，这样卡片顶部 16:9 封面区
// 恰好与来源缩略图（.art 同为 16:9）重合，信息区在展开途中从封面下方生长出来
function originTransform(from, to) {
  const s = Math.max(from.width / to.width, 0.01)
  const dx = from.left + from.width / 2 - (to.left + to.width / 2)
  const dy = from.top - to.top
  return `translate(${dx}px, ${dy}px) scale(${s})`
}

// 来源比卡片还大（Featured 整幅横幅）时按真实矩形起飞，起始态比落定态更大、
// 四边探出屏幕，观感像从屏幕外飞入/飞出（反馈#41 二轮）。收缩成"贴在来源中心、
// 92% 卡片大小"的虚拟矩形——变成从横幅中心浮出/缩回的 iOS 弹出手感；
// 普通缩略图（恒小于卡片）不受影响，仍走严丝合缝的封面重合 morph
function normalizedOrigin(from, to) {
  if (from.width < to.width * 0.92) return from
  const w = to.width * 0.92
  const h = to.height * 0.92
  return {
    left: from.left + (from.width - w) / 2,
    top: from.top + (from.height - h) / 2,
    width: w,
    height: h,
  }
}

function zoomIn() {
  const dlg = document.querySelector('.el-dialog.vdc')
  const ov = dlg?.closest('.el-overlay')
  const ovd = dlg?.closest('.el-overlay-dialog')
  if (!dlg || !ov) return
  ov.style.animation = 'none'
  if (ovd) ovd.style.animation = 'none'
  ov.style.transition = 'none'
  ov.style.opacity = '0'
  const from = origin?.rect
  if (!from || reduced()) { // 无来源矩形 / 系统偏好减少动态：退化为整层快速淡入
    void ov.offsetWidth
    ov.style.transition = 'opacity 0.2s ease'
    ov.style.opacity = ''
    zoomTimer = setTimeout(() => { ov.style.transition = '' }, 240)
    return
  }
  if (ovd) ovd.style.overflow = 'hidden' // 起飞姿态可能探出视口底部，别闪滚动条
  cardRect = dlg.getBoundingClientRect()
  dlg.classList.add('vdc-zooming')
  dlg.style.transformOrigin = '50% 0'
  dlg.style.transition = 'none'
  dlg.style.transform = originTransform(normalizedOrigin(from, cardRect), cardRect)
  void ov.offsetWidth // 起始态强制落地，随后的改动才走 transition
  ov.style.transition = 'opacity 0.22s ease'
  ov.style.opacity = ''
  dlg.style.transition = `transform 0.42s ${ZOOM_EASE}`
  dlg.style.transform = ''
  zoomTimer = setTimeout(() => {
    dlg.classList.remove('vdc-zooming')
    dlg.style.transition = ''; dlg.style.transform = ''; dlg.style.transformOrigin = ''
    ov.style.transition = ''
    if (ovd) ovd.style.overflow = ''
  }, 480)
}

// 关闭转场（el-dialog before-close：ESC/点遮罩，及卡片右上关闭钮共用）：
// 卡片缩回来源缩略图处、遮罩磨砂稍滞后淡出，结束才真正关弹窗
function animatedClose(done = () => { visible.value = false }) {
  if (closing) return
  const dlg = document.querySelector('.el-dialog.vdc')
  const ov = dlg?.closest('.el-overlay')
  const ovd = dlg?.closest('.el-overlay-dialog')
  const to = origin?.el?.isConnected ? origin.el.getBoundingClientRect() : origin?.rect
  if (!dlg || !ov || !to || reduced()) { done(); return }
  // 开场动画进行中被关：布局矩形取开场量好的 cardRect（此刻 gBCR 含中途 transform 不可用）
  const from = dlg.style.transform ? cardRect : dlg.getBoundingClientRect()
  if (!from) { done(); return }
  closing = true
  clearTimeout(zoomTimer)
  if (ovd) ovd.style.overflow = 'hidden'
  dlg.classList.add('vdc-zooming')
  dlg.style.transformOrigin = '50% 0'
  dlg.style.transition = `transform 0.34s ${ZOOM_EASE}, background 0.2s ease`
  dlg.style.transform = originTransform(normalizedOrigin(to, from), from)
  ov.style.transition = 'opacity 0.24s ease 0.1s'
  ov.style.opacity = '0'
  setTimeout(() => {
    ov.style.transition = '' // 先清 transition 再 done()：EP 的 leave 立即完成，节点马上卸载
    done()
    dlg.classList.remove('vdc-zooming')
    dlg.style.transition = ''; dlg.style.transform = ''; dlg.style.transformOrigin = ''
    if (ovd) ovd.style.overflow = ''
    closing = false
    // ov.opacity 留在 0（清掉会闪回不透明），下次 zoomIn 以 0 起步淡入
  }, 360)
}

function open(v, originEl = null) {
  clearTimeout(zoomTimer)
  closing = false
  cardRect = null
  origin = originEl ? { el: originEl, rect: originEl.getBoundingClientRect() } : null
  video.value = v
  visible.value = true
  info.value = null
  tint.value = null
  infoLoading.value = true
  // 列表若已带 position/duration（最近播放货架/进度视图）先用，避免闪烁；再拉权威值
  progress.value = { position: v.position || 0, duration: v.duration || 0 }
  const g = ++seq
  // 与播放页共用同一次探测（见 utils/videoInfo）：用户「检测格式中」时点播放，
  // 播放页复用这同一次探测，不再另探一遍。
  fetchVideoInfo(v.path)
    .then((d) => { if (g === seq) info.value = d })
    .catch(() => {})
    .finally(() => { if (g === seq) infoLoading.value = false })
  http.get('/media/progress', { params: { path: v.path } })
    .then((d) => { if (g === seq) progress.value = { position: d?.position || 0, duration: d?.duration || 0 } })
    .catch(() => {})
  nextTick(zoomIn) // 弹窗 DOM 就绪后、首帧上屏前摆好起飞姿态（nextTick 在 paint 之前跑完）
}
defineExpose({ open })

// 宿主列表页被 keep-alive 缓存：弹窗 teleport 在 body 上，不随组件树隐藏。
// 用离开守卫（在 keep-alive 冻结组件前触发，此时仍可正常刷新 teleport DOM）关掉，
// 覆盖浏览器后退等所有导航——onDeactivated 触发太晚，关不掉已 teleport 的遮罩。
onBeforeRouteLeave(() => { visible.value = false })

function play(restart) {
  visible.value = false
  router.push(restart
    ? { path: playRoute(video.value.path), query: { restart: '1' } }
    : playRoute(video.value.path))
}
function goDir() {
  visible.value = false
  router.push(filesRoute(dirPath.value))
}
</script>

<!-- 弹窗壳（append-to-body 渲染在组件外，须全局样式）：
     液态玻璃——半透明底，磨砂 backdrop-filter 由 glass.css 全局 .el-dialog 规则提供 -->
<style>
.el-dialog.vdc {
  max-width: 94vw;
  padding: 0;
  border-radius: 18px;
  overflow: hidden;
  background: var(--glass-bg, rgba(255, 255, 255, 0.07));
  border: 1px solid var(--glass-border, rgba(255, 255, 255, 0.14));
  box-shadow: 0 30px 80px rgba(0, 0, 0, 0.65);
}
.el-dialog.vdc::before { /* 玻璃顶部高光线（同 .glass 签名） */
  content: '';
  position: absolute; z-index: 3;
  left: 8%; right: 8%; top: 0; height: 1px;
  background: linear-gradient(90deg, transparent, rgba(255, 255, 255, 0.4), transparent);
  pointer-events: none;
}
@supports not (backdrop-filter: blur(1px)) {
  .el-dialog.vdc { background: rgba(20, 20, 30, 0.92); }
}
.el-dialog.vdc .el-dialog__header { display: none; }
.el-dialog.vdc .el-dialog__body { padding: 0; color: var(--text-main, #e8eaf2); }

/* iOS 式 hero 转场态：动画期间临时停掉卡片及内部控件的磨砂——
   iOS WebKit 对 backdrop-filter 层做缩放动画时采样区域不随 transform 走，会错位闪烁。
   底色换近实底顶替玻璃，落定移除类后 background 过渡回半透明、磨砂恢复，肉眼无跳变 */
.el-dialog.vdc { transition: background 0.25s ease; }
.el-dialog.vdc.vdc-zooming {
  will-change: transform;
  backdrop-filter: none !important;
  -webkit-backdrop-filter: none !important;
  background: rgba(17, 19, 28, 0.97);
}
.el-dialog.vdc.vdc-zooming .vdc-close,
.el-dialog.vdc.vdc-zooming .el-button {
  backdrop-filter: none !important;
  -webkit-backdrop-filter: none !important;
}
</style>

<style scoped>
.vdc-art {
  position: relative;
  aspect-ratio: 16 / 9;
  background: #101018;
}
.vdc-art img {
  position: relative; z-index: 1;
  width: 100%; height: 100%;
  object-fit: cover; display: block;
}
/* 低清底图垫在高清图下（z-index 0），高清加载完自然盖住 */
.vdc-art img.vdc-art-lo {
  position: absolute; inset: 0; z-index: 0;
}
.vdc-art-fallback {
  position: absolute; inset: 0;
  display: flex; align-items: center; justify-content: center;
  color: rgba(255, 255, 255, 0.22);
}
.vdc-close {
  position: absolute; z-index: 2; top: 14px; right: 14px;
  display: flex; align-items: center; justify-content: center;
  width: 32px; height: 32px; border-radius: 50%;
  border: none; cursor: pointer;
  background: rgba(10, 12, 18, 0.55); color: #fff;
  backdrop-filter: blur(6px);
  transition: background 0.2s;
}
.vdc-close:hover { background: rgba(10, 12, 18, 0.85); }
/* 续播进度条：贴大缩略图底沿 */
.vdc-prog {
  position: absolute; z-index: 2; left: 0; right: 0; bottom: 0;
  height: 5px; background: rgba(0, 0, 0, 0.5);
}
.vdc-prog span {
  display: block; height: 100%;
  background: var(--accent, #7aa2ff);
  border-radius: 0 2.5px 2.5px 0;
}

.vdc-info { padding: 20px 26px 26px; }
.vdc-title { margin: 0 0 10px; font-size: 24px; font-weight: 800; line-height: 1.25; }
.vdc-meta {
  display: flex; align-items: center; flex-wrap: wrap; gap: 8px 12px;
  font-size: 13px; margin-bottom: 16px;
}
.vdc-badge {
  padding: 2px 10px; border-radius: 999px;
  background: rgba(255, 255, 255, 0.1);
  font-size: 12px; font-weight: 600; letter-spacing: 0.5px;
}
.vdc-badge.warn { background: rgba(230, 162, 60, 0.18); color: #e6a23c; }

.vdc-rows { display: flex; flex-direction: column; gap: 7px; font-size: 13.5px; }
.vdc-row { display: flex; gap: 12px; }
.vdc-row .k { flex: 0 0 62px; color: var(--text-dim, #9aa0b4); }
.vdc-row .v { word-break: break-all; min-width: 0; }
.vdc-link { cursor: pointer; color: #7aa2ff; }
.vdc-link:hover { text-decoration: underline; }

.vdc-note { margin: 12px 0 0; font-size: 12.5px; }
.vdc-ops { margin-top: 20px; display: flex; gap: 4px; }
.vdc-play { box-shadow: 0 6px 24px rgba(122, 162, 255, 0.35); }

/* ---- 移动端 ---- */
@media (max-width: 768px) {
  .vdc-info { padding: 16px 16px 18px; }
  .vdc-title { font-size: 19px; margin-bottom: 8px; }
  .vdc-meta { margin-bottom: 12px; }
  .vdc-ops { margin-top: 16px; }
  .vdc-ops .el-button { flex: 1; padding-left: 8px; padding-right: 8px; }
}
</style>
