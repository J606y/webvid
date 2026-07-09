<template>
  <div class="page" @dragover.prevent="onDragOver" @drop.prevent="onDrop" @dragleave="dragging = false">
    <div v-if="dragging && caps.upload" class="drop-mask glass-panel">
      <el-icon :size="46"><UploadFilled /></el-icon>
      <p>松开以上传到 {{ current }}</p>
    </div>

    <!-- 面包屑 -->
    <el-breadcrumb class="crumbs" separator="/">
      <el-breadcrumb-item>
        <router-link to="/files" class="crumb"><el-icon><HomeFilled /></el-icon></router-link>
      </el-breadcrumb-item>
      <el-breadcrumb-item v-for="c in crumbs" :key="c.path">
        <router-link :to="filesRoute(c.path)" class="crumb">{{ c.name }}</router-link>
      </el-breadcrumb-item>
    </el-breadcrumb>

    <!-- 工具栏 -->
    <div class="toolbar glass">
      <el-button :icon="Refresh" circle text @click="load" />
      <el-radio-group :model-value="app.viewMode" size="small" @change="app.setViewMode">
        <el-radio-button value="list"><el-icon><Expand /></el-icon></el-radio-button>
        <el-radio-button value="grid"><el-icon><Grid /></el-icon></el-radio-button>
      </el-radio-group>
      <el-select v-model="sortKey" size="small" style="width: 110px">
        <el-option label="按名称" value="name" />
        <el-option label="按大小" value="size" />
        <el-option label="按时间" value="modified" />
      </el-select>
      <el-button size="small" text :icon="sortOrder === 'asc' ? SortUp : SortDown"
        @click="sortOrder = sortOrder === 'asc' ? 'desc' : 'asc'" />
      <div class="spacer" />
      <template v-if="selection.length">
        <span class="dim sel-info">已选 {{ selection.length }} 项</span>
        <el-button size="small" type="danger" plain :icon="Delete" @click="removeSelected">删除</el-button>
        <el-button size="small" plain :icon="Rank" @click="openMoveCopy('move')">移动</el-button>
        <el-button size="small" plain :icon="CopyDocument" @click="openMoveCopy('copy')">复制</el-button>
      </template>
      <el-button v-if="caps.write" size="small" :icon="FolderAdd" @click="mkdirVisible = true">新建目录</el-button>
      <el-button v-if="caps.upload" size="small" :icon="Link" @click="offlineVisible = true">离线下载</el-button>
      <el-button v-if="caps.upload" size="small" type="primary" :icon="Upload"
        @click="uploadVisible = true">上传</el-button>
      <el-badge :value="activeTasks" :hidden="!activeTasks" :offset="[-4, 4]">
        <el-button size="small" circle :icon="Van" title="传输任务" @click="tasksVisible = true" />
      </el-badge>
    </div>

    <!-- 列表视图 -->
    <div v-if="app.viewMode === 'list'" class="glass table-wrap">
      <el-table :data="sorted" style="width: 100%" @selection-change="selection = $event"
        @row-click="dispatch" row-class-name="row-click">
        <el-table-column type="selection" width="42" />
        <el-table-column label="名称" :min-width="isMobile ? 150 : 320">
          <template #default="{ row }">
            <span class="cell-name">
              <el-icon :size="18" :class="{ folder: row.is_dir }">
                <component :is="icons[typeIcon(row)]" />
              </el-icon>
              {{ row.name }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="大小" :width="isMobile ? 84 : 110">
          <template #default="{ row }">
            <span class="dim">{{ row.is_dir ? '-' : formatSize(row.size) }}</span>
          </template>
        </el-table-column>
        <!-- 移动端屏幕装不下时间列，藏掉 -->
        <el-table-column v-if="!isMobile" label="修改时间" width="160">
          <template #default="{ row }"><span class="dim">{{ formatTime(row.modified) }}</span></template>
        </el-table-column>
        <el-table-column :width="isMobile ? 88 : 120" align="right">
          <template #default="{ row }">
            <el-button v-if="caps.write" link size="small" :icon="EditPen"
              @click.stop="openRename(row)" />
            <el-button v-if="!row.is_dir" link size="small" :icon="Download"
              @click.stop="download(row)" />
          </template>
        </el-table-column>
      </el-table>
      <div v-if="!items.length && loaded" class="dim empty-tip">空目录</div>
    </div>

    <!-- 网格视图 -->
    <div v-else class="poster-grid">
      <div v-for="row in sorted" :key="row.name" class="g-card glass glass-hover" @click="dispatch(row)">
        <div class="g-thumb">
          <img v-if="hasThumb(row)" :src="thumbUrl(join(current, row.name), 320)"
            loading="lazy" @error="hideImg" />
          <div class="thumb-fallback abs">
            <el-icon :size="34" :class="{ folder: row.is_dir }">
              <component :is="icons[typeIcon(row)]" />
            </el-icon>
          </div>
        </div>
        <div class="g-name" :title="row.name">{{ row.name }}</div>
      </div>
      <div v-if="!items.length && loaded" class="dim empty-tip">空目录</div>
    </div>

    <!-- 对话框与抽屉 -->
    <NameDialog v-model="mkdirVisible" title="新建目录" @confirm="doMkdir" />
    <NameDialog v-model="renameVisible" title="重命名" :initial="renameTarget?.name || ''" @confirm="doRename" />
    <MoveCopyDialog v-model="mcVisible" :mode="mcMode" :paths="mcPaths" @done="load"
      @tasks="tasksVisible = true" />
    <UploadDrawer ref="uploader" v-model="uploadVisible" :dir="current" @uploaded="load" />
    <TextDrawer v-model="textVisible" :path="textPath" :kind="textKind" />
    <TasksDrawer v-model="tasksVisible" @count="onTaskCount" />

    <!-- 离线下载：URL 拉取到当前目录，进度在传输任务抽屉查看 -->
    <el-dialog v-model="offlineVisible" title="离线下载" width="480px" append-to-body class="offline-dlg">
      <el-input v-model="offlineUrls" type="textarea" :rows="5"
        placeholder="每行一个 http/https 链接" />
      <div class="dim offline-dst">将下载到：{{ current || '/' }}</div>
      <template #footer>
        <el-button @click="offlineVisible = false">取消</el-button>
        <el-button type="primary" :loading="offlineSubmitting" @click="submitOffline">开始下载</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, watch, defineAsyncComponent } from 'vue'
