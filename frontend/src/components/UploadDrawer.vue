<template>
  <!-- title 仍要给：header 插槽只接管显示，无障碍的 aria-label 取自 title -->
  <el-drawer :model-value="modelValue" title="上传队列" size="420px" :close-on-click-modal="false"
    @update:model-value="$emit('update:modelValue', $event)">
    <!-- 上传与后台传输是两套机制（浏览器直传 vs 服务端拉流），但对用户是一件事，
         两个抽屉之间留一个入口互相可达 -->
    <template #header>
      <div class="dh">
        <span>上传队列</span>
        <el-button size="small" link type="primary" @click="$emit('tasks')">传输任务</el-button>
      </div>
    </template>

    <div class="pick glass" @click="pick">
      <el-icon :size="28"><UploadFilled /></el-icon>
      <span>点击选择文件，或将文件拖拽到页面任意处</span>
    </div>
    <input ref="fileInput" type="file" multiple hidden @change="onPick" />

    <!-- 多个文件同时撞名时给一次性处理，免得逐个点 -->
    <div v-if="conflicts.length > 1" class="bulk glass">
      <span class="dim">{{ conflicts.length }} 个文件同名</span>
      <div class="spacer" />
      <el-button size="small" link type="warning" @click="resolveAll('overwrite')">全部覆盖</el-button>
      <el-button size="small" link type="primary" @click="resolveAll('keepBoth')">全部保留两者</el-button>
      <el-button size="small" link @click="skipAll">全部跳过</el-button>
    </div>

    <div v-for="t in tasks" :key="t.id" class="task glass">
      <div class="row">
        <span class="name" :title="targetName(t)">{{ targetName(t) }}</span>
        <span class="dim size">{{ formatSize(t.file.size) }}</span>
        <el-icon class="rm" title="移除" @click="removeTask(t)"><Close /></el-icon>
      </div>
      <el-progress :percentage="t.percent" :status="statusOf(t)" :stroke-width="6" />
      <div class="row">
        <span class="dim state">{{ stateText(t) }}</span>
        <el-button v-if="t.state === 'error'" size="small" link type="primary" @click="retry(t)">
          重试
        </el-button>
        <template v-if="t.state === 'conflict'">
          <el-button size="small" link type="warning" @click="resolve(t, 'overwrite')">覆盖</el-button>
          <el-button size="small" link type="primary" @click="resolve(t, 'keepBoth')">保留两者</el-button>
          <el-button size="small" link @click="removeTask(t)">跳过</el-button>
        </template>
      </div>
    </div>
  </el-drawer>
</template>

<script setup>
import { ref, computed, reactive, watch } from 'vue'
import { UploadFilled, Close } from '@element-plus/icons-vue'
import { api } from '../utils/api'
import { join } from '../utils/path'
import { formatSize } from '../utils/file'
import { useApp } from '../stores/app'

const props = defineProps({
  modelValue: Boolean,
  dir: { type: String, required: true }, // 目标目录逻辑路径
})
const emit = defineEmits(['update:modelValue', 'uploaded', 'count', 'tasks'])

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
      // mode 撞名后的处置：'' 未定 | 'overwrite' 覆盖 | 'keepBoth' 保留两者
      // seq 「保留两者」的序号，0 = 用原名
      percent: 0, state: 'pending', overwrite: false, mode: '', seq: 0, error: '',
    }))
  }
  emit('update:modelValue', true)
  pump()
}

// targetName 实际上传用的文件名。「保留两者」在扩展名前加序号，同 macOS 访达的做法。
function targetName(t) {
  if (!t.seq) return t.file.name
  const i = t.file.name.lastIndexOf('.')
  const base = i > 0 ? t.file.name.slice(0, i) : t.file.name
  const ext = i > 0 ? t.file.name.slice(i) : ''
  return `${base} (${t.seq})${ext}`
}

const conflicts = computed(() => tasks.value.filter((t) => t.state === 'conflict'))

// 进行中的上传数上报给页面，与后台传输任务合并成一个徽标：
// 上传只在这个抽屉里有进度，抽屉一关全局就再没有任何指示。
watch(
  () => tasks.value.filter((t) => t.state === 'pending' || t.state === 'uploading').length,
  (n) => emit('count', n),
  { immediate: true },
)

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
    const target = join(t.dir, targetName(t))
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
      // 已选「保留两者」就自动往下试序号，不必每撞一次名就回来点一次
      if (t.mode === 'keepBoth' && t.seq < 50) {
        t.seq++
        t.percent = 0
        t.state = 'pending'
      } else {
        t.state = 'conflict'
        t.error = '同名文件已存在'
      }
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

// resolve 处置一个撞名任务：覆盖原文件，或改名保留两者。
function resolve(t, mode) {
  t.mode = mode
  t.overwrite = mode === 'overwrite'
  if (mode === 'keepBoth') t.seq = 1
  retry(t)
}

function resolveAll(mode) {
  for (const t of conflicts.value) resolve(t, mode)
}

function skipAll() {
  for (const t of conflicts.value) removeTask(t)
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
.dh { display: flex; align-items: center; gap: 12px; }
.bulk {
  display: flex; align-items: center; gap: 6px;
  padding: 8px 12px; margin-bottom: 10px; font-size: 12px;
}
.bulk .spacer { flex: 1; }
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
