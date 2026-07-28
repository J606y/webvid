<template>
  <div class="page play-page">
    <div class="head">
      <el-icon class="back" :size="20" @click="$router.back()"><Back /></el-icon>
      <h1 class="title" :title="name">{{ name }}</h1>
      <span v-if="reason" class="badge">{{ reason === 'remux' ? '转封装' : '转码' }}</span>
    </div>

    <!-- 播放器插槽：真正的播放器容器由 utils/playerHost 自建后塞进来（它要能跨页面存活，
         不能归 Vue 管，详见该模块）。本页只负责给它一个位置。 -->
    <div v-if="strategy === 'direct' || strategy === 'hls'" ref="slotRef" class="player-slot glass glass-panel" />

    <div v-else-if="strategy === 'unsupported'" class="unsupported glass glass-panel">
      <el-icon :size="46" class="dim"><VideoCamera /></el-icon>
      <p>{{ message }}</p>
      <div class="ops">
        <!-- 重试仅在运行期中断时给出（探测期判定的「格式不支持」重试无意义） -->
        <el-button v-if="canRetry" round :icon="RefreshRight" @click="start(true)">重试</el-button>
        <a :href="rawUrl(path, true)">
          <el-button type="primary" round :icon="Download">下载原文件</el-button>
        </a>
      </div>
    </div>

    <!-- 探测未回前的占位（非直连格式才会经历）：给「立即播放」即时反馈，
         别让页面停在空白面板上看着像卡死。多数情况详情卡已探完，秒切走。 -->
    <div v-else class="detecting glass glass-panel">
      <el-icon :size="34" class="is-loading"><Loading /></el-icon>
      <p class="dim">检测格式中…</p>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { useRoute } from 'vue-router'
import { Back, VideoCamera, Download, Loading, RefreshRight } from '@element-plus/icons-vue'
import { api } from '../utils/api'
import { fetchVideoInfo } from '../utils/videoInfo'
import { rawUrl, hlsUrl, fromParams } from '../utils/path'
import * as player from '../utils/playerHost'

const route = useRoute()
// path 冻结在进入播放页时的值（不跟 route 变）：离开时路由先改、组件后卸载，
// 若用 computed，onBeforeUnmount 的末次进度和迟到的 loadedmetadata 补报会拿
// "/" 去上报 —— /media/played 404 噪音，且真正的末次进度丢失
const path = ref(fromParams(route.params.path))
const name = computed(() => path.value.split('/').filter(Boolean).pop() || '')

// 与后端 handler_video.go directPlayExts 一致：原生容器不必等 /video/info
// （对云盘是一次网络往返）就能确定直连播放，进页立刻挂播放器秒开。
// 其余格式交 /video/info 探测决策（hls = remux/转码，unsupported = 下载兜底）。
const DIRECT_EXTS = new Set(['mp4', 'webm', 'mov', 'm4v'])

// 直连格式无需探测，起手即定 'direct'（进页首帧就渲染播放器，不闪占位）；
// 其余留空 ''，模板落到「检测格式中…」占位，探测回来再切成 hls/unsupported。
const ext0 = name.value.slice(name.value.lastIndexOf('.') + 1).toLowerCase()
const strategy = ref(DIRECT_EXTS.has(ext0) ? 'direct' : '')
const message = ref('')
const reason = ref('')
const canRetry = ref(false) // 兜底面板是否给「重试」（运行期中断才给）
const slotRef = ref(null)
let resumeAt = 0            // 续播起点（秒），起播后定位到此处

// start 起播全流程：取续播位置 → 定播放策略 → 挂播放器。
// keepResume=true 时沿用当前 resumeAt（运行期中断后重试，从断点接着播），
// 否则回服务端取续播位置。
async function start(keepResume = false) {
  player.destroy() // 重试前收掉上一轮播放器与转码流（首次进入是空操作）
  strategy.value = DIRECT_EXTS.has(ext0) ? 'direct' : ''
  message.value = ''
  reason.value = ''
  canRetry.value = false

  // 起播前取续播位置，与 /video/info 并行请求，不额外拖慢起播；
  // ?restart=1（详情卡「从头播放」）跳过续播定位
  const progP = keepResume || route.query.restart === '1'
    ? Promise.resolve()
    : api.media.progress(path.value)
      .then((d) => { resumeAt = d?.position > 0 ? d.position : 0 })
      .catch(() => {})

  if (DIRECT_EXTS.has(ext0)) {
    await progP
    await mount(rawUrl(path.value), false)
    return
  }
  // 非直连：走共享探测（详情卡多半已探完 → 秒回；否则接其在途探测），
  // 探回前模板停在「检测格式中…」占位。探测失败降级为可下载兜底，别把页面卡在占位上。
  let d
  try {
    const r = await Promise.all([fetchVideoInfo(path.value), progP])
    d = r[0]
  } catch {
    strategy.value = 'unsupported'
    message.value = '该视频暂时无法播放，可下载后本地观看'
    return
  }
  strategy.value = d.strategy
  message.value = d.message || ''
  reason.value = d.reason || ''
  if (d.strategy === 'unsupported') return
  if (d.strategy === 'direct') await mount(rawUrl(path.value), false)
  else await mount(hlsUrl(path.value), true)
}

