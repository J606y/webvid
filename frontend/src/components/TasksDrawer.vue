<template>
  <el-drawer :model-value="modelValue" title="传输任务" size="420px" append-to-body
    @update:model-value="$emit('update:modelValue', $event)" @open="onOpen" @close="onClose">
    <div v-if="!tasks.length" class="dim empty">暂无任务</div>
    <div v-for="t in tasks" :key="t.id" class="task glass">
      <div class="t-head">
        <span class="t-name" :title="t.name">{{ t.name }}</span>
        <el-tag size="small" :type="stateTag(t.state)" effect="dark">{{ stateText(t.state) }}</el-tag>
      </div>
      <el-progress :percentage="percent(t)" :status="progressStatus(t.state)" :stroke-width="8" />
      <div class="t-foot">
        <span class="dim t-info">
          <template v-if="t.state === 'running'">
            {{ formatSize(t.done) }} / {{ t.total ? formatSize(t.total) : '…' }}
            <template v-if="t.speed"> · {{ formatSize(t.speed) }}/s</template>
            <template v-if="t.cur_file"> · {{ t.cur_file }}</template>
          </template>
          <template v-else-if="t.state === 'error'">{{ t.error }}</template>
        </span>
        <span class="t-actions">
          <el-button v-if="t.state === 'running' || t.state === 'pending'" link size="small"
            @click="cancel(t)">取消</el-button>
          <el-button v-if="t.state === 'error' || t.state === 'canceled'" link size="small"
            type="primary" @click="retry(t)">重试</el-button>
          <el-button v-if="isTerminal(t.state)" link size="small" @click="remove(t)">删除</el-button>
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
import { Delete } from '@element-plus/icons-vue'
import http from '../api/http'
import { formatSize } from '../utils/file'

const props = defineProps({ modelValue: Boolean })
const emit = defineEmits(['update:modelValue', 'count'])

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
    tasks.value = (await http.get('/tasks')) || []
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

async function cancel(t) {
  await http.post(`/tasks/${t.id}/cancel`)
  poll()
}

async function retry(t) {
  await http.post(`/tasks/${t.id}/retry`)
  poll()
}

async function remove(t) {
  await http.post(`/tasks/${t.id}/remove`)
  poll()
}

async function clearDone() {
  await http.delete('/tasks/done')
  poll()
}

onBeforeUnmount(onClose)
</script>

<style scoped>
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
