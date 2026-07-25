<template>
  <!-- 文件管理 / 搜索 方格视图卡片（原两页各写一份同构 .g-card）。
       .g-card/.g-thumb/.g-name/.abs/.thumb-fallback 均为全局样式（glass.css），故本组件无需自带。
       缩略图源路径与点击行为由父级注入：Files 用 join(当前目录,名)、Search 用 it.path。
       选择框与操作菜单默认关闭（搜索页只看不管），文件管理传入后才出现。 -->
  <div class="g-card glass glass-hover" :class="{ picked: selected }" @click="$emit('open')">
    <div class="g-thumb">
      <img v-if="hasThumb" :src="thumbUrl(thumbPath, 320)" loading="lazy" @error="hideImg" />
      <div class="thumb-fallback abs">
        <el-icon :size="34" :class="{ folder: isDir }">
          <component :is="icons[iconKey]" />
        </el-icon>
      </div>

      <!-- 选择框常驻而非 hover 才现：触屏没有 hover，藏起来等于不存在 -->
      <span v-if="selectable" class="pick" @click.stop>
        <el-checkbox :model-value="selected" @change="$emit('update:selected', $event)" />
      </span>

      <span v-if="actions.length" class="more" @click.stop>
        <el-dropdown trigger="click" @command="$emit('command', $event)">
          <el-button class="more-btn" circle size="small" :icon="MoreFilled" />
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item v-if="actions.includes('rename')" command="rename" :icon="EditPen">
                重命名
              </el-dropdown-item>
              <el-dropdown-item v-if="actions.includes('download')" command="download" :icon="Download">
                下载
              </el-dropdown-item>
              <el-dropdown-item v-if="actions.includes('remove')" command="remove" :icon="Delete" divided>
                删除
              </el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </span>
    </div>
    <div class="g-name" :title="label">{{ label }}</div>
  </div>
</template>

<script setup>
import { MoreFilled, EditPen, Download, Delete } from '@element-plus/icons-vue'
import { iconMap as icons } from '../utils/icons'
import { thumbUrl } from '../utils/path'
import { hideImg } from '../utils/file'

defineProps({
  thumbPath: { type: String, required: true }, // thumbUrl 源路径（Files: 当前目录+名；Search: 全路径）
  label: { type: String, default: '' },
  iconKey: { type: String, default: 'Files' }, // typeIcon() 结果字符串，无缩略图时的兜底图标
  hasThumb: { type: Boolean, default: false },
  isDir: { type: Boolean, default: false },
  selectable: { type: Boolean, default: false },
  selected: { type: Boolean, default: false },
  // 操作菜单项，由父级按写权限与文件类型决定：rename / download / remove
  actions: { type: Array, default: () => [] },
})
defineEmits(['open', 'update:selected', 'command'])
</script>

<style scoped>
/* 目录图标染琥珀色：.folder 在 Files/Search 列表视图各自 scoped 定义，此处兜底方格视图的目录图标 */
.folder { color: #ffd479; }

/* 选中态：描边 + 内发光，与列表视图的整行高亮同一层意思 */
.g-card.picked {
  outline: 2px solid var(--accent);
  outline-offset: -2px;
  box-shadow: 0 0 0 1px rgba(var(--accent-rgb), .35), 0 6px 20px rgba(var(--accent-rgb), .18);
}

.pick, .more { position: absolute; z-index: 3; top: 6px; }
.pick { left: 8px; }
.more { right: 6px; }

/* 两个角标都坐在一枚半透明磨砂片上，缩略图明暗不定时仍分得清 */
.pick :deep(.el-checkbox), .more-btn {
  border-radius: 8px;
  background: rgba(0, 0, 0, .38);
  -webkit-backdrop-filter: blur(6px);
  backdrop-filter: blur(6px);
}
.pick :deep(.el-checkbox) { height: 26px; padding: 0 5px; }
.more-btn {
  width: 26px; height: 26px;
  border: none; color: #fff;
}
.more-btn:hover, .more-btn:focus { background: rgba(0, 0, 0, .6); color: #fff; }
</style>
