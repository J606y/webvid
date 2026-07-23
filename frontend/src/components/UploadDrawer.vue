<template>
  <el-drawer :model-value="modelValue" title="上传队列" size="420px" :close-on-click-modal="false"
    @update:model-value="$emit('update:modelValue', $event)">
    <div class="pick glass" @click="pick">
      <el-icon :size="28"><UploadFilled /></el-icon>
      <span>点击选择文件，或将文件拖拽到页面任意处</span>
    </div>
    <input ref="fileInput" type="file" multiple hidden @change="onPick" />

    <div v-for="t in tasks" :key="t.id" class="task glass">
      <div class="row">
        <span class="name" :title="t.file.name">{{ t.file.name }}</span>
        <span class="dim size">{{ formatSize(t.file.size) }}</span>
        <el-icon class="rm" title="移除" @click="removeTask(t)"><Close /></el-icon>
      </div>
      <el-progress :percentage="t.percent" :status="statusOf(t)" :stroke-width="6" />
      <div class="row">
        <span class="dim state">{{ stateText(t) }}</span>
        <el-button v-if="t.state === 'error'" size="small" link type="primary" @click="retry(t)">
          重试
        </el-button>
        <el-button v-if="t.state === 'conflict'" size="small" link type="warning" @click="overwrite(t)">
          覆盖上传
        </el-button>
      </div>
    </div>
  </el-drawer>
</template>

<script setup>
import { ref, reactive } from 'vue'
import { UploadFilled, Close } from '@element-plus/icons-vue'
import { api } from '../utils/api'
import { join } from '../utils/path'
import { formatSize } from '../utils/file'
import { useApp } from '../stores/app'

const props = defineProps({
  modelValue: Boolean,
  dir: { type: String, required: true }, // 目标目录逻辑路径
})
const emit = defineEmits(['update:modelValue', 'uploaded'])

const app = useApp()
const fileInput = ref(null)
const tasks = ref([])
let nextId = 1
let active = 0
const controllers = new Map() // 任务 id → AbortController（非响应式，供移除时中断在途上传）

function pick() { fileInput.value?.click() }

function onPick(e) {
  addFiles([...e.target.files])
  e.target.value = ''
}

// 供父组件（拖拽落区）调用
function addFiles(files) {
  for (const f of files) {
    tasks.value.unshift(reactive({
      id: nextId++, file: f, dir: props.dir,
      percent: 0, state: 'pending', overwrite: false, error: '',
    }))
  }
  emit('update:modelValue', true)
  pump()
}

function pump() {
  // 并发数来自后台「任务设置」的上传并发（/public/settings 下发）
  while (active < (app.uploadWorkers || 2)) {
    const t = tasks.value.find((x) => x.state === 'pending')
    if (!t) break
    run(t)
  }
}

async function run(t) {
  active++
  t.state = 'uploading'
  const ctrl = new AbortController()
  controllers.set(t.id, ctrl)
  try {
    const target = join(t.dir, t.file.name)
    await api.fs.upload(target, t.overwrite, t.file, {
      headers: { 'Content-Type': 'application/octet-stream' },
      timeout: 0,
      signal: ctrl.signal, // 移除任务时中断在途请求
      silent: true, // 队列内每任务行内展示状态与重试/覆盖，无需全局 toast
      onUploadProgress: (ev) => {
        if (ev.total) t.percent = Math.round((ev.loaded / ev.total) * 100)
      },
    })
    t.percent = 100
    t.state = 'done'
    emit('uploaded')
  } catch (e) {
    if (ctrl.signal.aborted) return // 用户已移除该任务，忽略中断异常（finally 仍释放并发槽）
    if (e.status === 409) { // 后端 fsError：同名冲突（driver.ErrExist → 409）
      t.state = 'conflict'
      t.error = '同名文件已存在'
    } else {
      t.state = 'error'
      t.error = e.message || '上传失败'
    }
  } finally {
    controllers.delete(t.id)
    active--
    pump()
  }
}

// 从队列移除任务：正在上传的中断请求，其余直接删；空出并发槽由 pump 补位。
function removeTask(t) {
  controllers.get(t.id)?.abort()
  tasks.value = tasks.value.filter((x) => x.id !== t.id)
}

function retry(t) {
  t.percent = 0
  t.state = 'pending'
  pump()
}

function overwrite(t) {
  t.overwrite = true
  retry(t)
}

function statusOf(t) {
  if (t.state === 'done') return 'success'
  if (t.state === 'error' || t.state === 'conflict') return 'exception'
  return ''
}

function stateText(t) {
  switch (t.state) {
    case 'pending': return '排队中'
    case 'uploading': return `上传中 ${t.percent}%`
    case 'done': return '完成'
    default: return t.error
  }
}

defineExpose({ addFiles })
</script>

<style scoped>
.pick {
  display: flex; flex-direction: column; align-items: center; gap: 8px;
  padding: 26px 12px; margin-bottom: 14px;
  border: 1px dashed var(--glass-border); cursor: pointer;
  font-size: 13px; color: var(--text-dim); text-align: center;
}
.pick:hover { color: var(--text-main); border-color: var(--accent); }
.task { padding: 10px 12px; margin-bottom: 10px; }
.row { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.name {
  font-size: 13px; flex: 1;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.size, .state { font-size: 12px; }
.rm {
  flex-shrink: 0; cursor: pointer; color: var(--text-dim);
  border-radius: 6px; padding: 2px; transition: color 0.15s, background 0.15s;
}
.rm:hover { color: #f56c6c; background: rgba(245, 108, 108, 0.12); }
</style>
