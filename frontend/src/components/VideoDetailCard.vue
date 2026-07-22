<template>
  <!-- 视频详情二级卡片（反馈#16）：大缩略图 + 详细信息 + 立即播放 -->
  <el-dialog v-model="visible" class="vdc" width="720px" append-to-body align-center
    :show-close="false" destroy-on-close :before-close="animatedClose">
    <div v-if="video" class="vdc-body">
      <div class="vdc-art">
        <div class="vdc-art-fallback"><el-icon :size="46"><VideoCamera /></el-icon></div>
        <!-- 低清底图：列表 480 缩略图已在浏览器缓存，转场起飞瞬间就有画面；1200 高清加载后盖上。
             莫奈取色也挂在低清图上（24×24 缩样对源分辨率不敏感）：只等高清图要 ~1s 才见色
            （反馈#46），低清命中缓存开卡即着色，高清到货后再精修一次 -->
        <img class="vdc-art-lo" :key="'lo:' + video.path" :src="thumbUrl(video.path, 480)"
          alt="" @load="onArtLoad" @error="hideImg" />
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
import { ref, computed } from 'vue'
import { useRouter, onBeforeRouteLeave } from 'vue-router'
import { Close, FolderOpened, RefreshLeft, VideoCamera, VideoPlay } from '@element-plus/icons-vue'
import { api } from '../utils/api'
import { fetchVideoInfo } from '../utils/videoInfo'
import { useHeroDialog } from '../utils/heroDialog'
import { extractVibrant } from '../utils/monet'
import { thumbUrl, playRoute, filesRoute, parent } from '../utils/path'
import { formatSize, formatTime, hideImg, stripExt, formatDuration, progressPct } from '../utils/file'

const router = useRouter()
const visible = ref(false)
const video = ref(null)
const info = ref(null) // /video/info 结果：播放策略与时长
const infoLoading = ref(false)
const progress = ref({ position: 0, duration: 0 }) // 续播位置
const tint = ref(null) // 封面莫奈主色 {r,g,b}，取不到 = 中性玻璃
let seq = 0 // 连开多个卡片时丢弃过期响应

// resumePct 续播百分比（0 = 无进度，不显示进度条/继续观看）
const resumePct = computed(() => progressPct(progress.value.position, progress.value.duration))
// 信息区玻璃染色：主色由近及远渐隐（叠在 backdrop 磨砂上，保持玻璃质感）
const tintStyle = computed(() => {
  if (!tint.value) return {}
  const { r, g, b } = tint.value
  return {
    background: `linear-gradient(180deg, rgba(${r}, ${g}, ${b}, 0.30) 0%, ` +
      `rgba(${(r * 0.55) | 0}, ${(g * 0.55) | 0}, ${(b * 0.55) | 0}, 0.14) 100%)`,
  }
})

