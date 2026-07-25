<template>
  <div class="index-pane">
    <div class="glass index-card">
      <h3 class="index-title">文件索引</h3>
      <template v-if="progress.running">
        <el-progress :percentage="100" :indeterminate="true" :show-text="false" :stroke-width="8" />
        <p>正在扫描：<span class="dim">{{ progress.current || '…' }}</span></p>
        <p>已索引 <b>{{ progress.scanned }}</b> 项</p>
      </template>
      <template v-else>
        <p>索引共 <b>{{ progress.scanned }}</b> 项</p>
        <p v-if="progress.err" class="err">上次重建出错：{{ progress.err }}</p>
        <p class="dim">重建会全量扫描所有存储，供搜索与媒体库使用。日常写操作会自动增量更新。</p>
      </template>
      <el-button type="primary" :disabled="progress.running" :icon="RefreshRight"
        @click="rebuild">重建索引</el-button>
    </div>

    <div class="glass index-card">
      <h3 class="index-title">封面与源信息预载</h3>
      <template v-if="preload.snoozed">
        <p v-if="preload.running">已推迟，正在停下手头的几项…</p>
        <p v-else>{{ snoozeNote }}</p>
        <p>封面 <b>{{ preload.covers }}</b> · 视频源信息 <b>{{ preload.probes }}</b> 已缓存<template
            v-if="preload.pending">，还剩 {{ preload.pending }} 项</template></p>
        <p class="dim">推迟期间不会在后台预载，浏览到的封面与源信息改为当场加载。</p>
      </template>
      <template v-else-if="preload.running">
        <el-progress :percentage="preloadPct" :stroke-width="8" />
        <p>正在预载：<span class="dim">{{ preload.current || '…' }}</span></p>
        <p>已处理 <b>{{ preload.done }}</b> / {{ preload.total }} 项
          （封面 {{ preload.covers }} · 源信息 {{ preload.probes }}）</p>
      </template>
      <template v-else>
        <p>封面 <b>{{ preload.covers }}</b> · 视频源信息 <b>{{ preload.probes }}</b> 已缓存</p>
        <p v-if="preload.err" class="err">上次预载出错：{{ preload.err }}</p>
        <p class="dim">挂载云盘并勾选「在视频库/照片墙展示」后，会自动在后台加载封面与视频源信息并缓存，之后浏览即刻呈现、播放免现场探测。</p>
      </template>
      <div class="index-actions">
        <el-button v-if="preload.snoozed" type="primary" :icon="VideoPlay"
          @click="resumePreload">继续</el-button>
        <template v-else>
          <el-button :disabled="preload.running" :icon="RefreshRight" @click="runPreload">重新预载</el-button>
          <el-button v-if="preload.running" :icon="Clock" @click="snoozePreload">不是现在</el-button>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { ElMessage } from 'element-plus'
import { Clock, RefreshRight, VideoPlay } from '@element-plus/icons-vue'
import { api } from '../../utils/api'

// active：本 pane 是否为当前选中 Tab。切到索引管理时刷新进度（原 Admin.vue 的 watch(tab)），
// 好在别的 Tab 触发重建（如添加存储）后切回来能看到运行中的进度。
const props = defineProps({ active: { type: Boolean, default: false } })

const progress = ref({ running: false, scanned: 0, current: '', err: '' })
const preload = ref({
  running: false, total: 0, done: 0, covers: 0, probes: 0, current: '', err: '',
  snoozed: false, resume_at: '', pending: 0,
})
const preloadPct = computed(() =>
  preload.value.total > 0 ? Math.round((preload.value.done / preload.value.total) * 100) : 0)
// 推迟到点：一天后基本都落在「明天 HH:MM」，跨重启恢复时也可能是今天
const snoozeNote = computed(() => {
  const at = fmtTime(preload.value.resume_at)
  return at ? `已推迟，${at}自动继续。` : '已推迟。'
})
let pollTimer = null

function fmtTime(iso) {
  const t = new Date(iso || '')
  if (isNaN(t.getTime())) return ''
  const midnight = (x) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const days = Math.round((midnight(t) - midnight(new Date())) / 86400000)
  const day = days <= 0 ? '' : days === 1 ? '明天 ' : `${t.getMonth() + 1}月${t.getDate()}日 `
  return `${day}${t.getHours()}:${String(t.getMinutes()).padStart(2, '0')} `
}

async function loadProgress() {
  try {
    const [idx, pre] = await Promise.all([
      api.admin.index.progress(),
      api.admin.preload.progress(),
    ])
    progress.value = idx
    preload.value = pre
    const busy = idx.running || pre.running
    if (busy && !pollTimer) {
      pollTimer = setInterval(loadProgress, 1500)
    } else if (!busy && pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  } catch (e) {
    console.error(e)
  }
}
async function rebuild() {
  await api.admin.index.rebuild()
  ElMessage.success('已开始重建')
  loadProgress()
}
async function runPreload() {
  await api.admin.preload.run()
  ElMessage.success('已开始预载封面与源信息')
  loadProgress()
}
async function snoozePreload() {
  const p = await api.admin.preload.snooze()
  preload.value = p
  const at = fmtTime(p.resume_at)
  ElMessage.success(at ? `已推迟，${at}自动继续` : '已推迟')
  loadProgress()
}
async function resumePreload() {
  preload.value = await api.admin.preload.resume()
  ElMessage.success('已继续预载')
  loadProgress()
}

watch(() => props.active, (a) => { if (a) loadProgress() })

onMounted(loadProgress)
onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<style scoped>
.index-pane { display: flex; gap: 16px; flex-wrap: wrap; }
.index-card {
  padding: 24px; min-width: 380px; flex: 1 1 380px; max-width: 460px;
  display: flex; flex-direction: column; gap: 10px; align-items: flex-start;
}
.index-card p { margin: 0; font-size: 14px; }
.index-title { margin: 0 0 4px; font-size: 15px; font-weight: 600; }
.index-card .el-progress { width: 100%; }
.index-actions { display: flex; gap: 8px; }
.index-actions .el-button + .el-button { margin-left: 0; }
.err { color: var(--el-color-error); }

@media (max-width: 768px) {
  .index-card { min-width: 0; flex-basis: 100%; max-width: none; padding: 18px; }
}
</style>
