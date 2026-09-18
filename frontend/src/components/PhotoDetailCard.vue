<template>
  <!-- 照片详情二级卡片：结构对齐 VideoDetailCard，信息换成照片关心的那几项。
       分辨率不劳后端探测 —— 这张卡本来就要显示原图，读它的 naturalWidth 即是真值。 -->
  <el-dialog v-model="visible" class="pdc" width="720px" append-to-body align-center
    :show-close="false" destroy-on-close :before-close="animatedClose">
    <div v-if="photo" class="pdc-body">
      <div class="pdc-art">
        <div class="pdc-art-fallback"><el-icon :size="46"><Picture /></el-icon></div>
        <!-- 低清底图：网格 320 已在浏览器缓存，转场起飞瞬间就有画面，莫奈取色也挂在它上面；
             原图到货后盖上，顺带报出真实分辨率（同 VideoDetailCard 的双层图套路） -->
        <img class="pdc-art-lo" :key="'lo:' + photo.path" :src="thumbUrl(photo.path, 480)"
          alt="" @load="onLoLoad" @error="hideImg" />
        <img :key="photo.path" :src="rawUrl(photo.path)" @load="onFullLoad" @error="hideImg" />
        <button class="pdc-close" @click="animatedClose()">
          <el-icon :size="16"><Close /></el-icon>
        </button>
      </div>
      <div class="pdc-info" :style="tintStyle">
        <h2 class="pdc-title">{{ stripExt(photo.name) }}</h2>
        <div class="pdc-meta">
          <span class="pdc-badge">{{ ext }}</span>
          <span v-if="dimText" class="dim">{{ dimText }}</span>
          <span v-else class="dim">读取尺寸中…</span>
          <span class="dim">{{ formatSize(photo.size) }}</span>
        </div>
        <div class="pdc-rows">
          <div class="pdc-row"><span class="k">文件名</span><span class="v">{{ photo.name }}</span></div>
          <div class="pdc-row">
            <span class="k">所在目录</span>
            <a class="v pdc-link" @click="goDir">{{ dirPath }}</a>
          </div>
          <div class="pdc-row"><span class="k">修改时间</span><span class="v">{{ formatTime(photo.modified) }}</span></div>
        </div>
        <div class="pdc-ops">
          <el-button size="large" round :icon="FolderOpened" @click="goDir">文件位置</el-button>
        </div>
      </div>
    </div>
  </el-dialog>
</template>

<script setup>
import { ref, computed } from 'vue'
import { useRouter, onBeforeRouteLeave } from 'vue-router'
import { Close, FolderOpened, Picture } from '@element-plus/icons-vue'
import { useHeroDialog } from '../utils/heroDialog'
import { extractVibrant } from '../utils/monet'
import { thumbUrl, rawUrl, filesRoute, parent } from '../utils/path'
import { formatSize, formatTime, hideImg, stripExt } from '../utils/file'

const router = useRouter()
const visible = ref(false)
const photo = ref(null)
const dim = ref(null)  // 原图到货后才有 {w, h}
const tint = ref(null) // 莫奈主色 {r,g,b}，取不到 = 中性玻璃

const ext = computed(() => {
  const i = photo.value?.name.lastIndexOf('.') ?? -1
  return i > 0 ? photo.value.name.slice(i + 1).toUpperCase() : '图片'
})
const dirPath = computed(() => (photo.value ? parent(photo.value.path) : ''))
const dimText = computed(() => (dim.value ? `${dim.value.w}×${dim.value.h}` : ''))

// 信息区玻璃染色：主色由近及远渐隐（叠在 backdrop 磨砂上，保持玻璃质感）
const tintStyle = computed(() => {
  if (!tint.value) return {}
  const { r, g, b } = tint.value
  return {
    background: `linear-gradient(180deg, rgba(${r}, ${g}, ${b}, 0.30) 0%, `
      + `rgba(${(r * 0.55) | 0}, ${(g * 0.55) | 0}, ${(b * 0.55) | 0}, 0.14) 100%)`,
  }
})

// :key 切换后旧 img 的迟到 load 要忽略（已脱离文档）
function onLoLoad(e) {
  if (!e.target.isConnected) return
  tint.value = extractVibrant(e.target)
}
function onFullLoad(e) {
  const img = e.target
  if (!img.isConnected) return
  dim.value = { w: img.naturalWidth, h: img.naturalHeight }
  tint.value = extractVibrant(img) // 原图取色更准，到货后精修一次
}

