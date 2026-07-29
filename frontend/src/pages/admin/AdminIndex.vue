<template>
  <div class="index-pane">
    <div class="glass index-card">
      <h3 class="index-title">文件索引</h3>
      <template v-if="idxState.loaded && progress.running">
        <el-progress :percentage="100" :indeterminate="true" :show-text="false" :stroke-width="8" />
        <p>正在扫描：<span class="dim">{{ progress.current || '…' }}</span></p>
        <p>已索引 <b>{{ progress.scanned }}</b> 项</p>
      </template>
      <template v-else>
        <!-- 首次读到之前只占位、不写 0：0 是个像模像样的数字，会被当成「索引空了」 -->
        <p v-if="idxFailed" class="err">读不到索引信息：{{ idxState.err }}
          <el-button link type="primary" @click="loadProgress">重试</el-button>
        </p>
        <p v-else>索引共 <b v-if="idxState.loaded">{{ progress.scanned }}</b><i v-else class="ph" /> 项</p>
        <p v-if="idxState.loaded && progress.err" class="err">上次重建出错：{{ progress.err }}</p>
        <p class="dim">重建会全量扫描所有存储，供搜索与媒体库使用。日常写操作与存储改动会自动增量更新，无需重建。</p>
      </template>
      <div class="index-actions">
        <el-button type="primary" :disabled="!idxState.loaded || progress.running" :icon="RefreshRight"
          @click="rebuild">重建索引</el-button>
        <el-button type="danger" link :disabled="!idxState.loaded || progress.running" :icon="Delete"
          @click="clearIndex">删除索引</el-button>
      </div>
    </div>

    <div class="glass index-card">
      <h3 class="index-title">封面与源信息预载</h3>
      <template v-if="preState.loaded && preload.snoozed">
        <p v-if="preload.running">已推迟，正在停下手头的几项…</p>
        <p v-else>{{ snoozeNote }}</p>
        <p>封面 <b>{{ preload.covers }}</b>{{ coverOf }} · 视频源信息 <b>{{ preload.probes }}</b>{{ probeOf }}
          已缓存<template v-if="preload.pending">，还剩 {{ preload.pending }} 项</template></p>
        <p class="dim">推迟期间不会在后台预载，浏览到的封面与源信息改为当场加载。</p>
      </template>
      <template v-else-if="preState.loaded && preload.running">
        <el-progress :percentage="preloadPct" :stroke-width="8" />
        <p>正在预载：<span class="dim">{{ preload.current || '…' }}</span></p>
        <!-- 总数只含真要下载/探测的项，已缓存的不算——否则进度条一开局就停在缓存占比上 -->
        <p>已处理 <b>{{ preload.done }}</b> / {{ preload.total }} 项</p>
        <p>封面 <b>{{ preload.covers }}</b>{{ coverOf }} · 视频源信息 <b>{{ preload.probes }}</b>{{ probeOf }} 已缓存</p>
      </template>
      <template v-else>
        <p v-if="preFailed" class="err">读不到预载信息：{{ preState.err }}
          <el-button link type="primary" @click="loadProgress">重试</el-button>
        </p>
        <p v-else>封面 <b v-if="preState.loaded">{{ preload.covers }}</b><i v-else class="ph" />{{ coverOf }}
          · 视频源信息 <b v-if="preState.loaded">{{ preload.probes }}</b><i v-else class="ph" />{{ probeOf }} 已缓存</p>
        <!-- 秒回的失败（云盘不给缩略图、抽帧崩了、探测连不上）不摆出来，界面上只剩
             「跑完了」，看着就像后台没干活 -->
        <p v-if="preState.loaded && preload.failed" class="err">
          {{ preload.failed }} 项没能预载<template v-if="preload.fail_note">：{{ preload.fail_note }}</template>
          <template v-if="preload.will_retry"> 稍后会自动重试。</template>
        </p>
        <p v-if="preState.loaded && preload.err" class="err">上次预载出错：{{ preload.err }}</p>
        <p class="dim">挂载云盘并勾选「在视频库/照片墙展示」后，会自动在后台加载封面与视频源信息并缓存，之后浏览即刻呈现、播放免现场探测。</p>
      </template>
      <div class="index-actions">
        <el-button v-if="preState.loaded && preload.snoozed" type="primary" :icon="VideoPlay"
          @click="resumePreload">继续</el-button>
        <template v-else>
          <el-button :disabled="!preState.loaded || preload.running" :icon="RefreshRight"
            @click="runPreload">重新预载</el-button>
          <el-button v-if="preState.loaded && preload.running" :icon="Clock"
            @click="snoozePreload">不是现在</el-button>
        </template>
        <el-button type="danger" link :disabled="!preState.loaded" :icon="Delete"
          @click="clearPreload">删除缓存</el-button>
      </div>
    </div>

    <p v-if="staleErr" class="dim pane-note">刷新失败：{{ staleErr }}。上面是上一次读到的数字。</p>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import 'element-plus/es/components/message-box/style/css'
