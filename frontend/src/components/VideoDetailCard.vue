<template>
  <!-- 视频详情二级卡片（反馈#16）：大缩略图 + 详细信息 + 立即播放 -->
  <el-dialog v-model="visible" class="vdc" width="720px" append-to-body align-center
    :show-close="false" destroy-on-close>
    <div v-if="video" class="vdc-body">
      <div class="vdc-art">
        <div class="vdc-art-fallback"><el-icon :size="46"><VideoCamera /></el-icon></div>
        <img :key="video.path" :src="thumbUrl(video.path, 1200)" @load="onArtLoad" @error="hideImg" />
        <button class="vdc-close" @click="visible = false">
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
import { ref, computed } from 'vue'
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

function open(v) {
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