// iOS 式 hero 转场：从点击的缩略图处放大展开、关闭缩回原位（详见 utils/heroDialog）
const hero = useHeroDialog({
  selector: '.el-dialog.pdc',
  setClosed: () => { visible.value = false },
  zoomClass: 'pdc-zooming',
  anchor: 'top',
  coverBig: true,
  chromeClass: 'pdc-closing',
  cornerVar: '--pdc-art-r',
  reanchor: (el) => el.closest('.el-carousel') || el,
  dur: { in: 600, close: 480 },
  fadeOutClose: { dur: 180, delay: 280 },
})
const animatedClose = hero.animatedClose

function open(p, originEl = null) {
  photo.value = p
  dim.value = null
  tint.value = null
  hero.open(originEl, () => { visible.value = true })
}
defineExpose({ open })

// 宿主页被 keep-alive 缓存，弹窗 teleport 在 body 上不随组件树隐藏（同 VideoDetailCard）
onBeforeRouteLeave(() => { visible.value = false })

function goDir() {
  visible.value = false
  router.push(filesRoute(dirPath.value))
}
</script>

<!-- 弹窗壳 append-to-body 渲染在组件外，须全局样式 -->
<style>
.el-dialog.pdc {
  max-width: 94vw;
  padding: 0;
  border-radius: 18px;
  overflow: hidden;
  background: var(--glass-bg, rgba(255, 255, 255, 0.07));
  border: 1px solid var(--glass-border, rgba(255, 255, 255, 0.14));
  /* 外圈高光描边，配方同 VideoDetailCard：上缘最亮 + 整圈弱光 + 下缘反射 */
  box-shadow:
    inset 0 1px 0 rgba(255, 255, 255, 0.34),
    inset 0 0 0 1px rgba(255, 255, 255, 0.1),
    inset 0 -1px 0 rgba(255, 255, 255, 0.06),
    0 30px 80px rgba(0, 0, 0, 0.65);
}
.el-dialog.pdc::before { /* 玻璃顶部高光线（同 .glass 签名） */
  content: '';
  position: absolute; z-index: 3;
  left: 8%; right: 8%; top: 0; height: 1px;
  background: linear-gradient(90deg, transparent, rgba(255, 255, 255, 0.4), transparent);
  pointer-events: none;
}
.el-dialog.pdc .el-dialog__header,
.el-dialog.pdc .el-dialog__body { padding: 0; margin: 0; }
</style>

<style scoped>
.pdc-art {
  position: relative;
  /* 照片比例千奇百怪，给一个上限高度并 contain —— 详情卡就该看到完整构图，不裁 */
  max-height: 52vh;
  min-height: 220px;
  background: #0d0d14;
  border-radius: var(--pdc-art-r, 0);
  overflow: hidden;
}
.pdc-art img {
  position: relative; z-index: 1;
  display: block;
  width: 100%;
  max-height: 52vh;
  object-fit: contain;
}
/* 低清底图垫在原图之下，原图到货即盖上 */
.pdc-art-lo { position: absolute; inset: 0; z-index: 0; width: 100%; height: 100%; }
.pdc-art-fallback {
  position: absolute; inset: 0;
  display: flex; align-items: center; justify-content: center;
  color: rgba(255, 255, 255, 0.35);
}
.pdc-close {
  position: absolute; z-index: 4; top: 12px; right: 12px;
  width: 30px; height: 30px;
  display: flex; align-items: center; justify-content: center;
  border: none; border-radius: 50%;
  background: rgba(0, 0, 0, 0.42);
  color: #fff; cursor: pointer;
  -webkit-backdrop-filter: blur(6px);
  backdrop-filter: blur(6px);
}
.pdc-close:hover { background: rgba(0, 0, 0, 0.62); }

.pdc-info { padding: 18px 22px 20px; }
.pdc-title {
  margin: 0 0 8px; font-size: 19px; font-weight: 650;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.pdc-meta {
  display: flex; flex-wrap: wrap; align-items: center; gap: 10px;
  font-size: 13px; margin-bottom: 14px;
}
.pdc-badge {
  padding: 2px 10px; border-radius: 999px; font-size: 12px;
  background: rgba(var(--accent-rgb), 0.18);
  color: var(--accent);
  border: 1px solid rgba(var(--accent-rgb), 0.35);
}
.pdc-rows { display: flex; flex-direction: column; gap: 7px; margin-bottom: 16px; }
.pdc-row { display: flex; gap: 12px; font-size: 13px; min-width: 0; }
.pdc-row .k { flex: 0 0 68px; opacity: 0.6; }
.pdc-row .v { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pdc-link { cursor: pointer; color: var(--accent); }
.pdc-link:hover { text-decoration: underline; }
.pdc-ops { display: flex; flex-wrap: wrap; gap: 10px; }

@media (max-width: 768px) {
  .pdc-art, .pdc-art img { max-height: 42vh; }
  .pdc-info { padding: 14px 16px 16px; }
  .pdc-title { font-size: 16px; }
  .pdc-row .k { flex-basis: 60px; }
}
</style>
