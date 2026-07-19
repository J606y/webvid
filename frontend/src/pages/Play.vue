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
    theme: '#ff0000', // YouTube 红：已播进度条 / 拖拽圆点 / 音量 / 选中项统一取此色
    volume: 0.7,
    // 关掉 ArtPlayer 的 backdrop：它默认给弹窗加 .art-backdrop 类、附带一条
    // `.art-video-player.art-backdrop .art-volume-inner{background:rgba(0,0,0,.75)}`（0,3,0 高优先级），
    // 会把弹窗背景钉死成黑、盖过 --art-widget-background。关掉后弹窗背景回落到该变量（可控成白），
    // 磨砂由下方 CSS 自己加。
    backdrop: false,
    setting: true,
    playbackRate: true,
    aspectRatio: true,
    pip: true,
    fullscreen: true,
    fullscreenWeb: true,
    hotkey: true,
    autoSize: false,
    autoplay: true,
    // 中间大播放态图标换成纯三角（去掉 ArtPlayer 自带的实心圆），
    // 下方 .art-state 用液态玻璃圆承托 —— 圆由玻璃画、三角只是白色glyph。
    // svg 必须带显式 width/height 属性（ArtPlayer 自带图标全都带）：只有 viewBox 的
    // svg 在 iOS WebKit 的百分比尺寸链里会解析成 0 高，iPhone 上三角直接消失只剩玻璃圆。
    icons: {
      state: '<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 24 24"><path d="M8 5v14l11-7z"></path></svg>',
    },
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

/* ---- YouTube 风格播放器（红色细进度条 + 液态玻璃控件）---- */
/* 参考 YouTube 2025「液态玻璃」新版：底部控件不再是扁平白图标，而是每颗按钮各自
   坐在一枚半透明磨砂玻璃胶囊上（backdrop-filter 实时模糊背后画面 + 顶部高光描边）。
   进度条保持 YouTube 招牌：红色细条、悬停变粗、拖拽红点、全宽贴边。
   磨砂一律用 backdrop-filter（本项目 iOS 上验证安全的方案，backdrop 全保留不受影响），
   绝不用 filter:blur（会在 iOS 触发极光式重光栅化卡死，见项目历史）。 */
.player :deep(.art-video-player) {
  --art-progress-height: 5px;                     /* 悬停态条高；静止态取其半（~2.5px），细如 YouTube */
  --art-progress-color: rgba(255, 255, 255, .22); /* 未播放轨道 */
  --art-loaded-color: rgba(255, 255, 255, .45);   /* 已缓冲段 */
  --art-hover-color: rgba(255, 255, 255, .5);     /* 鼠标前方的预览高亮 */
  --art-indicator-size: 13px;                     /* 拖拽圆点（红色，悬停浮现） */
  --art-control-icon-size: 22px;                  /* 图标收到 YouTube 尺度，好落进玻璃胶囊 */
  --art-control-icon-scale: 1;
  --art-bottom-gap: 14px;
  --art-widget-background: rgba(255, 255, 255, .06);  /* 弹出层底色：与下方控件胶囊同透明度（关了 backdrop 后此变量才真正生效） */
}
/* 进度条全宽贴边（YouTube 招牌）：抵消底栏左右内边距，红条从边到边；
   底栏 overflow:hidden，圆点在 0% 处半探出左缘会被裁掉，恰是 YouTube 的观感。 */
.player :deep(.art-bottom .art-progress) {
  margin-left: calc(var(--art-padding) * -1);
  margin-right: calc(var(--art-padding) * -1);
}
/* 左右分组：清掉 ArtPlayer 的负边距（原本让图标视觉贴边），胶囊之间留呼吸间距 */
.player :deep(.art-controls-left),
.player :deep(.art-controls-right) {
  margin: 0;
  gap: 8px;
  align-items: center;
}
.player :deep(.art-controls) { padding-bottom: 8px; }
/* ArtPlayer 检测到手机 UA（.art-mobile）会给两个控件组加负边距让图标贴边——玻璃胶囊
   贴边很难看；上面的 margin:0 与它平级打平、而 ArtPlayer 样式是运行时注入排在产物 CSS
   之后会赢，这里按更高特异性压回。 */
.player :deep(.art-video-player.art-mobile .art-controls-left) { margin-left: 0; }
.player :deep(.art-video-player.art-mobile .art-controls-right) { margin-right: 0; }
/* 每颗控件 = 一枚液态玻璃胶囊：近乎透明的底 + 弱磨砂（透背后画面）+ 顶部高光描边 + 轻投影。
   要点：blur 压到 7px 才透（16px 会糊成厚磨砂），底色降到 .06、靠 saturate/brightness 提折射感
   与更亮的高光内描边把玻璃「形状」勾出来 —— 这才是液态玻璃而非磨砂玻璃。 */
