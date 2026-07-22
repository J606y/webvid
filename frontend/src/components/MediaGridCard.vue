<template>
  <!-- 文件管理 / 搜索 方格视图卡片（原两页各写一份同构 .g-card）。
       .g-card/.g-thumb/.g-name/.abs/.thumb-fallback 均为全局样式（glass.css），故本组件无需自带。
       缩略图源路径与点击行为由父级注入：Files 用 join(当前目录,名)、Search 用 it.path。 -->
  <div class="g-card glass glass-hover" @click="$emit('open')">
    <div class="g-thumb">
      <img v-if="hasThumb" :src="thumbUrl(thumbPath, 320)" loading="lazy" @error="hideImg" />
      <div class="thumb-fallback abs">
        <el-icon :size="34" :class="{ folder: isDir }">
          <component :is="icons[iconKey]" />
        </el-icon>
      </div>
    </div>
    <div class="g-name" :title="label">{{ label }}</div>
  </div>
</template>

<script setup>
import { iconMap as icons } from '../utils/icons'
import { thumbUrl } from '../utils/path'
import { hideImg } from '../utils/file'

defineProps({
  thumbPath: { type: String, required: true }, // thumbUrl 源路径（Files: 当前目录+名；Search: 全路径）
  label: { type: String, default: '' },
  iconKey: { type: String, default: 'Files' }, // typeIcon() 结果字符串，无缩略图时的兜底图标
  hasThumb: { type: Boolean, default: false },
  isDir: { type: Boolean, default: false },
})
defineEmits(['open'])
</script>

<style scoped>
/* 目录图标染琥珀色：.folder 在 Files/Search 列表视图各自 scoped 定义，此处兜底方格视图的目录图标 */
.folder { color: #ffd479; }
</style>