// 封面加载完取莫奈主色（算法见 utils/monet）；取不到落 null = 中性玻璃。
// :key 切换后旧 img 的迟到 load 要忽略（已脱离文档）。
function onArtLoad(e) {
  const img = e.target
  if (!img.isConnected) return
  tint.value = extractVibrant(img)
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

// ---- iOS 式 hero 转场：卡片从点击的封面处放大展开、关闭缩回原位（详见 utils/heroDialog）----
// 详情卡特化（options 覆盖后台弹窗缺省手感）：顶边锚——顶部 16:9 封面与来源缩略图重合、
// 超大来源（Featured 横幅）贴四边 morph + 淡入淡出、关场淡掉 chrome（vdc-closing）只留封面缩回、
// 圆角补偿（--vdc-art-r）、关场回位锚到轮播容器（横幅 6s 自转后不飞屏外）。
// iOS 磨砂/合成器安全策略（反馈#35/#42/#44/#45/#48）已内建于 heroDialog；本组件磨砂见下方全局块。
const hero = useHeroDialog({
  selector: '.el-dialog.vdc',
  setClosed: () => { visible.value = false }, // 关闭钮/无 done 时兜底
  zoomClass: 'vdc-zooming',
  anchor: 'top',
  coverBig: true,
  chromeClass: 'vdc-closing',
  cornerVar: '--vdc-art-r',
  reanchor: (el) => el.closest('.el-carousel') || el,
  dur: { in: 600, close: 480 },
  fadeOutClose: { dur: 180, delay: 280 },
})
// 模板 :before-close 与关闭钮共用（EP 传 done；无 done 走 setClosed）
const animatedClose = hero.animatedClose

function open(v, originEl = null) {
  video.value = v
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
  api.media.progress(v.path)
    .then((d) => { if (g === seq) progress.value = { position: d?.position || 0, duration: d?.duration || 0 } })
    .catch(() => {})
  // 记录来源矩形 → 翻 visible → nextTick 摆位起飞（zoomIn 在 paint 前跑）
  hero.open(originEl, () => { visible.value = true })
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

/* iOS 式 hero 转场 & 磨砂策略（反馈#48 三轮定稿）：
   iOS WebKit 对 backdrop-filter 层做缩放动画时采样区域不随 transform 走会错位闪烁，
   转场期卡片磨砂必须停；而"动画期配方"与"落定配方"之间无论怎么切换用户都看得见
   （一轮近实底→玻璃=磨砂迟到 0.5~1s、二轮深磨砂瞬间恢复=闪一下、三轮 0.3s 淡入
   深磨砂+saturate(1.7)=背景渐渐变深）。定稿：卡片自身永不挂 backdrop-filter
   （压掉 glass.css 全局 .el-dialog 深磨砂），磨砂观感恒由遮罩的整页 blur(8px)
   （glass.css .el-overlay）垫底 + 自身 var(--glass-bg) 半透明白提供——动画期与落定后
   完全同一配方，零切换零闪变；被 filter 的遮罩层静止不参与缩放，iOS 安全不变 */
.el-dialog.vdc {
  backdrop-filter: none !important;
  -webkit-backdrop-filter: none !important;
}
.el-dialog.vdc.vdc-zooming { will-change: transform; }
/* 关场（vdc-closing）：卡片缩回缩略图时，只有顶部 16:9 封面能与缩略图严丝合缝重合，其余
   chrome（信息区 / 关闭钮 / 续播进度条 / 卡底玻璃 + 阴影）比缩略图高、无处安放。若这些跟着
   卡片一路缩到位仍半透明可见，会与网格内容错位叠字、最后又随卸载啪地消失——观感就是"没收完、
   闪一下、突然收完"。关场一开始就快速淡掉它们（0.16s），只留封面独自缩回熔入缩略图（同图无缝）。
   横幅（big）整卡淡出消融，不进入缩略图，本区块对它无副作用。cancelClose 移除本类即恢复 */
.el-dialog.vdc.vdc-closing {
  background: transparent !important;
  border-color: transparent;
  box-shadow: none;
}
/* 顶部玻璃高光线一并隐去（内联 transition 够不着伪元素，这里自带过渡） */
.el-dialog.vdc.vdc-closing::before { opacity: 0; transition: opacity 0.16s ease; }
.el-dialog.vdc.vdc-closing .vdc-info,
.el-dialog.vdc.vdc-closing .vdc-close,
.el-dialog.vdc.vdc-closing .vdc-prog {
  opacity: 0;
  transition: opacity 0.16s ease;
}
/* 只余的封面补上四角圆角（否则下边是卡片中部直切、下方两角方，与圆角缩略图对不上）。
   半径由 JS 按 12/scale 反算注入 --vdc-art-r，被 dlg transform 缩放后落定恰好渲染成 12px */
.el-dialog.vdc.vdc-closing .vdc-art {
  border-radius: var(--vdc-art-r, 18px);
  overflow: hidden;
}
/* 卡内控件的小磨砂同样永久关闭（原只在转场期关、落定恢复=又一处"过半秒变一下"）：
   它们背后是卡片平滑渐变底，模糊与否肉眼无差，关掉换零切换 */
.el-dialog.vdc .vdc-close,
.el-dialog.vdc .el-button {
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
  background: var(--accent);
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
.vdc-link { cursor: pointer; color: var(--accent); }
.vdc-link:hover { text-decoration: underline; }

.vdc-note { margin: 12px 0 0; font-size: 12.5px; }
.vdc-ops { margin-top: 20px; display: flex; gap: 4px; }
.vdc-play { box-shadow: 0 6px 24px rgba(var(--accent-rgb), 0.35); }

/* ---- 移动端 ---- */
@media (max-width: 768px) {
  .vdc-info { padding: 16px 16px 18px; }
  .vdc-title { font-size: 19px; margin-bottom: 8px; }
  .vdc-meta { margin-bottom: 12px; }
  .vdc-ops { margin-top: 16px; }
  .vdc-ops .el-button { flex: 1; padding-left: 8px; padding-right: 8px; }
}
</style>