import { Clock, Delete, RefreshRight, VideoPlay } from '@element-plus/icons-vue'
import { api } from '../../utils/api'
import { formatSize } from '../../utils/file'

// active：本 pane 是否为当前选中 Tab。切到索引管理时刷新进度（原 Admin.vue 的 watch(tab)），
// 好在别的 Tab 触发重建后切回来能看到运行中的进度与最新条数。
const props = defineProps({ active: { type: Boolean, default: false } })

const progress = ref({ running: false, scanned: 0, current: '', err: '' })
const preload = ref({
  running: false, total: 0, done: 0, covers: 0, probes: 0, current: '', err: '',
  snoozed: false, resume_at: '', pending: 0, failed: 0, fail_note: '', will_retry: false,
  cover_total: 0, probe_total: 0,
})
// coverOf / probeOf 是「/ 多少」这半截分母：没有它，光看「封面 2952」对不上索引里的
// 五万条，只能靠猜。分母是「本该有多少」——能出封面的媒体数、需要探测的视频数
// （mp4 一类浏览器直接能播的不需要探测）。还没读到分母时不显示，免得写出「/ 0」。
const coverOf = computed(() => (preload.value.cover_total ? ` / ${preload.value.cover_total}` : ''))
const probeOf = computed(() => (preload.value.probe_total ? ` / ${preload.value.probe_total}` : ''))
const preloadPct = computed(() =>
  preload.value.total > 0 ? Math.round((preload.value.done / preload.value.total) * 100) : 0)
// 推迟到点：一天后基本都落在「明天 HH:MM」，跨重启恢复时也可能是今天
const snoozeNote = computed(() => {
  const at = fmtTime(preload.value.resume_at)
  return at ? `已推迟，${at}自动继续。` : '已推迟。'
})
// 两张卡各自记加载状态：首次读到真实进度前只占位、不写数字——进度是内存态，默认值全是 0，
// 直接渲染会先斩钉截铁地说「索引共 0 项」再跳成真实条数，看着像索引没了又回来。
// 分开记是因为两个接口各读各的，一个失败不该把另一张卡也说成读不到。
const idxState = ref({ loaded: false, err: '' })
const preState = ref({ loaded: false, err: '' })
const idxFailed = computed(() => !idxState.value.loaded && !!idxState.value.err)
const preFailed = computed(() => !preState.value.loaded && !!preState.value.err)
// 已有数字后又刷新失败：留住上一次的值，底部说明一句，免得进度条僵在那里看不出是断了
const staleErr = computed(() =>
  (idxState.value.loaded && idxState.value.err) || (preState.value.loaded && preState.value.err) || '')
let pollTimer = null

