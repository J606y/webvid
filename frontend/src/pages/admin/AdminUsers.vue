<template>
  <div>
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
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import 'element-plus/es/components/message-box/style/css'
import { Plus, EditPen, Delete } from '@element-plus/icons-vue'
import { api } from '../../utils/api'
import { useHeroDialog } from '../../utils/heroDialog'
import { isMobile } from '../../utils/viewport'
import { useAuth } from '../../stores/auth'

const auth = useAuth()
const saving = ref(false)

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
    users.value = await api.admin.users.list() || []
  } catch (e) {
    console.error(e)
    ElMessage.error('用户列表加载失败')
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
      await api.admin.users.update(editingUser.value.id, userForm.value)
      ElMessage.success('已保存')
    } else {
      const d = await api.admin.users.create(userForm.value)
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
  await api.admin.users.remove(row.id)
  ElMessage.success('已删除')
  loadUsers()
}

onMounted(loadUsers)
</script>

<style scoped>
.pane-head { display: flex; justify-content: flex-end; margin-bottom: 10px; }

/* ---- 移动端：表格单元格收紧，让状态/操作列留在屏内不必横向滑动（#29） ---- */
@media (max-width: 768px) {
  :deep(.el-table .cell) { padding: 0 8px; }
  :deep(.el-table td .el-button + .el-button) { margin-left: 2px; }
  :deep(.el-table .el-tag) { max-width: 100%; }
  :deep(.el-table .el-tag .el-tag__content) {
    min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
}
</style>
