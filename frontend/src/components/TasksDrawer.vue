<template>
  <!-- title 仍要给：header 插槽只接管显示，无障碍的 aria-label 取自 title -->
  <el-drawer :model-value="modelValue" title="传输任务" size="420px" append-to-body
    @update:model-value="$emit('update:modelValue', $event)" @open="onOpen" @close="onClose">
    <!-- 网页上传走的是另一套（浏览器直传），进度在「上传队列」里；两边留个入口互相可达 -->
    <template #header>
      <div class="dh">
        <span>传输任务</span>
        <el-button size="small" link type="primary" @click="$emit('uploads')">上传队列</el-button>
      </div>
    </template>

    <div v-if="!tasks.length" class="dim empty">暂无任务</div>
    <div v-for="t in tasks" :key="t.id" class="task glass">
      <div class="t-head">
        <span class="t-name" :title="t.name">{{ t.name }}</span>
        <el-tag size="small" :type="stateTag(t.state)" effect="dark">{{ stateText(t.state) }}</el-tag>
      </div>
      <el-progress :percentage="percent(t)" :status="progressStatus(t.state)" :stroke-width="8" />
      <!-- 失败原因独占整行：与按钮挤在一行时只剩两百来像素，长路径一来原因就被省略号吃掉了 -->
      <div v-if="t.state === 'error' && t.error" class="t-err" :title="t.error">{{ t.error }}</div>
      <div class="t-foot">
        <span class="dim t-info">
          <template v-if="t.state === 'running'">
            {{ formatSize(t.done) }} / {{ t.total ? formatSize(t.total) : '…' }}
            <template v-if="t.speed"> · {{ formatSize(t.speed) }}/s</template>
            <!-- cur_file 是最早开始且仍在传的文件；并发时补上同时在传的总数 -->
            <template v-if="t.cur_file"> · {{ t.cur_file }}</template>
            <template v-if="t.active_files > 1"> 等 {{ t.active_files }} 个</template>
          </template>
        </span>
        <span class="t-actions">
          <el-button v-if="t.state === 'running' || t.state === 'pending'" link size="small"
            type="danger" :icon="CircleClose" @click="cancel(t)">取消</el-button>
          <el-button v-if="t.state === 'error' || t.state === 'canceled'" link size="small"
            type="primary" @click="retry(t)">重试</el-button>
          <el-button v-if="isTerminal(t.state)" link size="small" :icon="Delete" @click="remove(t)">删除记录</el-button>
        </span>
      </div>
    </div>
    <template #footer>
      <el-button size="small" :icon="Delete" @click="clearDone">清除已成功</el-button>
    </template>
  </el-drawer>
</template>

<script setup>
import { ref, onBeforeUnmount } from 'vue'
import { ElMessageBox } from 'element-plus'
import 'element-plus/es/components/message-box/style/css'
import { Delete, CircleClose } from '@element-plus/icons-vue'
import { api } from '../utils/api'
import { formatSize } from '../utils/file'

const props = defineProps({ modelValue: Boolean })
const emit = defineEmits(['update:modelValue', 'count', 'uploads'])

const tasks = ref([])
let timer = null

const stateText = (s) => ({
  pending: '等待中', running: '进行中', done: '已完成', error: '失败', canceled: '已取消',
}[s] || s)
const stateTag = (s) => ({
  pending: 'info', running: 'primary', done: 'success', error: 'danger', canceled: 'warning',
}[s] || 'info')
const progressStatus = (s) =>
  s === 'done' ? 'success' : s === 'error' ? 'exception' : s === 'canceled' ? 'warning' : undefined
const isTerminal = (s) => s === 'done' || s === 'error' || s === 'canceled'

function percent(t) {
  if (t.state === 'done') return 100
  if (!t.total) return 0
  return Math.min(100, Math.round((t.done / t.total) * 100))
}

async function poll() {
  try {
    tasks.value = (await api.tasks.list()) || []
    emit('count', tasks.value.filter((t) => t.state === 'pending' || t.state === 'running').length)
  } catch { /* 网络抖动忽略，下轮再取 */ }
}

function onOpen() {
  poll()
  timer = setInterval(poll, 1500)
}

function onClose() {
  if (timer) { clearInterval(timer); timer = null }
}

// 离线下载没有断点续传，取消=已下载的部分全部作废；跨存储转存取消也会丢当前这个文件的进度。
// 有实际损失的操作，必须先问一句；取消确认框本身会 reject（用户放弃），照抄项目里现成的 try/catch 兜底。
async function cancel(t) {
  try {
    await ElMessageBox.confirm('取消后正在传输的进度会丢失，需要重新开始。', '取消任务',
      { type: 'warning', confirmButtonText: '取消任务', cancelButtonText: '继续等待' })
  } catch {
    return
  }
  await api.tasks.cancel(t.id)
  poll()
}

async function retry(t) {
  await api.tasks.retry(t.id)
  poll()
}

// 删除的只是记录本身，不动已完成的文件——文案与上面「取消」区分开，避免用户误以为会连带删文件。
async function remove(t) {
  try {
    await ElMessageBox.confirm('仅删除这条任务记录，不影响已完成的文件。', '删除记录',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' })
  } catch {
    return
  }
  await api.tasks.remove(t.id)
  poll()
}

async function clearDone() {
  try {
    await ElMessageBox.confirm('仅清除已成功的任务记录，不影响已完成的文件。', '清除已成功记录',
      { type: 'warning', confirmButtonText: '清除', cancelButtonText: '取消' })
  } catch {
    return
  }
  await api.tasks.clearDone()
  poll()
}

onBeforeUnmount(onClose)
</script>

<style scoped>
.dh { display: flex; align-items: center; gap: 12px; }
.empty { text-align: center; padding: 40px 0; }
.task { padding: 12px 14px; margin-bottom: 12px; }
.t-head {
  display: flex; align-items: center; justify-content: space-between;
  gap: 8px; margin-bottom: 8px;
}
.t-name {
  font-size: 13px; flex: 1;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
/* 三行足够放下「原因 + 怎么办」；再长的（多为路径）截断，悬停看全文 */
.t-err {
  font-size: 12px; line-height: 1.5; margin-top: 6px;
  color: var(--el-color-error);
  display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 3; overflow: hidden;
}
.t-foot {
  display: flex; align-items: center; justify-content: space-between;
  margin-top: 6px; min-height: 24px;
}
.t-info {
  font-size: 12px; flex: 1;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.t-actions { flex-shrink: 0; }
</style>