.player :deep(.art-controls .art-control) {
  opacity: 1;                     /* 玻璃底恒显，不再靠透明度淡入淡出 */
  min-width: 42px;
  min-height: 38px;
  padding: 0 4px;
  border-radius: 13px;
  background: rgba(255, 255, 255, .06);
  border: 1px solid rgba(255, 255, 255, .2);
  -webkit-backdrop-filter: blur(7px) saturate(1.8) brightness(1.08);
  backdrop-filter: blur(7px) saturate(1.8) brightness(1.08);
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, .38), inset 0 -1px 2px rgba(0, 0, 0, .12), 0 2px 10px rgba(0, 0, 0, .22);
  transition: background var(--art-transition-duration) ease;
}
.player :deep(.art-controls .art-control:hover) { background: rgba(255, 255, 255, .18); }
/* 时间胶囊：左右多留白、数字等宽不抖，贴近 YouTube「1:26 / 4:02」 */
.player :deep(.art-control-time) {
  padding: 0 12px;
  font-size: 13px;
  font-variant-numeric: tabular-nums;
}
/* 二级弹窗（设置 / 音量竖条 / 画质选择 / 右键菜单）同款通透液态玻璃：
   弱模糊透背后画面 + 高光内描边成形，跟胶囊一个配方，不再是 Image#3 那种厚暗磨砂。 */
.player :deep(.art-settings),
.player :deep(.art-selector-list),
.player :deep(.art-contextmenus),
.player :deep(.art-volume-inner) {
  color: #fff;                                    /* 白字，和按钮白图标一致 */
  -webkit-backdrop-filter: blur(7px) saturate(1.8) brightness(1.08);
  backdrop-filter: blur(7px) saturate(1.8) brightness(1.08);
  border: 1px solid rgba(255, 255, 255, .2);
  border-radius: 14px;
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, .38), 0 8px 28px rgba(0, 0, 0, .3);
  text-shadow: 0 1px 3px rgba(0, 0, 0, .7);       /* 白字在亮画面上靠深色投影保可读（图标本有描边） */
}
/* 中间大播放态图标：液态玻璃圆 + 纯三角（图标已在 mount() 换成无实心圆的三角）。
   仅暂停/点按时浮现，玻璃圆透背后画面 + 高光描边，三角白色带投影保对比。 */
.player :deep(.art-state) {
  width: 76px;
  height: 76px;
  border-radius: 50%;
  background: rgba(255, 255, 255, .14);
  border: 1px solid rgba(255, 255, 255, .3);
  -webkit-backdrop-filter: blur(10px) saturate(1.8) brightness(1.1);
  backdrop-filter: blur(10px) saturate(1.8) brightness(1.1);
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, .45), 0 6px 22px rgba(0, 0, 0, .3);
}
.player :deep(.art-state .art-icon) {
  width: 32px;  /* 定像素并与 svg 的 width/height 属性一致：原 42% 的百分比链在 iOS WebKit
                   会把内层 svg 解析成 0 高（只剩玻璃圆没三角）；原 filter:drop-shadow 也是
                   iOS 光栅化雷区（同极光教训）一并去掉，对比度交给玻璃圆的底色+描边 */
  height: 32px;
  margin-left: 3px; /* 三角视觉重心偏左，右移一点看着才居中 */
}
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
  /* 底栏控件收一号：390px 屏减页边距后控件行只有 ~350px，桌面尺度（胶囊 42 + gap 8 +
     时间胶囊两侧 12px）七颗排不下会挤成一团。胶囊 36/34、图标 20、gap 5、时间字号 12，
     整行 ~320px 落进一行还留呼吸空间；控件行高回 44 给触控留高度（.art-mobile 压成 38 太矮）。 */
  .player :deep(.art-video-player) {
    --art-control-icon-size: 20px;
    --art-padding: 8px;
    --art-control-height: 44px;
  }
  .player :deep(.art-controls-left),
  .player :deep(.art-controls-right) { gap: 5px; }
  .player :deep(.art-controls .art-control) {
    min-width: 36px;
    min-height: 34px;
    border-radius: 12px;
    padding: 0 2px;
  }
  .player :deep(.art-control-time) { padding: 0 7px; font-size: 12px; }
  .unsupported, .detecting { margin: auto 0; } /* 兜底/探测占位同样居中，不再吊在顶部 */
}
</style>
