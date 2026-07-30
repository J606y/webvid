<template>
  <div>
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
          <el-button v-if="row.driver === 'googledrive'" link size="small" type="primary"
            :icon="Connection" title="授权 Google" @click="authGoogle(row)" />
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
      <el-form ref="storageFormRef" :model="storageForm" :rules="storageRules" label-width="130px">
        <el-form-item label="挂载路径" prop="mount_path" required>
          <el-input v-model="storageForm.mount_path" placeholder="/网盘名" />
        </el-form-item>
        <el-form-item label="驱动" prop="driver" required>
          <el-select v-model="storageForm.driver" :disabled="!!editingStorage?.id" style="width: 100%"
            @change="onDriverChange">
            <el-option v-for="m in drivers" :key="m.name" :label="m.label" :value="m.name" />
          </el-select>
        </el-form-item>
        <template v-for="f in currentFields" :key="f.name">
          <el-form-item :label="f.label" :prop="'config.' + f.name" :required="f.required">
            <!-- locked：取值由驱动决定（如 Google Drive 恒中转），显示成已开启且点不动 -->
            <el-switch v-if="f.type === 'bool'" :disabled="f.locked"
              :model-value="f.locked ? f.default === 'true' : storageForm.config[f.name] === 'true'"
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
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import 'element-plus/es/components/message-box/style/css'
import { Plus, EditPen, Delete, RefreshRight, Key, Connection } from '@element-plus/icons-vue'
import { api } from '../../utils/api'
import { useHeroDialog } from '../../utils/heroDialog'
import { isMobile } from '../../utils/viewport'

const saving = ref(false)

// ---- 存储管理 ----
const storages = ref([])
const drivers = ref([])
const storageDlg = ref(false)
// iOS 式 hero 转场：弹窗从触发按钮处放大展开、关闭缩回原位（详见 utils/heroDialog）
const storageHero = useHeroDialog('.admin-hero-storage .el-dialog', () => { storageDlg.value = false })
const editingStorage = ref(null)
const storageForm = ref(emptyStorage())
const storageFormRef = ref(null)

function emptyStorage() {
  return { mount_path: '', driver: 'local', config: {}, ord: 0, enabled: true }
}
function driverLabel(name) {
  return drivers.value.find((d) => d.name === name)?.label || name
}
const currentFields = computed(() =>
  drivers.value.find((d) => d.name === storageForm.value.driver)?.fields || [])

// 校验规则随驱动变：必填项由驱动自己声明（fields[].required）。
// 此前红星只是装饰——留空照样"保存成功"，直到挂载失败才在状态列看出问题。
const storageRules = computed(() => {
  const r = {
    mount_path: [{ required: true, message: '请填写挂载路径', trigger: 'blur' }],
    driver: [{ required: true, message: '请选择驱动', trigger: 'change' }],
  }
  for (const f of currentFields.value) {
    if (!f.required) continue
    // 编辑已有存储时，密钥类字段留空或 *** 表示沿用旧值，不算缺填
    if (f.secret && editingStorage.value?.id) continue
    r['config.' + f.name] = [{ required: true, message: `请填写${f.label}`, trigger: 'blur' }]
  }
  return r
})

