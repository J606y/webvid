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

      <!-- 任务设置（线程数 + 限速，独立于站点设置，但与其共用 settings 对象与保存） -->
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

      <!-- 存储管理（表格 + 编辑弹窗 + Telegram 登录弹窗） -->
      <el-tab-pane label="存储管理" name="storage">
        <AdminStorage />
      </el-tab-pane>

      <!-- 用户管理（表格 + 编辑弹窗） -->
      <el-tab-pane label="用户管理" name="users">
        <AdminUsers />
      </el-tab-pane>

      <!-- 索引管理（文件索引 + 封面/源信息预载） -->
      <el-tab-pane label="索引管理" name="index">
        <AdminIndex :active="tab === 'index'" />
      </el-tab-pane>
    </el-tabs>
    <p v-if="app.version" class="dim version-tip">v{{ app.version }}</p>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { api } from '../utils/api'
import { useApp } from '../stores/app'
import AdminStorage from './admin/AdminStorage.vue'
import AdminUsers from './admin/AdminUsers.vue'
import AdminIndex from './admin/AdminIndex.vue'

const app = useApp()
const tab = ref('site')

// ---- 站点设置（标题）+ 任务设置（线程数 + 限速）----
// 两 Tab 共用同一 settings 对象与 saveSite（都 PUT /admin/settings 全量）。
// 后端：site_title 必填，worker/限速为指针字段、缺省保原值，保存即热生效。
const site = ref({
  site_title: '', copy_workers: 2, offline_workers: 2, upload_workers: 2,
  copy_speed_kb: 0, upload_speed_kb: 0, download_speed_kb: 0,
})
async function loadSite() {
  try {
    site.value = await api.admin.settings.get()
  } catch (e) {
    console.error(e)
    ElMessage.error('站点设置加载失败')
  }
}
async function saveSite() {
  if (!site.value.site_title) return ElMessage.warning('站点标题不能为空')
  await api.admin.settings.save(site.value)
  ElMessage.success('已保存并生效')
  app.fetchPublic()
}

onMounted(loadSite)
</script>

<style scoped>
.admin-page { max-width: 1000px; min-height: 100vh; display: flex; flex-direction: column; }
.tabs { padding: 10px 20px 20px; }
.version-tip { margin-top: auto; padding-top: 14px; text-align: center; font-size: 12px; }
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

/* ---- 移动端 ---- */
@media (max-width: 768px) {
  .tabs { padding: 6px 12px 14px; }
  /* 五个 Tab 挤进窄屏：收紧内边距与字号，免出现左右滚动箭头 */
  :deep(.el-tabs__item) { padding: 0 8px; font-size: 12px; }
}
</style>
