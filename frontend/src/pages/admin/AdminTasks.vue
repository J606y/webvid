<template>
  <div>
    <div class="pane-head">
      <span class="dim head-stat">{{ headStat }}</span>
      <el-button size="small" :icon="Delete" @click="clearDone">清除已成功</el-button>
    </div>

    <el-table :data="tasks" class="task-table" :empty-text="'暂无任务'">
      <el-table-column label="任务" min-width="200">
        <template #default="{ row }">
          <div class="t-name" :title="row.name">{{ row.name }}</div>
          <div v-if="row.error" class="t-err" :title="row.error">{{ row.error }}</div>
          <div v-else-if="row.cur_file" class="dim t-sub" :title="row.cur_file">
            {{ row.cur_file }}<template v-if="row.active_files > 1"> 等 {{ row.active_files }} 个</template>
          </div>
        </template>
      </el-table-column>
      <el-table-column v-if="!isMobile" label="发起人" width="100">
        <template #default="{ row }">{{ userName(row.owner) }}</template>
      </el-table-column>
      <el-table-column label="状态" :width="isMobile ? 72 : 90">
        <template #default="{ row }">
          <el-tag :type="stateTag(row.state)" size="small">{{ stateText(row.state) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="进度" :min-width="isMobile ? 120 : 180">
        <template #default="{ row }">
          <el-progress :percentage="percent(row)" :status="progressStatus(row.state)"
            :stroke-width="6" :show-text="false" />
          <div class="dim t-sub">
            {{ formatSize(row.done) }} / {{ row.total ? formatSize(row.total) : '…' }}
            <template v-if="row.state === 'running' && row.speed"> · {{ formatSize(row.speed) }}/s</template>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="文件" :width="isMobile ? 78 : 110">
        <template #default="{ row }">
          <template v-if="row.files.total">
            <span>{{ row.files.done + row.files.skipped }} / {{ row.files.total }}</span>
            <div v-if="row.files.error" class="t-err">失败 {{ row.files.error }}</div>
          </template>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
      <el-table-column :width="isMobile ? 92 : 190" align="right">
        <template #default="{ row }">
          <el-button v-if="row.files.total" link size="small" type="primary"
            :icon="Files" @click="openFiles(row)">{{ isMobile ? '' : '查看文件' }}</el-button>
          <el-button v-if="row.state === 'running' || row.state === 'pending'" link size="small"
            type="danger" :icon="CircleClose" @click="cancel(row)">{{ isMobile ? '' : '取消' }}</el-button>
          <el-button v-if="row.state === 'error' || row.state === 'canceled'" link size="small"
            type="primary" :icon="RefreshRight" @click="retry(row)">{{ isMobile ? '' : '重试' }}</el-button>
          <el-button v-if="isTerminal(row.state)" link size="small" :icon="Delete"
            @click="remove(row)">{{ isMobile ? '' : '删除记录' }}</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 文件清单：主页抽屉只给一个「当前文件」，这里给整个文件夹的逐文件状态 -->
    <el-drawer v-model="filesDlg" :size="isMobile ? '100%' : '760px'" append-to-body
      :title="fileTask?.name || '文件清单'" @closed="onFilesClosed">
      <div class="f-bar">
        <el-input v-model="fileQuery" size="small" clearable placeholder="搜索文件名或子目录"
          :prefix-icon="Search" class="f-search" @input="onFilterChange" />
        <el-select v-model="fileState" size="small" class="f-state" @change="onFilterChange">
          <el-option v-for="o in stateOptions" :key="o.value" :label="o.label" :value="o.value" />
        </el-select>
      </div>
      <div class="dim f-counts">{{ countsText }}</div>

      <el-table :data="files" size="small" :empty-text="filesEmptyText" class="f-table">
        <el-table-column label="文件" min-width="220">
          <template #default="{ row }">
            <div class="f-path" :title="row.path">{{ row.path }}</div>
            <div v-if="row.error" class="t-err" :title="row.error">{{ row.error }}</div>
          </template>
        </el-table-column>
        <el-table-column v-if="!isMobile" label="大小" width="100" align="right">
          <template #default="{ row }">{{ row.size ? formatSize(row.size) : '—' }}</template>
        </el-table-column>
        <el-table-column label="状态" :width="isMobile ? 96 : 160">
          <template #default="{ row }">
            <template v-if="row.state === 'running'">
              <el-progress :percentage="filePercent(row)" :stroke-width="6" :show-text="false" />
              <div class="dim t-sub">传输中 {{ filePercent(row) }}%</div>
            </template>
            <el-tag v-else :type="fileTag(row.state)" size="small">{{ fileText(row.state) }}</el-tag>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination v-if="fileTotal > pageSize" class="f-page" layout="prev, pager, next"
        small background :total="fileTotal" :page-size="pageSize" :current-page="page"
        @current-change="goPage" />
    </el-drawer>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import 'element-plus/es/components/message-box/style/css'
import { Delete, CircleClose, RefreshRight, Files, Search } from '@element-plus/icons-vue'
import { api } from '../../utils/api'
import { formatSize } from '../../utils/file'
import { isMobile } from '../../utils/viewport'

// active：本 pane 是否为当前选中 Tab。只在选中时轮询，切走就停——后台页开着不该一直打接口。
const props = defineProps({ active: { type: Boolean, default: false } })

const tasks = ref([])
const users = ref([])
let pollTimer = null

const stateText = (s) => ({
  pending: '等待中', running: '进行中', done: '已完成', error: '失败', canceled: '已取消',
}[s] || s)
const stateTag = (s) => ({
  pending: 'info', running: 'primary', done: 'success', error: 'danger', canceled: 'warning',
}[s] || 'info')
const progressStatus = (s) =>
  s === 'done' ? 'success' : s === 'error' ? 'exception' : s === 'canceled' ? 'warning' : undefined
const isTerminal = (s) => s === 'done' || s === 'error' || s === 'canceled'

const headStat = computed(() => {
  const running = tasks.value.filter((t) => t.state === 'running').length
  const pending = tasks.value.filter((t) => t.state === 'pending').length
  if (!tasks.value.length) return ''
  return `共 ${tasks.value.length} 个任务 · 进行中 ${running} · 等待中 ${pending}`
})

function userName(id) {
  return users.value.find((u) => u.id === id)?.username || `#${id}`
}
function percent(t) {
  if (t.state === 'done') return 100
  if (!t.total) return 0
  return Math.min(100, Math.round((t.done / t.total) * 100))
}

async function loadTasks() {
  try {
    tasks.value = (await api.tasks.list()) || []
  } catch (e) {
    console.error(e)
  }
}
// 发起人只在后台展示，用户列表变动少，进 pane 取一次即可
async function loadUsers() {
  try {
    users.value = (await api.admin.users.list()) || []
  } catch (e) {
    console.error(e)
  }
}

async function cancel(t) {
  try {
    await ElMessageBox.confirm('取消后正在传输的进度会丢失，需要重新开始。', '取消任务',
      { type: 'warning', confirmButtonText: '取消任务', cancelButtonText: '继续等待' })
  } catch {
    return
  }
  await api.tasks.cancel(t.id)
  loadTasks()
}
async function retry(t) {
  await api.tasks.retry(t.id)
  ElMessage.success('已重新开始')
  loadTasks()
}
async function remove(t) {
  try {
    await ElMessageBox.confirm('仅删除这条任务记录，不影响已完成的文件。', '删除记录',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' })
  } catch {
    return
  }
  await api.tasks.remove(t.id)
  if (fileTask.value?.id === t.id) filesDlg.value = false
  loadTasks()
}
async function clearDone() {
  try {
    await ElMessageBox.confirm('仅清除已成功的任务记录，不影响已完成的文件。', '清除已成功记录',
      { type: 'warning', confirmButtonText: '清除', cancelButtonText: '取消' })
  } catch {
    return
  }
  await api.tasks.clearDone()
  loadTasks()
}

// ---- 文件清单 ----
const filesDlg = ref(false)
const fileTask = ref(null)
const files = ref([])
const fileTotal = ref(0)
const fileCounts = ref({ total: 0, pending: 0, running: 0, done: 0, skipped: 0, error: 0 })
const fileQuery = ref('')
// 'all' 而不是空串：el-select 把空串当「没选」，控件会掉回灰色的占位文字
const fileState = ref('all')
const page = ref(1)
const pageSize = 100
let filesTimer = null
let filterTimer = null

const stateOptions = [
  { label: '全部', value: 'all' },
  { label: '等待', value: 'pending' },
  { label: '传输中', value: 'running' },
  { label: '已完成', value: 'done' },
  { label: '已跳过', value: 'skipped' },
  { label: '失败', value: 'error' },
]
const fileText = (s) => ({
  pending: '等待', running: '传输中', done: '已完成', skipped: '已跳过', error: '失败',
}[s] || s)
const fileTag = (s) => ({
  pending: 'info', running: 'primary', done: 'success', skipped: 'info', error: 'danger',
}[s] || 'info')
const filePercent = (f) =>
  f.size > 0 ? Math.min(100, Math.round((f.done / f.size) * 100)) : 0

const countsText = computed(() => {
  const c = fileCounts.value
  const parts = [`共 ${c.total} 个文件`]
  if (c.pending) parts.push(`等待 ${c.pending}`)
  if (c.running) parts.push(`传输中 ${c.running}`)
  if (c.done) parts.push(`已完成 ${c.done}`)
  if (c.skipped) parts.push(`已跳过 ${c.skipped}`)
  if (c.error) parts.push(`失败 ${c.error}`)
  return parts.join(' · ')
})
const filesEmptyText = computed(() =>
  fileQuery.value || fileState.value !== 'all' ? '没有符合条件的文件' : '暂无文件')

function openFiles(row) {
  fileTask.value = row
  fileQuery.value = ''
  fileState.value = 'all'
  page.value = 1
  files.value = []
  filesDlg.value = true
  loadFiles()
  filesTimer = setInterval(loadFiles, 1500)
}
async function loadFiles() {
  const t = fileTask.value
  if (!t) return
  try {
    const d = await api.tasks.files(t.id, {
      state: fileState.value === 'all' ? undefined : fileState.value,
      q: fileQuery.value || undefined,
      offset: (page.value - 1) * pageSize,
      limit: pageSize,
    })
    files.value = d.items || []
    fileTotal.value = d.total
    fileCounts.value = d.counts
    // 任务本身的状态也跟着刷新（抽屉标题下的进度、按钮可用性都取自它）
    const fresh = tasks.value.find((x) => x.id === t.id)
    if (fresh) fileTask.value = fresh
    if (isTerminal(fresh?.state || t.state) && !fileCounts.value.running) stopFilesPoll()
  } catch (e) {
    console.error(e)
    stopFilesPoll() // 任务记录被删/无权限，继续轮询只会刷屏报错
  }
}
// 输入即查会把每个按键都打成一次请求，等手停下来再发
function onFilterChange() {
  page.value = 1
  clearTimeout(filterTimer)
  filterTimer = setTimeout(loadFiles, 250)
}
function goPage(p) {
  page.value = p
  loadFiles()
}
function stopFilesPoll() {
  if (filesTimer) { clearInterval(filesTimer); filesTimer = null }
}
function onFilesClosed() {
  stopFilesPoll()
  clearTimeout(filterTimer)
  fileTask.value = null
  files.value = []
}

function startPoll() {
  loadTasks()
  if (!pollTimer) pollTimer = setInterval(loadTasks, 1500)
}
function stopPoll() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
}

watch(() => props.active, (a) => (a ? startPoll() : stopPoll()))

onMounted(() => {
  loadUsers()
  if (props.active) startPoll()
})
onBeforeUnmount(() => {
  stopPoll()
  stopFilesPoll()
  clearTimeout(filterTimer)
})
</script>

<style scoped>
.pane-head {
  display: flex; align-items: center; justify-content: space-between;
  gap: 12px; margin-bottom: 10px;
}
.head-stat { font-size: 12px; }
.t-name, .f-path { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.t-sub, .t-err {
  font-size: 12px; line-height: 1.5;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.t-err { color: var(--el-color-error); }

.f-bar { display: flex; gap: 8px; }
.f-search { flex: 1; }
.f-state { width: 108px; flex-shrink: 0; }
.f-counts { font-size: 12px; margin: 8px 0 4px; }
.f-page { margin-top: 12px; justify-content: center; }

@media (max-width: 768px) {
  :deep(.el-table .cell) { padding: 0 8px; }
  :deep(.el-table td .el-button + .el-button) { margin-left: 2px; }
}
</style>