function fmtTime(iso) {
  const t = new Date(iso || '')
  if (isNaN(t.getTime())) return ''
  const midnight = (x) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const days = Math.round((midnight(t) - midnight(new Date())) / 86400000)
  const day = days <= 0 ? '' : days === 1 ? '明天 ' : `${t.getMonth() + 1}月${t.getDate()}日 `
  return `${day}${t.getHours()}:${String(t.getMinutes()).padStart(2, '0')} `
}

// 两个 progress 接口都是 silent：1.5s 一次的轮询若连不上，逐次弹 toast 会刷屏。
// 失败改为就地呈现——首次失败给「重试」，已有数字时留住上一次的值并继续轮询自愈。
// allSettled 而非 all：一个挂了不牵连另一张卡。
async function loadProgress() {
  const [idx, pre] = await Promise.allSettled([
    api.admin.index.progress(),
    api.admin.preload.progress(),
  ])
  if (idx.status === 'fulfilled') {
    progress.value = idx.value
    idxState.value = { loaded: true, err: '' }
  } else {
    idxState.value = { ...idxState.value, err: idx.reason?.message || '请求失败' }
    console.error(idx.reason)
  }
  if (pre.status === 'fulfilled') {
    preload.value = pre.value
    preState.value = { loaded: true, err: '' }
  } else {
    preState.value = { ...preState.value, err: pre.reason?.message || '请求失败' }
    console.error(pre.reason)
  }
  // 轮询：任一侧在跑就开着；读不到状态时也保持轮询，好在服务恢复后自动补上数字
  const busy = (idxState.value.loaded && progress.value.running) ||
    (preState.value.loaded && preload.value.running) ||
    !!idxState.value.err || !!preState.value.err
  if (busy && !pollTimer) {
    pollTimer = setInterval(loadProgress, 1500)
  } else if (!busy && pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}
async function rebuild() {
  await api.admin.index.rebuild()
  ElMessage.success('已开始重建')
  loadProgress()
}
// confirmDelete 两处删除共用的确认弹窗：取消走 catch，当作没点。
function confirmDelete(title, body) {
  return ElMessageBox.confirm(body, title,
    { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' })
    .then(() => true).catch(() => false)
}

async function clearIndex() {
  if (!await confirmDelete('删除索引',
    '索引只记录文件的位置与名称，文件本身不受影响。搜索与媒体库会暂时为空，直到重新建立索引。')) return
  try {
    progress.value = await api.admin.index.clear()
    ElMessage.success('索引已删除')
  } catch {
    // 多半是 409：别处已开始重建。失败原因拦截器已经弹过，这里只把进度拉回真实状态
    loadProgress()
  }
}

async function clearPreload() {
  if (!await confirmDelete('删除缓存',
    '删除全部已缓存的封面与视频源信息，正在进行的预载会停下。之后浏览与播放时重新加载，文件本身不受影响。')) return
  try {
    const r = await api.admin.preload.clear()
    ElMessage.success(r?.covers
      ? `已删除 ${r.covers} 个封面，释放 ${formatSize(r.bytes)}`
      : '缓存已删除')
  } catch {
    // 原因已由拦截器弹出；预载此时已停下，刷新进度让界面与服务端一致
  }
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
.pane-note { flex-basis: 100%; margin: 0; font-size: 13px; }

/* 数字占位：与 <b> 同高的一枚短条，读数到位就换成真数字，卡片不跳行 */
.ph {
  display: inline-block; width: 2.4em; height: .78em; border-radius: 4px;
  background: currentColor; opacity: .16; animation: ph-breathe 1.4s ease-in-out infinite;
}
@keyframes ph-breathe {
  0%, 100% { opacity: .1; }
  50% { opacity: .22; }
}
@media (prefers-reduced-motion: reduce) {
  .ph { animation: none; }
}

@media (max-width: 768px) {
  .index-card { min-width: 0; flex-basis: 100%; max-width: none; padding: 18px; }
}
</style>
