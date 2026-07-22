<template>
  <div class="page search-page">
    <h1 class="page-title">全局搜索</h1>
    <div class="bar glass">
      <el-input v-model="q" size="large" placeholder="输入文件或目录名…" clearable
        :prefix-icon="SearchIcon" @keyup.enter="doSearch" />
      <el-radio-group v-model="kind" size="large" @change="doSearch">
        <el-radio-button value="">全部</el-radio-button>
        <el-radio-button value="video">视频</el-radio-button>
        <el-radio-button value="image">图片</el-radio-button>
      </el-radio-group>
      <el-button type="primary" size="large" :loading="loading" @click="doSearch">搜索</el-button>
    </div>

    <div v-if="searched && !items.length" class="dim empty">没有找到「{{ lastQ }}」相关的内容</div>

    <template v-if="items.length">
      <!-- 结果头：数量 + 列表/方格视图切换（复用全站 app.viewMode，与文件管理一致并持久化） -->
      <div class="results-head">
        <span class="dim count">找到 {{ items.length }} 项</span>
        <div class="spacer" />
        <el-radio-group :model-value="app.viewMode" size="small" @change="app.setViewMode">
          <el-radio-button value="list"><el-icon><Expand /></el-icon></el-radio-button>
          <el-radio-button value="grid"><el-icon><Grid /></el-icon></el-radio-button>
        </el-radio-group>
      </div>

      <!-- 列表视图 -->
      <div v-if="app.viewMode === 'list'" class="results glass">
        <div v-for="it in items" :key="it.path" class="result" @click="open(it)">
          <el-icon :size="20" :class="{ folder: it.is_dir }">
            <component :is="icons[typeIcon(it)]" />
          </el-icon>
          <div class="info">
            <div class="name">{{ it.name }}</div>
            <div class="dim path">{{ it.path }}</div>
          </div>
          <div class="dim meta">
            <span v-if="!it.is_dir">{{ formatSize(it.size) }}</span>
            <span>{{ formatTime(it.modified) }}</span>
          </div>
        </div>
      </div>

      <!-- 方格网视图 -->
      <div v-else class="poster-grid">
        <MediaGridCard v-for="it in items" :key="it.path"
          :thumb-path="it.path" :label="it.name"
          :icon-key="typeIcon(it)" :has-thumb="hasThumb(it)" :is-dir="it.is_dir"
          @open="open(it)" />
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { Search as SearchIcon, Expand, Grid } from '@element-plus/icons-vue'
import { iconMap as icons } from '../utils/icons'
import { api } from '../utils/api'
import { filesRoute, playRoute, parent } from '../utils/path'
import { extType, typeIcon, formatSize, formatTime, hasThumb } from '../utils/file'
import { useApp } from '../stores/app'
import MediaGridCard from '../components/MediaGridCard.vue'

defineOptions({ name: 'Search' }) // App.vue keep-alive include 按此名匹配

const app = useApp() // 复用全站列表/方格视图偏好（与文件管理共享、持久化到 localStorage）
const router = useRouter()
const q = ref('')
const kind = ref('')
const items = ref([])
const loading = ref(false)
const searched = ref(false)
const lastQ = ref('')

async function doSearch() {
  const query = q.value.trim()
  if (!query) return
  loading.value = true
  try {
    const params = { q: query, limit: 100 }
    if (kind.value) params.type = kind.value
    const d = await api.fs.search(params)
    items.value = d.items || []
    lastQ.value = query
    searched.value = true
  } finally {
    loading.value = false
  }
}

function open(it) {
  if (it.is_dir) return router.push(filesRoute(it.path))
  switch (extType(it.name)) {
    case 'video': return router.push(playRoute(it.path))
    case 'image': return router.push({ path: '/library/photos', query: { dir: parent(it.path) } })
    default: return router.push(filesRoute(parent(it.path)))
  }
}

// 方格视图：仅图片/视频有缩略图，其余回退到类型图标
</script>

<style scoped>
.search-page { max-width: 900px; }
.bar {
  display: flex; gap: 12px; align-items: center;
  padding: 14px; margin-bottom: 18px;
}
.bar :deep(.el-input) { flex: 1; }
.empty { text-align: center; padding: 50px 0; }

/* ---- 结果头（数量 + 视图切换） ---- */
.results-head { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; }
.results-head .count { font-size: 13px; }
.spacer { flex: 1; }

/* 方格网视图：.g-card/.g-thumb/.abs/.g-name 与 .poster-grid 均已上提到全局 glass.css（与 Files.vue 共用） */

.results { padding: 6px; }
.result {
  display: flex; align-items: center; gap: 12px;
  padding: 11px 14px; border-radius: 10px; cursor: pointer;
}
.result:hover { background: rgba(255, 255, 255, 0.06); }
.folder { color: #ffd479; }
.info { flex: 1; min-width: 0; }
.name { font-size: 14px; }
.path { font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.meta { display: flex; gap: 14px; font-size: 12px; flex-shrink: 0; }

/* ---- 移动端 ---- */
@media (max-width: 768px) {
  /* 搜索条一行放不下：输入框独占一行，类型/按钮折到第二行 */
  .bar { flex-wrap: wrap; padding: 12px; gap: 10px; }
  .bar :deep(.el-input) { flex: 1 1 100%; }
  /* 大小/时间折到名称下方一行，给路径留满宽度 */
  .result { flex-wrap: wrap; row-gap: 2px; }
  .meta { flex-basis: 100%; padding-left: 32px; }
}
</style>