import { useRoute, useRouter, onBeforeRouteLeave } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import 'element-plus/es/components/message-box/style/css'
import { iconMap as icons } from '../utils/icons'
import {
  Refresh, Expand, Grid, SortUp, SortDown, Delete, Rank, CopyDocument,
  FolderAdd, Upload, UploadFilled, HomeFilled, EditPen, Download, Van, Link,
} from '@element-plus/icons-vue'
import http from '../api/http'
import { join, filesRoute, playRoute, fromParams, rawUrl, thumbUrl } from '../utils/path'
import { extType, typeIcon, formatSize, formatTime, hideImg } from '../utils/file'
import { openLightbox } from '../utils/lightbox'
import { isMobile } from '../utils/viewport'
import { useApp } from '../stores/app'
import NameDialog from '../components/NameDialog.vue'
import MoveCopyDialog from '../components/MoveCopyDialog.vue'
import UploadDrawer from '../components/UploadDrawer.vue'
import TasksDrawer from '../components/TasksDrawer.vue'

// 懒加载：TextDrawer 静态引入 highlight.js/marked/dompurify/github-markdown-css（体积不小），
// 只有用户点开文本/markdown 文件才用到，按需加载不塞进 Files 路由主 chunk
const TextDrawer = defineAsyncComponent(() => import('../components/TextDrawer.vue'))

defineOptions({ name: 'Files' }) // App.vue keep-alive include 按此名匹配

const route = useRoute()
const router = useRouter()
const app = useApp()

const items = ref([])
const caps = ref({ write: false, upload: false })
const loaded = ref(false)
const selection = ref([])
const sortKey = ref('name')
const sortOrder = ref('asc')
const dragging = ref(false)

const mkdirVisible = ref(false)
const renameVisible = ref(false)
const renameTarget = ref(null)
const mcVisible = ref(false)
const mcMode = ref('move')
const mcPaths = ref([])
const uploadVisible = ref(false)
const uploader = ref(null)
const textVisible = ref(false)
const textPath = ref('')
const textKind = ref('text')
const tasksVisible = ref(false)
const activeTasks = ref(0)
const offlineVisible = ref(false)
const offlineUrls = ref('')
const offlineSubmitting = ref(false)

function onTaskCount(n) { activeTasks.value = n }

async function submitOffline() {
  const urls = offlineUrls.value.split('\n').map((s) => s.trim()).filter(Boolean)
  if (!urls.length) return ElMessage.warning('请填写下载链接')
  offlineSubmitting.value = true
  try {
    const d = await http.post('/fs/offline', { urls, dst_dir: current.value || '/' })
    ElMessage.success(`已创建 ${d.task_ids.length} 个离线下载任务`)
    offlineVisible.value = false
    offlineUrls.value = ''
    tasksVisible.value = true // 直接打开任务抽屉看进度
  } finally {
    offlineSubmitting.value = false
  }
}

// 首次进入取一次进行中任务数；抽屉打开后由其轮询接管
http.get('/tasks').then((list) => {
  activeTasks.value = (list || []).filter((t) => t.state === 'pending' || t.state === 'running').length
}).catch(() => {})

// keep-alive 驻留期间路由切走（/play/:path 与本页共用 params.path）时冻结取值，
// 返回后目录未变就不触发 load 重刷列表
const onPage = computed(() => route.path === '/files' || route.path.startsWith('/files/'))
const current = computed((prev) => (onPage.value ? fromParams(route.params.path) : prev ?? ''))
const crumbs = computed(() => {
  const segs = current.value.split('/').filter(Boolean)
  return segs.map((name, i) => ({ name, path: '/' + segs.slice(0, i + 1).join('/') }))
})