// mount 把播放器要到本页的插槽里。插槽由 v-if 控制，等一帧渲染出来再要。
async function mount(url, isHls) {
  await nextTick()
  if (!slotRef.value) return // 异步期间已快速离页卸载，别再建播放器
  await player.attach(slotRef.value, {
    path: path.value, url, isHls, resumeAt, reason: reason.value, onFail: fail,
  })
}

// fail 运行期播放中断（playerHost 回调）→ 落到可重试、可下载的兜底面板。
// 断点由 playerHost 交回，重试从中断处接着播。
function fail(msg, at) {
  resumeAt = at || 0
  strategy.value = 'unsupported'
  message.value = msg
  canRetry.value = true
}

onMounted(() => {
  // 这部片的播放器还活着 = 用户刚从系统画中画的小窗还原回来。直接把它接回页面：
  // 不重新探测、不重新起播，进度与小窗里的一模一样。
  const snap = player.snapshot(path.value)
  if (snap) {
    strategy.value = snap.isHls ? 'hls' : 'direct'
    reason.value = snap.reason
    nextTick(() => {
      if (slotRef.value) player.attach(slotRef.value, { path: path.value, onFail: fail })
    })
    return
  }
  start()
})

// 离页：在画中画里就寄存（小窗继续播），否则收掉。见 utils/playerHost
onBeforeUnmount(() => player.leave())
</script>

<style scoped>
.play-page { max-width: 1200px; }
.head { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
.back { cursor: pointer; }
.back:hover { color: var(--accent); }
.title {
  margin: 0; font-size: 18px; font-weight: 600;
  min-width: 0; /* flex 行内允许收缩，长标题才会走省略号而非撑开挤掉徽标 */
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.badge {
  flex: none; font-size: 12px; padding: 2px 10px; border-radius: 999px;
  background: rgba(var(--accent-rgb), .18); color: var(--accent);
  border: 1px solid rgba(var(--accent-rgb), .35);
  backdrop-filter: blur(8px);
  -webkit-backdrop-filter: blur(8px);
}
/* 插槽只管排版与"播放器还没就位"时的那块玻璃底（首帧不留白）；
   播放器自身外观在 assets/player.css（容器不带 scoped 属性，见 utils/playerHost） */
.player-slot { width: 100%; aspect-ratio: 16/9; overflow: hidden; }
.unsupported, .detecting {
  padding: 70px 24px; text-align: center;
  display: flex; flex-direction: column; align-items: center; gap: 12px;
}
.unsupported p { margin: 0 0 8px; font-size: 15px; }
.unsupported .ops { display: flex; align-items: center; gap: 10px; }
.detecting { aspect-ratio: 16/9; justify-content: center; }
.detecting p { margin: 0; font-size: 14px; }

/* ---- 移动端：沉浸式聚焦 ---- */
/* 顶栏/底部 Tab 栏由 App.vue 在播放页隐藏，这里让页面吃满视口、自管上下安全区留白，
   头部收成一行贴顶，播放器在头部之下的剩余空间垂直居中，消除大片空白。 */
@media (max-width: 768px) {
  .play-page {
    min-height: 100vh;
    min-height: 100dvh; /* 移动端浏览器地址栏收缩后仍占满 */
    display: flex;
    flex-direction: column;
    padding: calc(12px + env(safe-area-inset-top)) 12px calc(12px + env(safe-area-inset-bottom));
  }
  .head { gap: 8px; margin-bottom: 0; }
  .title { font-size: 15px; }
  .player-slot { margin: auto 0; } /* 头部之下剩余空间垂直居中 */
  .unsupported, .detecting { margin: auto 0; } /* 兜底/探测占位同样居中，不再吊在顶部 */
}
</style>
