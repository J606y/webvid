<template>
  <div class="page admin-page">
    <h1 class="page-title">后台管理</h1>
    <el-tabs v-model="tab" class="glass tabs">
      <!-- 站点设置 -->
      <el-tab-pane label="站点设置" name="site">
        <el-form label-width="104px" style="max-width: 520px">
          <el-form-item label="站点标题">
            <el-input v-model="site.site_title" />
          </el-form-item>

          <el-form-item>
            <el-button type="primary" @click="saveSite">保存</el-button>
          </el-form-item>
        </el-form>
      </el-tab-pane>

      <!-- 任务设置（线程数 + 限速，独立于站点设置） -->
      <el-tab-pane label="任务设置" name="task">
        <el-form label-width="104px" style="max-width: 520px">
          <el-divider content-position="left" class="task-sect">并发线程</el-divider>
          <el-form-item label="复制任务线程">
            <el-input-number v-model="site.copy_workers" :min="1" :max="32" />
            <div class="dim field-help">跨存储复制/移动同时执行的任务数，其余排队；保存后立即生效</div>
          </el-form-item>
          <el-form-item label="离线下载线程">
            <el-input-number v-model="site.offline_workers" :min="1" :max="32" />
            <div class="dim field-help">同时进行的离线下载任务数</div>
          </el-form-item>
          <el-form-item label="上传并发数">
            <el-input-number v-model="site.upload_workers" :min="1" :max="8" />
            <div class="dim field-help">网页端同时上传的文件数</div>
          </el-form-item>

          <el-divider content-position="left" class="task-sect">速度限制（KB/s，0 为不限速）</el-divider>
          <el-form-item label="复制限速">
            <el-input-number v-model="site.copy_speed_kb" :min="0" :step="512" />
            <div class="dim field-help">全部复制/移动任务共享的总速率</div>
          </el-form-item>
          <el-form-item label="上传限速">
            <el-input-number v-model="site.upload_speed_kb" :min="0" :step="512" />
            <div class="dim field-help">网页上传的总速率</div>
          </el-form-item>
          <el-form-item label="下载限速">
            <el-input-number v-model="site.download_speed_kb" :min="0" :step="512" />
            <div class="dim field-help">服务器中转下行（下载/直连播放）与离线下载共享的总速率；云盘 302 直链不经服务器、不受限</div>
          </el-form-item>

          <el-form-item>
            <el-button type="primary" @click="saveSite">保存</el-button>
          </el-form-item>
        </el-form>
      </el-tab-pane>

      <!-- 存储管理 -->
      <el-tab-pane label="存储管理" name="storage">
        <div class="pane-head">
          <el-button type="primary" size="small" :icon="Plus" @click="openStorage(null, $event)">添加存储</el-button>
        </div>
        <el-table :data="storages">
          <el-table-column prop="mount_path" label="挂载路径" :min-width="isMobile ? 88 : 160" />
          <el-table-column prop="driver" label="驱动" :width="isMobile ? 82 : 110">
            <template #default="{ row }">{{ driverLabel(row.driver) }}</template>
          </el-table-column>
          <el-table-column label="状态" :min-width="isMobile ? 64 : 140">
            <template #default="{ row }">
              <el-tag v-if="!row.enabled" type="info" size="small">已停用</el-tag>
              <el-tag v-else-if="row.status" type="danger" size="small">{{ row.status }}</el-tag>
              <el-tag v-else type="success" size="small">正常</el-tag>
            </template>
          </el-table-column>
          <el-table-column :width="storageOpsWidth" align="right">
            <template #default="{ row }">
              <el-button v-if="row.driver === 'telegram'" link size="small" type="primary"
                :icon="Key" title="验证码登录" @click="openTgLogin(row, $event)" />
              <el-button link size="small" :icon="EditPen" @click="openStorage(row, $event)" />
              <el-button link size="small" :icon="RefreshRight" @click="reloadStorage(row)" />
              <el-button link size="small" type="danger" :icon="Delete" @click="deleteStorage(row)" />
            </template>
          </el-table-column>
        </el-table>

        <!-- append-to-body：脱离 .glass 容器（backdrop-filter 会让 fixed 相对面板定位而被裁剪） -->
        <el-dialog v-model="storageDlg" :title="editingStorage?.id ? '编辑存储' : '添加存储'" width="480px"
          append-to-body destroy-on-close modal-class="admin-hero admin-hero-storage"
          :before-close="storageHero.animatedClose">
          <el-form label-width="130px">
            <el-form-item label="挂载路径" required>
              <el-input v-model="storageForm.mount_path" placeholder="/网盘名" />
            </el-form-item>
            <el-form-item label="驱动" required>
              <el-select v-model="storageForm.driver" :disabled="!!editingStorage?.id" style="width: 100%"
                @change="onDriverChange">
                <el-option v-for="m in drivers" :key="m.name" :label="m.label" :value="m.name" />
              </el-select>
            </el-form-item>
            <template v-for="f in currentFields" :key="f.name">
              <el-form-item :label="f.label" :required="f.required">
                <el-switch v-if="f.type === 'bool'"
                  :model-value="storageForm.config[f.name] === 'true'"
                  @update:model-value="storageForm.config[f.name] = $event ? 'true' : 'false'" />
                <el-select v-else-if="f.type === 'select'" v-model="storageForm.config[f.name]"
                  style="width: 100%">
                  <el-option v-for="o in f.options" :key="o" :label="o" :value="o" />
                </el-select>
                <el-input v-else v-model="storageForm.config[f.name]"
                  :type="f.type === 'password' ? 'password' : 'text'"
                  :show-password="f.type === 'password'"
                  :placeholder="f.secret && editingStorage?.id ? '留空或 *** 表示不修改' : f.default" />
                <div v-if="f.help" class="dim field-help">{{ f.help }}</div>
              </el-form-item>
            </template>
            <el-form-item label="排序">
              <el-input-number v-model="storageForm.ord" :min="0" />
            </el-form-item>
            <el-form-item label="启用">
              <el-switch v-model="storageForm.enabled" />
            </el-form-item>
          </el-form>
          <template #footer>
            <el-button @click="storageHero.animatedClose()">取消</el-button>
            <el-button type="primary" :loading="saving" @click="saveStorage">保存</el-button>
          </template>
        </el-dialog>

        <!-- Telegram 验证码登录（send_code / sign_in 间后端保持同一连接） -->
        <el-dialog v-model="tgDlg" title="Telegram 登录" width="400px" append-to-body
          destroy-on-close modal-class="admin-hero admin-hero-tg"
          :before-close="tgHero.animatedClose">
          <el-form label-width="90px" @submit.prevent>
            <el-form-item label="手机号">
              <span>{{ tgStorage?.config?.phone || '（未配置）' }}</span>
              <el-button size="small" :loading="tgSending" class="tg-send" @click="tgSendCode">
                {{ tgCodeSent ? '重新发送' : '发送验证码' }}
              </el-button>
            </el-form-item>
            <el-form-item label="验证码">
              <el-input v-model="tgCode" :disabled="!tgCodeSent"
                placeholder="查看 Telegram 客户端或短信" @keyup.enter="tgSignIn" />
              <div v-if="tgSentTo" class="tg-hint">
                {{ tgSentTo }}<span v-if="tgResend">；收不到？点「重新发送」将改用{{ tgResend }}<span v-if="tgTimeout">（约 {{ tgTimeout }} 秒后可重发）</span></span>
              </div>
            </el-form-item>
            <el-form-item v-if="tgNeedPwd" label="两步密码">
              <el-input v-model="tgPwd" type="password" show-password
                placeholder="该账号开启了两步验证" @keyup.enter="tgSignIn" />
            </el-form-item>
          </el-form>
          <template #footer>
            <el-button @click="tgHero.animatedClose()">取消</el-button>
            <el-button type="primary" :loading="tgSigning"
              :disabled="!tgCodeSent || !tgCode.trim()" @click="tgSignIn">登录</el-button>
          </template>
        </el-dialog>
      </el-tab-pane>

      <!-- 用户管理 -->
      <el-tab-pane label="用户管理" name="users">
        <div class="pane-head">
          <el-button type="primary" size="small" :icon="Plus" @click="openUser(null, $event)">添加用户</el-button>
        </div>
        <el-table :data="users">
          <el-table-column prop="username" label="用户名" :min-width="isMobile ? 90 : 120" />
          <el-table-column label="角色" :width="isMobile ? 72 : 100">
            <template #default="{ row }">
              <el-tag :type="row.role === 'admin' ? 'warning' : 'info'" size="small">
                {{ row.role === 'admin' ? '管理员' : '用户' }}
              </el-tag>
            </template>
          </el-table-column>
          <!-- 移动端窄屏放不下，可见根路径与可写移交编辑弹窗查看 -->
          <el-table-column v-if="!isMobile" prop="base_path" label="可见根路径" min-width="140" />
          <el-table-column v-if="!isMobile" label="可写" width="70">
            <template #default="{ row }">{{ row.role === 'admin' || row.can_write ? '是' : '否' }}</template>
          </el-table-column>
          <el-table-column label="状态" :width="isMobile ? 62 : 90">
            <template #default="{ row }">
              <el-tag :type="row.enabled ? 'success' : 'danger'" size="small">
                {{ row.enabled ? '启用' : '停用' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :width="isMobile ? 80 : 120" align="right">
            <template #default="{ row }">
              <el-button link size="small" :icon="EditPen" @click="openUser(row, $event)" />
              <el-button link size="small" type="danger" :icon="Delete"
                :disabled="row.id === auth.user?.id" @click="deleteUser(row)" />
            </template>
          </el-table-column>
        </el-table>

        <el-dialog v-model="userDlg" :title="editingUser?.id ? '编辑用户' : '添加用户'" width="440px"
          append-to-body destroy-on-close modal-class="admin-hero admin-hero-user"
          :before-close="userHero.animatedClose">
          <el-form label-width="110px">
            <el-form-item label="用户名" required>
              <el-input v-model="userForm.username" autocomplete="off" name="new-user-name" />
            </el-form-item>
            <el-form-item :label="editingUser?.id ? '重置密码' : '密码'">
              <!-- autocomplete=new-password：管理员是在「新建/重置某用户」的密码，告诉 iOS Safari
                   别把它当登录框去扫描/填充已存密码——否则 Safari 的自动填充会卡住主线程好几秒
                   （安卓无此问题）。这才是「添加用户卡、添加网盘不卡」的真因，与磨砂/渲染无关。 -->
              <el-input v-model="userForm.password" type="password" show-password
                autocomplete="new-password" name="new-user-password"
                :placeholder="editingUser?.id ? '留空则不修改' : '留空则自动生成'" />
            </el-form-item>
            <el-form-item label="角色">
              <el-select v-model="userForm.role" style="width: 100%">
                <el-option label="用户" value="user" />
                <el-option label="管理员" value="admin" />
              </el-select>
            </el-form-item>
            <el-form-item label="可见根路径">
              <el-input v-model="userForm.base_path" placeholder="/，或 /某目录 限定视野" />
            </el-form-item>
            <el-form-item label="允许写入">
              <el-switch v-model="userForm.can_write" :disabled="userForm.role === 'admin'" />
            </el-form-item>
            <el-form-item v-if="editingUser?.id" label="启用">
              <el-switch v-model="userForm.enabled" />
            </el-form-item>
          </el-form>
          <template #footer>
            <el-button @click="userHero.animatedClose()">取消</el-button>
            <el-button type="primary" :loading="saving" @click="saveUser">保存</el-button>
          </template>
        </el-dialog>
      </el-tab-pane>

      <!-- 索引管理 -->
      <el-tab-pane label="索引管理" name="index">
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
            <template v-if="preload.running">
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
            <el-button :disabled="preload.running" :icon="RefreshRight" @click="runPreload">重新预载</el-button>
          </div>
        </div>
      </el-tab-pane>
    </el-tabs>
    <p v-if="app.version" class="dim version-tip">v{{ app.version }}</p>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import 'element-plus/es/components/message-box/style/css'
import { Plus, EditPen, Delete, RefreshRight, Key } from '@element-plus/icons-vue'
import http from '../api/http'
import { useHeroDialog } from '../utils/heroDialog'
import { isMobile } from '../utils/viewport'
import { useAuth } from '../stores/auth'
import { useApp } from '../stores/app'

const auth = useAuth()
const app = useApp()
const tab = ref('site')
const saving = ref(false)

// ---- 站点设置（标题 + 任务线程数 + 限速）----
const site = ref({
  site_title: '', copy_workers: 2, offline_workers: 2, upload_workers: 2,
  copy_speed_kb: 0, upload_speed_kb: 0, download_speed_kb: 0,
})
async function loadSite() {
  try {
    site.value = await http.get('/admin/settings')
  } catch (e) {
    console.error(e)
  }
}
async function saveSite() {
  if (!site.value.site_title) return ElMessage.warning('站点标题不能为空')
  await http.put('/admin/settings', site.value)
  ElMessage.success('已保存并生效')
  app.fetchPublic()
}

// ---- 存储管理 ----
const storages = ref([])
const drivers = ref([])
const storageDlg = ref(false)
// iOS 式 hero 转场：弹窗从触发按钮处放大展开、关闭缩回原位（详见 utils/heroDialog）
const storageHero = useHeroDialog('.admin-hero-storage .el-dialog', () => { storageDlg.value = false })
const editingStorage = ref(null)
const storageForm = ref(emptyStorage())

function emptyStorage() {
  return { mount_path: '', driver: 'local', config: {}, ord: 0, enabled: true }
}
function driverLabel(name) {
  return drivers.value.find((d) => d.name === name)?.label || name
}
const currentFields = computed(() =>
  drivers.value.find((d) => d.name === storageForm.value.driver)?.fields || [])

async function loadStorages() {
  try {
    storages.value = await http.get('/admin/storages') || []
  } catch (e) {
    console.error(e)
  }
}
async function loadDrivers() {
  try {
    drivers.value = await http.get('/admin/drivers') || []
  } catch (e) {
    console.error(e)
  }
}
function onDriverChange() {
  const cfg = {}
  for (const f of currentFields.value) if (f.default) cfg[f.name] = f.default
  storageForm.value.config = cfg
}
async function openStorage(row, ev) {
  const originEl = ev?.currentTarget // 转场来源按钮，须在 await 前同步取（事件派发后 currentTarget 归零）
  editingStorage.value = row || null
  storageForm.value = row
    ? { mount_path: row.mount_path, driver: row.driver, config: { ...row.config }, ord: row.ord, enabled: row.enabled }
    : emptyStorage()
  if (!row) onDriverChange()
  else {
    try {
      // 列表接口的 secret 字段脱敏为 ***，编辑时取单条明文回显，点「眼睛」可见原文；
      // 取失败则保持 ***（保存时后端会保留旧值）
      const full = await http.get(`/admin/storages/${row.id}`)
      if (full?.config) storageForm.value.config = { ...full.config }
    } catch (e) {
      console.error(e)
    }
    for (const f of currentFields.value) {
      // 旧存储的 config 可能缺后来新增的字段，按默认值补齐（否则 bool 开关会显示为关）
      if (storageForm.value.config[f.name] === undefined && f.default) storageForm.value.config[f.name] = f.default
    }
  }
  storageHero.open(originEl, () => { storageDlg.value = true })
}
async function saveStorage() {
  saving.value = true
  try {
    if (editingStorage.value?.id) {
      await http.put(`/admin/storages/${editingStorage.value.id}`, storageForm.value)
    } else {
      await http.post('/admin/storages', storageForm.value)
    }
    ElMessage.success('已保存，索引重建已触发')
    storageHero.animatedClose()
    loadStorages()
  } finally {
    saving.value = false
  }
}
async function reloadStorage(row) {
  await http.post(`/admin/storages/${row.id}/reload`)
  ElMessage.success('已重载')
  loadStorages()
}
async function deleteStorage(row) {
  await ElMessageBox.confirm(`确定删除存储 ${row.mount_path}？文件本身不会被删除。`, '删除确认',
    { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' })
  await http.delete(`/admin/storages/${row.id}`)
  ElMessage.success('已删除')
  loadStorages()
}

// ---- Telegram 验证码登录 ----
// 有 telegram 存储时操作列多一个登录按钮，加宽避免 el-table 内部横溢出（#29 教训）。
// 移动端 390px 屏表格可用仅 ~336：列合计 88+82+64+操作 须 ≤336，操作列只能给 96
//（实测 4 个 link 图标钮各 18px + 2px 间距×3 + 单元格内边距 16 = 94，3 钮更松），
// 原 hasTg 124 会把合计顶到 358 触发内部横向溢出（mobile-check「存储表格无内部横向溢出」）。
const storageOpsWidth = computed(() => {
  if (isMobile.value) return 96
  return storages.value.some((s) => s.driver === 'telegram') ? 200 : 170
})
const tgDlg = ref(false)
const tgHero = useHeroDialog('.admin-hero-tg .el-dialog', () => { tgDlg.value = false })
const tgStorage = ref(null)
const tgCode = ref('')
const tgPwd = ref('')
const tgNeedPwd = ref(false)
const tgCodeSent = ref(false)
const tgSending = ref(false)
const tgSigning = ref(false)
const tgSentTo = ref('') // 验证码实际发到哪（App 内消息/短信/电话），后端透传
const tgResend = ref('')
const tgTimeout = ref(0)

function openTgLogin(row, ev) {
  const originEl = ev?.currentTarget
  tgStorage.value = row
  tgCode.value = ''
  tgPwd.value = ''
  tgNeedPwd.value = false
  tgCodeSent.value = false
  tgSentTo.value = ''
  tgResend.value = ''
  tgTimeout.value = 0
  tgHero.open(originEl, () => { tgDlg.value = true })
}
async function tgSendCode() {
  tgSending.value = true
  try {
    // 首次=发码；再点「重新发送」后端走 resendCode 切换投递通道（App→短信→电话）。
    // 后端给 MTProto 握手 60s 预算，请求超时须大于它，否则前端先断连带崩后端 ctx
    const r = await http.post(`/admin/telegram/${tgStorage.value.id}/send_code`, null, { timeout: 90000 })
    tgCodeSent.value = true
    tgSentTo.value = r?.sent_to || '验证码已发送，优先查看 Telegram 客户端消息'
    tgResend.value = r?.resend || ''
    tgTimeout.value = r?.timeout || 0
    ElMessage.success(tgSentTo.value)
  } finally {
    tgSending.value = false
  }
}
async function tgSignIn() {
  if (!tgCodeSent.value || !tgCode.value.trim()) return
  tgSigning.value = true
  try {
    const r = await http.post(`/admin/telegram/${tgStorage.value.id}/sign_in`,
      { code: tgCode.value.trim(), password: tgPwd.value })
    if (r?.need_password) {
      tgNeedPwd.value = true
      ElMessage.warning('该账号开启了两步验证，请补填两步密码后再点登录')
      return
    }
    ElMessage.success('登录成功，存储已重载')
    tgHero.animatedClose()
    loadStorages()
  } finally {
    tgSigning.value = false
  }
}

// ---- 用户管理 ----
const users = ref([])
const userDlg = ref(false)
const userHero = useHeroDialog('.admin-hero-user .el-dialog', () => { userDlg.value = false })
const editingUser = ref(null)
const userForm = ref(emptyUser())

function emptyUser() {
  return { username: '', password: '', role: 'user', base_path: '/', can_write: false, enabled: true }
}
async function loadUsers() {
  try {
    users.value = await http.get('/admin/users') || []
  } catch (e) {
    console.error(e)
  }
}
function openUser(row, ev) {
  const originEl = ev?.currentTarget
  editingUser.value = row || null
  userForm.value = row
    ? { username: row.username, password: '', role: row.role, base_path: row.base_path, can_write: row.can_write, enabled: row.enabled }
    : emptyUser()
  userHero.open(originEl, () => { userDlg.value = true })
}
async function saveUser() {
  if (!userForm.value.username) return ElMessage.warning('用户名不能为空')
  saving.value = true
  try {
    if (editingUser.value?.id) {
      await http.put(`/admin/users/${editingUser.value.id}`, userForm.value)
      ElMessage.success('已保存')
    } else {
      const d = await http.post('/admin/users', userForm.value)
      if (d.password) {
        await ElMessageBox.alert(`已生成随机密码：${d.password}\n仅显示这一次，请妥善保存。`,
          '用户已创建', { confirmButtonText: '我已保存' })
      } else {
        ElMessage.success('已创建')
      }
    }
    userHero.animatedClose()
    loadUsers()
  } finally {
    saving.value = false
  }
}
async function deleteUser(row) {
  await ElMessageBox.confirm(`确定删除用户 ${row.username}？`, '删除确认',
    { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' })
  await http.delete(`/admin/users/${row.id}`)
  ElMessage.success('已删除')
  loadUsers()
}

// ---- 索引管理 ----
const progress = ref({ running: false, scanned: 0, current: '', err: '' })
const preload = ref({ running: false, total: 0, done: 0, covers: 0, probes: 0, current: '', err: '' })
const preloadPct = computed(() =>
  preload.value.total > 0 ? Math.round((preload.value.done / preload.value.total) * 100) : 0)
let pollTimer = null

async function loadProgress() {
  try {
    const [idx, pre] = await Promise.all([
      http.get('/admin/index/progress'),
      http.get('/admin/preload/progress'),
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
  await http.post('/admin/index/rebuild')
  ElMessage.success('已开始重建')
  loadProgress()
}
async function runPreload() {
  await http.post('/admin/preload/run')
  ElMessage.success('已开始预载封面与源信息')
  loadProgress()
}

watch(tab, (t) => {
  if (t === 'index') loadProgress()
})

onMounted(() => {
  loadSite()
  loadDrivers()
  loadStorages()
  loadUsers()
  loadProgress()
})
onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<style scoped>
.admin-page { max-width: 1000px; min-height: 100vh; display: flex; flex-direction: column; }
.tabs { padding: 10px 20px 20px; }
.version-tip { margin-top: auto; padding-top: 14px; text-align: center; font-size: 12px; }
.pane-head { display: flex; justify-content: flex-end; margin-bottom: 10px; }
.field-help { font-size: 12px; line-height: 1.5; margin-top: 3px; }
/* 任务设置分节标题：EP el-divider 的文字底片默认取 --el-bg-color（暗色玻璃主题下是
   深蓝黑块），看起来像给标题加了黑底。去掉底片与横线，只留一行左对齐小标题。 */
:deep(.el-divider.task-sect) { border-top-color: transparent; margin: 22px 0 8px; }
:deep(.el-divider.task-sect .el-divider__text) {
  background: transparent;
  padding-left: 0;
  color: var(--el-text-color-primary);
  font-weight: 600;
}
.tg-send { margin-left: 12px; }
.tg-hint { width: 100%; font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.6; margin-top: 4px; }
.index-pane { display: flex; gap: 16px; flex-wrap: wrap; }
.index-card {
  padding: 24px; min-width: 380px; flex: 1 1 380px; max-width: 460px;
  display: flex; flex-direction: column; gap: 10px; align-items: flex-start;
}
.index-card p { margin: 0; font-size: 14px; }
.index-title { margin: 0 0 4px; font-size: 15px; font-weight: 600; }
.index-card .el-progress { width: 100%; }
.err { color: var(--el-color-error); }

/* ---- 移动端 ---- */
@media (max-width: 768px) {
  .tabs { padding: 6px 12px 14px; }
  .index-card { min-width: 0; flex-basis: 100%; max-width: none; padding: 18px; }
  /* 五个 Tab 挤进窄屏：收紧内边距与字号，免出现左右滚动箭头 */
  :deep(.el-tabs__item) { padding: 0 8px; font-size: 12px; }
  /* 表格单元格收紧，让状态/操作列留在屏内不必横向滑动 */
  :deep(.el-table .cell) { padding: 0 8px; }
  /* 操作列图标按钮紧凑排布（默认相邻按钮间距 12px 过宽） */
  :deep(.el-table td .el-button + .el-button) { margin-left: 2px; }
  /* 状态列长文案（如 telegram「未登录：请点击钥匙按钮登录」）不许撑出单元格：
     tag 钉在列宽内、文字超出省略；完整文案桌面端可见 */
  :deep(.el-table .el-tag) { max-width: 100%; }
  :deep(.el-table .el-tag .el-tag__content) {
    min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
}
</style>