async function loadStorages() {
  try {
    storages.value = await api.admin.storages.list() || []
  } catch (e) {
    console.error(e)
    ElMessage.error('存储列表加载失败')
  }
}
async function loadDrivers() {
  try {
    drivers.value = await api.admin.drivers() || []
  } catch (e) {
    console.error(e)
    ElMessage.error('驱动列表加载失败')
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
      const full = await api.admin.storages.get(row.id)
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
  const ok = await storageFormRef.value?.validate().then(() => true).catch(() => false)
  if (!ok) return
  saving.value = true
  try {
    // scanning 由后端给：只有文件清单可能变了（新增存储、换驱动/账号/根目录）才会后台重扫
    // 这一个盘；改排序、展示开关这类不动索引，就别说"正在索引"
    const r = editingStorage.value?.id
      ? await api.admin.storages.update(editingStorage.value.id, storageForm.value)
      : await api.admin.storages.create(storageForm.value)
    ElMessage.success(r?.scanning ? '已保存，正在后台索引该存储' : '已保存')
    storageHero.animatedClose()
    loadStorages()
  } finally {
    saving.value = false
  }
}
async function reloadStorage(row) {
  const r = await api.admin.storages.reload(row.id)
  ElMessage.success(r?.scanning ? '已重载，正在后台索引该存储' : '已重载')
  loadStorages()
}
async function deleteStorage(row) {
  await ElMessageBox.confirm(`确定删除存储 ${row.mount_path}？文件本身不会被删除。`, '删除确认',
    { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' })
  await api.admin.storages.remove(row.id)
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
  return storages.value.some((s) => s.driver === 'telegram' || s.driver === 'googledrive') ? 200 : 170
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

// 「发送验证码」/「重新发送」这两个文案不能只凭本地是否点过按钮来定：后端 LoginManager
// 的登录会话（pendingLogin）与本弹窗的生命周期是分开的——关掉弹窗再重开，会话在服务端
// 10 分钟内仍然存活（见 internal/driver/telegram/login.go 的 loginTTL），此时再点
// 「发送验证码」实际会命中 resendCode（切换投递通道），并非用户以为的"全新发一次"。
// 会话是否还活着以服务端为准（GET /admin/telegram/:id/status），打开弹窗时问一次；
// sessionStorage 只额外记住"上次发到了哪、下次可切到什么通道"这类服务端不便重放的文案，
// 到期时间与后端 loginTTL 对齐。两者不一致时听服务端的。
const TG_PENDING_TTL_MS = 10 * 60 * 1000
const tgMemKey = (id) => `webvid:tg-pending:${id}`

function tgLoadMemory(id) {
  try {
    const raw = sessionStorage.getItem(tgMemKey(id))
    if (!raw) return null
    const mem = JSON.parse(raw)
    if (!mem?.expiresAt || Date.now() >= mem.expiresAt) {
      sessionStorage.removeItem(tgMemKey(id))
      return null
    }
    return mem
  } catch {
    return null // 隐私模式等禁用 sessionStorage 时静默退化为"每次都当作全新发送"
  }
}
function tgSaveMemory(id, mem) {
  try {
    sessionStorage.setItem(tgMemKey(id), JSON.stringify(mem))
  } catch { /* 存储被禁用：不影响本次登录，只是下次重开弹窗文案会退回默认 */ }
}
function tgClearMemory(id) {
  try {
    sessionStorage.removeItem(tgMemKey(id))
  } catch { /* 同上 */ }
}

async function openTgLogin(row, ev) {
  const originEl = ev?.currentTarget
  tgStorage.value = row
  tgCode.value = ''
  tgPwd.value = ''
  tgNeedPwd.value = false
  const mem = tgLoadMemory(row.id)
  tgCodeSent.value = !!mem
  tgSentTo.value = mem?.sentTo || ''
  tgResend.value = mem?.resend || ''
  tgTimeout.value = mem?.timeout || 0
  tgHero.open(originEl, () => { tgDlg.value = true })
  // 弹窗已经开了，再向服务端核一次真实会话状态作补正——本地记忆看不见别的标签页
  // 或别的设备发过的码。查询失败就沿用本地记忆，不挡发码。
  try {
    const st = await api.admin.telegram.status(row.id)
    if (tgStorage.value?.id !== row.id) return // 期间换了存储，丢弃过期结果
    tgCodeSent.value = !!st?.pending
    if (!st?.pending) {
      tgSentTo.value = ''
      tgResend.value = ''
      tgTimeout.value = 0
      tgClearMemory(row.id)
    }
  } catch { /* 状态查不到不影响发码，按本地记忆显示 */ }
}
async function tgSendCode() {
  tgSending.value = true
  try {
    // 首次=发码；若服务端识别到未过期的登录会话（本地记忆到期前重开弹窗、或本地记忆
    // 丢失但会话仍活着）则走 resendCode 切换投递通道（App→短信→电话），响应里的
    // resumed 如实告知是哪一种。后端给 MTProto 握手 60s 预算，请求超时须大于它，
    // 否则前端先断连带崩后端 ctx
    const r = await api.admin.telegram.sendCode(tgStorage.value.id)
    tgCodeSent.value = true
    tgSentTo.value = r?.sent_to || '验证码已发送，优先查看 Telegram 客户端消息'
    tgResend.value = r?.resend || ''
    tgTimeout.value = r?.timeout || 0
    tgSaveMemory(tgStorage.value.id, {
      sentTo: tgSentTo.value, resend: tgResend.value, timeout: tgTimeout.value,
      expiresAt: Date.now() + TG_PENDING_TTL_MS,
    })
    ElMessage.success(r?.resumed
      ? `已复用此前未完成的登录会话，改走：${tgSentTo.value}`
      : tgSentTo.value)
  } finally {
    tgSending.value = false
  }
}
async function tgSignIn() {
  if (!tgCodeSent.value || !tgCode.value.trim()) return
  tgSigning.value = true
  try {
    const r = await api.admin.telegram.signIn(tgStorage.value.id,
      { code: tgCode.value.trim(), password: tgPwd.value })
    if (r?.need_password) {
      tgNeedPwd.value = true
      ElMessage.warning('该账号开启了两步验证，请补填两步密码后再点登录')
      return
    }
    ElMessage.success(r?.scanning ? '登录成功，正在后台索引该存储' : '登录成功，存储已重载')
    tgClearMemory(tgStorage.value.id) // 会话已消费完毕，不能再让下次重开弹窗误判成"仍待续发"
    tgHero.animatedClose()
    loadStorages()
  } finally {
    tgSigning.value = false
  }
}

// ---- Google Drive 一键授权 ----
// 打开 Google 同意页新标签；回调在服务端换 refresh_token 并重载存储。
//
// 「授权是否真的完成」不能由用户点了「已完成」还是「关闭」来定——两者过去走的是同一条
// 只会 loadStorages() 的路，用户没点同意、中途取消，回来点「已完成」也照样"看着像成功"。
// 现在双保险都以服务端状态为准：
//   1) 回调页（handler_googledrive.go 的 gdCallbackHTML）成功落盘后用同源 BroadcastChannel
//      把 {id, ok, message} 广播出来，这里监听到后立即收起确认弹窗、给出准确提示；
//   2) 万一用户中途直接关掉新标签、广播根本不会发生，点「已完成/关闭」任一按钮都会回查
//      该存储此刻的真实 config（refresh_token 是否写入）与挂载 status，而不是无脑刷新。
const GD_AUTH_CHANNEL = 'webvid-googledrive-auth'

// 回查存储真实状态：refresh_token 已写入且挂载没有报错 status，才算授权真正生效。
async function checkGoogleAuthDone(id) {
  try {
    const full = await api.admin.storages.get(id)
    if (full?.status) return { ok: false, message: `尚未就绪：${full.status}` }
    if (!full?.config?.refresh_token) {
      return { ok: false, message: '尚未完成授权：请在新标签内走完 Google 同意页后再点「已完成」' }
    }
    return { ok: true, message: 'Google 授权已完成，存储已就绪' }
  } catch (e) {
    return { ok: false, message: '状态核对失败，请稍后重试或手动刷新列表确认' }
  }
}

async function authGoogle(row) {
  let d
  try {
    d = await api.admin.googledrive.authUrl(row.id, { origin: window.location.origin })
  } catch (e) {
    return // 拦截器已弹错误（多为未填 client_id/secret）
  }
  window.open(d.auth_url, '_blank', 'noopener')

  let settled = false
  let bc = null
  const result = await new Promise((resolve) => {
    const finish = (r) => {
      if (settled) return
      settled = true
      resolve(r)
    }
    if (typeof BroadcastChannel !== 'undefined') {
      bc = new BroadcastChannel(GD_AUTH_CHANNEL)
      bc.onmessage = (ev) => {
        if (ev.data?.id && ev.data.id !== row.id) return // 不是这条存储的回调，忽略
        finish({ ok: !!ev.data?.ok, message: ev.data?.message || '' })
        ElMessageBox.close() // 服务端已给出真实结果，收起还在等待中的确认弹窗
      }
    }
    ElMessageBox.confirm(
      '已在新标签打开 Google 授权页。请在其中完成授权（首次需选择账号并允许访问），完成后回到这里点「已完成」核对状态。',
      'Google 授权',
      { confirmButtonText: '已完成', cancelButtonText: '关闭', type: 'info' })
      .then(() => { if (!settled) checkGoogleAuthDone(row.id).then(finish) })
      .catch(() => { if (!settled) checkGoogleAuthDone(row.id).then(finish) })
  })
  if (bc) bc.close()

  if (result.ok) ElMessage.success(result.message)
  else ElMessage.warning(result.message)
  loadStorages()
}

onMounted(() => {
  loadDrivers()
  loadStorages()
})
</script>

<style scoped>
.pane-head { display: flex; justify-content: flex-end; margin-bottom: 10px; }
.field-help { font-size: 12px; line-height: 1.5; margin-top: 3px; }
.tg-send { margin-left: 12px; }
.tg-hint { width: 100%; font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.6; margin-top: 4px; }

/* ---- 移动端：表格单元格收紧，让状态/操作列留在屏内不必横向滑动（#29） ---- */
@media (max-width: 768px) {
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
