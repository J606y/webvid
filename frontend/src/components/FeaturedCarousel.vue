<template>
  <!-- Featured 随机推荐横幅：视频库 / 照片墙首屏共用（原两页各写一份近同的 el-carousel）。
       差异化两处经外部注入：整块点击 → select 事件；右下角按钮 → action 插槽。
       .feat* 容器样式随本组件作用域下沉；.hero-btn 属插槽内容（父级作用域）故仍留 media-library.css。 -->
  <el-carousel ref="car" class="feat" :height="height" :interval="6000"
    :autoplay="autoplay" arrow="hover"
    @touchstart="swipe.onTouchstart" @touchend="swipe.onTouchend">
    <el-carousel-item v-for="(item, i) in items" :key="item.path">
      <div class="feat-item" @click="$emit('select', item, i, $event)">
        <img :src="thumbUrl(item.path, 1200)" class="feat-img" @error="hideImg" />
        <div class="feat-mask" />
        <div class="feat-info">
          <div class="feat-kicker">随机推荐</div>
          <div class="feat-title">{{ stripExt(item.name) }}</div>
          <div class="dim feat-meta">{{ formatTime(item.modified) }} · {{ formatSize(item.size) }}</div>
          <slot name="action" :item="item" :index="i" />
        </div>
      </div>
    </el-carousel-item>
  </el-carousel>
</template>

<script setup>
import { ref } from 'vue'
import { thumbUrl } from '../utils/path'
import { formatSize, formatTime, hideImg, stripExt } from '../utils/file'

defineProps({
  items: { type: Array, default: () => [] },
  height: { type: String, default: '420px' },
  autoplay: { type: Boolean, default: true }, // keep-alive 切走时置 false 暂停轮播空转
  // 触屏左右滑动手势（来自 useMediaLibrary 的 swipe，其回调经父级 carousel ref 调本组件 next/prev）
  swipe: { type: Object, default: () => ({ onTouchstart() {}, onTouchend() {} }) },
})
defineEmits(['select']) // select(item, index, $event)：整块点击，$event.currentTarget=.feat-item 供取转场锚

// 暴露 next/prev：父级把本组件绑为 useMediaLibrary 的 carousel ref，swipe 回调调这两个方法翻页
const car = ref(null)
defineExpose({
  next: () => car.value?.next(),
  prev: () => car.value?.prev(),
})
</script>

<style scoped>
/* ---- Featured 横幅（原在 media-library.css，随组件抽出下沉到此） ---- */
.feat {
  margin: 4px 0 36px;
  border-radius: 22px;
  overflow: hidden;
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.5);
}
.feat-item { position: relative; height: 100%; cursor: pointer; background: #101018; }
.feat-img { width: 100%; height: 100%; object-fit: cover; display: block; }
.feat-mask {
  position: absolute; inset: 0;
  background: linear-gradient(to top, rgba(5, 6, 10, 0.94) 0%, rgba(5, 6, 10, 0.35) 42%, transparent 72%);
}
.feat-info { position: absolute; left: 40px; bottom: 32px; right: 40px; }
.feat-kicker {
  font-size: 12px; letter-spacing: 3px; color: var(--text-dim);
  text-transform: uppercase; margin-bottom: 8px;
}
.feat-title { font-size: 34px; font-weight: 800; margin-bottom: 8px; text-shadow: 0 2px 14px rgba(0, 0, 0, .6); }
.feat-meta { margin-bottom: 18px; font-size: 13px; }
.feat :deep(.el-carousel__indicators--horizontal) {
  left: auto; right: 26px; bottom: 14px; transform: none;
}
.feat :deep(.el-carousel__button) { width: 7px; height: 7px; border-radius: 50%; opacity: 0.4; }
.feat :deep(.el-carousel__indicator.is-active .el-carousel__button) { opacity: 1; }

@media (max-width: 768px) {
  .feat { margin-bottom: 24px; border-radius: 16px; }
  .feat-info { left: 16px; right: 16px; bottom: 16px; }
  .feat-kicker { letter-spacing: 2px; margin-bottom: 4px; }
  .feat-title { font-size: 20px; margin-bottom: 4px; }
  .feat-meta { margin-bottom: 10px; font-size: 12px; }
  .feat :deep(.el-carousel__indicators--horizontal) { right: 14px; bottom: 8px; }
}
</style>
