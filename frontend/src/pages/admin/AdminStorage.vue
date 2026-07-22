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
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import 'element-plus/es/components/message-box/style/css'
import { Plus, EditPen, Delete, RefreshRight, Key } from '@element-plus/icons-vue'
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
  saving.value = true
  try {
    if (editingStorage.value?.id) {
      await api.admin.storages.update(editingStorage.value.id, storageForm.value)
    } else {
      await api.admin.storages.create(storageForm.value)
    }
    ElMessage.success('已保存，索引重建已触发')
    storageHero.animatedClose()
    loadStorages()
  } finally {
    saving.value = false
  }
}
async function reloadStorage(row) {
  await api.admin.storages.reload(row.id)
  ElMessage.success('已重载')
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
    const r = await api.admin.telegram.sendCode(tgStorage.value.id)
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
    const r = await api.admin.telegram.signIn(tgStorage.value.id,
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
