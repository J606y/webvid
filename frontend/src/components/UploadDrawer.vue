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

    <!-- 落区是整个抽屉体，不止那个框：只认那个框的话稍微拖偏一点就落空，
         而抽屉一旦打开，落在它任何位置的文件拖放意图都是明确的。框只作视觉锚点。 -->
    <div class="dz" @dragenter="onDragEnter" @dragover="onDragOver"
      @dragleave="onDragLeave" @drop="onDrop">
      <div class="pick glass" :class="{ over }" @click="pick">
        <el-icon :size="28"><UploadFilled /></el-icon>
        <span v-if="over">松开以上传到 {{ dir || '/' }}</span>
        <span v-else>点击选择文件，或将文件拖到这里</span>
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
    </div>
  </el-drawer>
</template>

<script setup>
import { ref, computed, reactive, watch, onBeforeUnmount } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import 'element-plus/es/components/message-box/style/css'
import { UploadFilled, Close } from '@element-plus/icons-vue'
import { api } from '../utils/api'
import { join } from '../utils/path'
import { formatSize } from '../utils/file'
import { useApp } from '../stores/app'

const props = defineProps({
  modelValue: Boolean,
  dir: { type: String, required: true }, // 目标目录逻辑路径
  // 目标目录所在存储是否支持上传。只读存储上拖进来的文件只会换回一串失败任务，
  // 不如在入队前就拦住并说清楚。缺省 true：不传的调用方维持原行为。
  canUpload: { type: Boolean, default: true },
})
const emit = defineEmits(['update:modelValue', 'uploaded', 'count', 'tasks'])

const app = useApp()
const fileInput = ref(null)
const tasks = ref([])
let nextId = 1
let active = 0
const controllers = new Map() // 任务 id → AbortController（非响应式，供移除时中断在途上传）

function pick() { fileInput.value?.click() }

// ---- 拖放落区 ----
// 这个框是唯一的落区（整页落区已移除）。系统（桌面/资源管理器/访达）拖进来的文件能不能
// 落下，取决于三件事，缺一不可：
//   1. dragenter 与 dragover 都要 preventDefault。只挡 dragover 时，进入元素的那一刻
//      仍按「不接受放置」处理，光标是禁止符号，drop 不会触发。
//   2. dropEffect 必须显式设成 'copy'。系统拖出的 effectAllowed 通常是 copyMove/all，
//      不指定时浏览器可能解析成 move 或 none——显示禁止符号，drop 同样不触发。
//   3. stopPropagation。页面在 window 上把落区之外的文件拖放标成 dropEffect='none' 吞掉
//      （防止浏览器把文件当导航打开），事件冒上去会把这里设好的 'copy' 覆盖掉。
const over = ref(false)
let beat = 0

function isFileDrag(e) {
  return !!e.dataTransfer && Array.prototype.includes.call(e.dataTransfer.types || [], 'Files')
}

// 高亮靠 dragover 心跳维持，不靠 dragleave：拖着文件离开窗口时浏览器不保证补发 dragleave，
// 只认 dragleave 的话高亮会一直亮着。dragover 在悬停期间持续触发，停 400ms 即判定已离开。
function endOver() {
  clearTimeout(beat)
  beat = 0
  over.value = false
}

function accept(e) {
  e.preventDefault()
  e.stopPropagation()
  e.dataTransfer.dropEffect = 'copy'
  over.value = true
  clearTimeout(beat)
  beat = setTimeout(endOver, 400)
}

function onDragEnter(e) { if (isFileDrag(e)) accept(e) }
function onDragOver(e) { if (isFileDrag(e)) accept(e) }
function onDragLeave(e) {
  // 框内子元素之间移动也会触发 dragleave；relatedTarget 仍在框内就不算离开
  if (!e.currentTarget.contains(e.relatedTarget)) endOver()
}

// 拆出文件与目录：拖进来的文件夹在 DataTransfer 里同样算一个 File（大小 0、无类型），
// 直接传上去只会在网盘里得到一个空文件。按 webkitGetAsEntry 判定剔除，并明确告知。
// 用 items 而非 files 逐个取：items 里除文件外还可能有 text/uri-list 之类的字符串项，
// 两个列表的下标并不对齐，只能按 kind === 'file' 自己走一遍。
function splitEntries(dt) {
  const files = []
  const dirs = []
  for (const it of [...(dt.items || [])]) {
    if (it.kind !== 'file') continue
    const entry = it.webkitGetAsEntry?.()
    const f = it.getAsFile()
    if (!f) continue
    if (entry?.isDirectory) dirs.push(f.name)
    else files.push(f)
  }
  // items 不可用时退回 files（此时无法识别目录，交由后端拒绝）
  if (!files.length && !dirs.length) files.push(...(dt.files || []))
  return { files, dirs }
}

async function onDrop(e) {
  if (!isFileDrag(e)) return
  e.preventDefault()
  e.stopPropagation()
  endOver()
  // 必须在 await 之前把 File 取出来：DataTransfer 在事件回调结束后即失效，
  // 等用户点完确认框再来读就已经是空的了。File 本身是持久引用，不受影响。
  const { files, dirs } = splitEntries(e.dataTransfer)
  if (dirs.length) ElMessage.warning(`网页上传不支持文件夹，已跳过 ${dirs.length} 项`)
  if (!files.length) return
  try {
    await ElMessageBox.confirm(`将上传到 ${props.dir || '/'}`,
      files.length === 1 ? `上传「${files[0].name}」？` : `上传 ${files.length} 个文件？`,
      { confirmButtonText: '上传', cancelButtonText: '取消' })
  } catch {
    return // 取消或关闭
  }
  addFiles(files)
}

onBeforeUnmount(endOver)

function onPick(e) {
  addFiles([...e.target.files])
  e.target.value = ''
}

// 供父组件（拖拽落区）调用。
// 权限在这里把关而不是在各入口：拖放、点击选择、父组件直接调用三条路都汇到这儿，
// 挡一次就全挡住了 —— 早先只有窗口级拖放做了这道检查，另外两条一直是漏的。
function addFiles(files) {
  if (!props.canUpload) {
    ElMessage.warning('当前目录所在存储不支持上传')
    return
  }
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
/* 落区铺满抽屉体：否则它只有内容那么高，队列为空时下方一大片空白拖上去落不到 */
.dz { min-height: 100%; }
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
/* 拖拽悬停：比 hover 再明确一档——实线边加淡色底，表示「松手就落在这」 */
.pick.over {
  color: var(--accent);
  border-style: solid;
  border-color: var(--accent);
  background: rgba(var(--accent-rgb), 0.12);
}
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
