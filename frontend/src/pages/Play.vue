<template>
  <div class="page play-page">
    <div class="head">
      <el-icon class="back" :size="20" @click="$router.back()"><Back /></el-icon>
      <h1 class="title" :title="name">{{ name }}</h1>
      <span v-if="reason" class="badge">{{ reason === 'remux' ? '转封装' : '转码' }}</span>
    </div>

    <div v-if="strategy === 'direct' || strategy === 'hls'" ref="artRef" class="player glass glass-panel" />

    <div v-else-if="strategy === 'unsupported'" class="unsupported glass glass-panel">
      <el-icon :size="46" class="dim"><VideoCamera /></el-icon>
      <p>{{ message }}</p>
      <a :href="rawUrl(path, true)">
        <el-button type="primary" round :icon="Download">下载原文件</el-button>
      </a>
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
import { Back, VideoCamera, Download, Loading } from '@element-plus/icons-vue'
import Artplayer from 'artplayer'
import http from '../api/http'
import { fetchVideoInfo } from '../utils/videoInfo'
import { rawUrl, hlsUrl, fromParams } from '../utils/path'

const route = useRoute()
const path = computed(() => fromParams(route.params.path))
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
const artRef = ref(null)
let art = null
let hls = null
let resumeAt = 0        // 续播起点（秒），起播后定位到此处
let reportTimer = null  // 进度定时上报句柄

onMounted(async () => {
  // 起播前取续播位置，与 /video/info 并行请求，不额外拖慢起播；
  // ?restart=1（详情卡「从头播放」）跳过续播定位
  const progP = route.query.restart === '1'
    ? Promise.resolve()
    : http.get('/media/progress', { params: { path: path.value } })
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
})

// report 上报播放进度：起播 position=0 只刷"最近播放"；播放中带当前秒数；
// duration 供货架/详情卡画进度条（后端只在 duration>0 时更新，避免元数据未就绪的
// 早期上报把已知时长覆盖成 0）。silent 失败不打扰。频率由各调用点节流。
function report(position) {
  const sec = Math.floor(position || 0)
  const dur = art && isFinite(art.duration) ? art.duration : 0
  http.post('/media/played', { path: path.value, position: sec, duration: dur }, { silent: true }).catch(() => {})
}

async function mount(url, isHls) {
  await nextTick()
  if (!artRef.value) return // 异步期间已快速离页卸载，别再建播放器（否则 ArtPlayer 报 container 无效）
  const opts = {
    container: artRef.value,
    url,
    title: name.value,
    theme: '#7aa2ff',
    volume: 0.7,
    setting: true,
    playbackRate: true,
    aspectRatio: true,
    pip: true,
    fullscreen: true,
    fullscreenWeb: true,
    hotkey: true,
    autoSize: false,
    autoplay: true,
  }
  if (isHls) {
    const { default: Hls } = await import('hls.js') // 独立 chunk，仅转码播放时加载
    opts.type = 'm3u8'
    opts.customType = {
      m3u8(video, src) {
        if (Hls.isSupported()) {
          if (hls) hls.destroy()
          // 续播：从 resumeAt 起（0 = 从头）。event 型列表（remux 边跑边播）默认会追
          // "直播沿"，显式 startPosition 强制落到目标位置；vod 列表本就全时间轴可 seek。
          hls = new Hls({ startPosition: resumeAt })
          hls.loadSource(src)
          hls.attachMedia(video)
        } else {
          video.src = src // Safari 原生 HLS：token 已注入播放列表
        }
      },
    }
  }
  art = new Artplayer(opts)

  // 续播定位：direct / Safari 原生 HLS 走 video.currentTime；hls.js 已在 startPosition 处理
  art.on('ready', () => {
    if (resumeAt > 0 && !(isHls && hls)) art.currentTime = resumeAt
    report(art.currentTime || 0) // 起播即记一次最近播放
  })
  // 元数据就绪后补记一次，确保 duration 落库（ready 可能早于 metadata，dur 尚为 0）
  art.on('video:loadedmetadata', () => report(art.currentTime || 0))
  // 播放中每 10s 上报一次；暂停/跳转/播完各补一次
  art.on('video:timeupdate', () => {
    if (reportTimer) return
    reportTimer = setTimeout(() => {
      reportTimer = null
      if (art && !art.paused) report(art.currentTime)
    }, 10000)
  })
  art.on('video:pause', () => report(art.currentTime))
  art.on('video:seeked', () => report(art.currentTime))
  art.on('video:ended', () => report(art.duration || 0)) // 播完上报满进度 → 后端归零，下次从头
}

onBeforeUnmount(() => {
  if (reportTimer) { clearTimeout(reportTimer); reportTimer = null }
  if (art) {
    try { report(art.currentTime) } catch { /* 离页最后一次进度，忽略异常 */ }
    art.destroy(true)
    art = null
  }
  if (hls) {
    hls.destroy()
    hls = null
  }
})
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
  background: rgba(122, 162, 255, .18); color: var(--accent, #7aa2ff);
  border: 1px solid rgba(122, 162, 255, .35);
  backdrop-filter: blur(8px);
  -webkit-backdrop-filter: blur(8px);
}
.player { aspect-ratio: 16/9; overflow: hidden; }
.unsupported, .detecting {
  padding: 70px 24px; text-align: center;
  display: flex; flex-direction: column; align-items: center; gap: 12px;
}
.unsupported p { margin: 0 0 8px; font-size: 15px; }
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
  .player {
    border-radius: 14px;
    width: 100%;
    margin: auto 0; /* 头部之下剩余空间垂直居中 */
  }
  .unsupported, .detecting { margin: auto 0; } /* 兜底/探测占位同样居中，不再吊在顶部 */
}
</style>