const sorted = computed(() => {
  const arr = [...items.value]
  const k = sortKey.value
  const dir = sortOrder.value === 'asc' ? 1 : -1
  arr.sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1 // 目录恒在前
    let r = 0
    if (k === 'size') r = a.size - b.size
    else if (k === 'modified') r = a.modified < b.modified ? -1 : a.modified > b.modified ? 1 : 0
    else r = a.name.localeCompare(b.name, 'zh-CN')
    return r * dir
  })
  return arr
})

async function load() {
  loaded.value = false
  selection.value = []
  try {
    const d = await http.get('/fs/list', { params: { path: current.value } })
    items.value = d.items || []
    caps.value = { write: !!d.write, upload: !!d.upload }
  } catch {
    items.value = []
    caps.value = { write: false, upload: false }
  } finally {
    loaded.value = true
  }
}

function fullPath(row) { return join(current.value, row.name) }

function hasThumb(row) {
  if (row.is_dir) return false
  const t = extType(row.name)
  return t === 'image' || t === 'video'
}

function dispatch(row) {
  const p = fullPath(row)
  if (row.is_dir) return router.push(filesRoute(p))
  switch (extType(row.name)) {
    case 'image': {
      const imgs = sorted.value.filter((x) => !x.is_dir && extType(x.name) === 'image')
      openLightbox(imgs.map((x) => fullPath(x)), imgs.findIndex((x) => x.name === row.name))
      break
    }
    case 'video':
      router.push(playRoute(p))
      break
    case 'markdown':
      textPath.value = p; textKind.value = 'markdown'; textVisible.value = true
      break
    case 'text':
      textPath.value = p; textKind.value = 'text'; textVisible.value = true
      break
    case 'pdf':
      window.open(rawUrl(p), '_blank')
      break
    default:
      download(row)
  }
}

function download(row) {
  const a = document.createElement('a')
  a.href = rawUrl(fullPath(row), true)
  a.download = row.name
  a.click()
}

async function doMkdir(name) {
  await http.post('/fs/mkdir', { path: join(current.value, name) })
  ElMessage.success('已创建')
  load()
}

function openRename(row) {
  renameTarget.value = row
  renameVisible.value = true
}

async function doRename(name) {
  if (!renameTarget.value || name === renameTarget.value.name) return
  await http.post('/fs/rename', { path: fullPath(renameTarget.value), name })
  ElMessage.success('已重命名')
  load()
}

async function removeSelected() {
  const paths = selection.value.map(fullPath)
  await ElMessageBox.confirm(`确定删除选中的 ${paths.length} 项？此操作不可恢复。`, '删除确认',
    { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' })
  await http.post('/fs/remove', { paths })
  ElMessage.success('已删除')
  load()
}

function openMoveCopy(mode) {
  mcMode.value = mode
  mcPaths.value = selection.value.map(fullPath)
  mcVisible.value = true
}

function onDragOver(e) {
  if (caps.value.upload && e.dataTransfer?.types?.includes('Files')) dragging.value = true
}

function onDrop(e) {
  dragging.value = false
  if (!caps.value.upload) return
  const files = [...(e.dataTransfer?.files || [])]
  if (files.length) uploader.value?.addFiles(files)
}

watch(current, load, { immediate: true })

// keep-alive 驻留：TasksDrawer teleport 在 body 上，不随组件树隐藏。
// 用离开守卫（在 keep-alive 冻结组件前触发）关掉，覆盖浏览器后退等所有导航。
onBeforeRouteLeave(() => { tasksVisible.value = false })
</script>

<style scoped>
.crumbs { margin-bottom: 14px; font-size: 14px; }
.crumb { display: inline-flex; align-items: center; gap: 4px; color: var(--text-dim); }
.crumb:hover { color: var(--accent); }

.toolbar {
  display: flex; align-items: center; gap: 10px;
  padding: 10px 14px; margin-bottom: 14px;
}
.spacer { flex: 1; }
.offline-dst { font-size: 12px; margin-top: 8px; word-break: break-all; }
.sel-info { font-size: 12px; }

.table-wrap { padding: 4px 8px 8px; overflow: hidden; }
.cell-name { display: inline-flex; align-items: center; gap: 8px; cursor: pointer; }
.folder { color: #ffd479; }
:deep(.row-click) { cursor: pointer; }
.empty-tip { text-align: center; padding: 40px 0; }

/* .g-card/.g-thumb/.abs/.g-name 网格卡片样式已上提到全局 glass.css（与 Search.vue 共用） */

/* ---- 移动端 ---- */
@media (max-width: 768px) {
  /* 按钮多，放不下就换行（选中批量操作时出现第二行） */
  .toolbar { flex-wrap: wrap; gap: 8px; padding: 8px 10px; }
  .crumbs { margin-bottom: 10px; }
}

.drop-mask {
  position: fixed; inset: 16px; z-index: 200;
  display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px;
  background: rgba(20, 26, 50, 0.65);
  border: 2px dashed var(--accent);
  border-radius: var(--radius-panel);
  backdrop-filter: blur(8px);
  pointer-events: none;
  color: var(--accent);
}
</style>
