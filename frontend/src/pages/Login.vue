<template>
  <div class="login-wrap">
    <div class="login-card glass glass-panel">
      <div class="logo">
        <el-icon :size="34"><Platform /></el-icon>
      </div>
      <h1>{{ app.siteTitle }}</h1>
      <p class="dim sub">登录以继续</p>
      <el-form @submit.prevent="submit">
        <el-form-item>
          <el-input v-model="username" placeholder="用户名" size="large" :prefix-icon="User" autofocus />
        </el-form-item>
        <el-form-item>
          <el-input v-model="password" type="password" placeholder="密码" size="large"
            :prefix-icon="Lock" show-password @keyup.enter="submit" />
        </el-form-item>
        <el-button type="primary" size="large" class="submit" :loading="loading" @click="submit">
          登 录
        </el-button>
      </el-form>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Platform, User, Lock } from '@element-plus/icons-vue'
import { useAuth } from '../stores/auth'
import { useApp } from '../stores/app'

const router = useRouter()
const auth = useAuth()
const app = useApp()

const username = ref('')
const password = ref('')
const loading = ref(false)

async function submit() {
  if (!username.value || !password.value) return ElMessage.warning('请输入用户名和密码')
  loading.value = true
  try {
    await auth.login(username.value, password.value)
    router.push('/')
  } catch (e) {
    ElMessage.error(e.message || '登录失败')
  } finally {
    loading.value = false
  }
}

onMounted(() => app.fetchPublic())
</script>

<style scoped>
.login-wrap {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}
.login-card {
  width: min(380px, 100%);
  padding: 44px 40px 36px;
  text-align: center;
}
@media (max-width: 768px) {
  .login-card { padding: 36px 26px 30px; }
}
.logo {
  width: 64px; height: 64px;
  margin: 0 auto 14px;
  display: flex; align-items: center; justify-content: center;
  border-radius: 20px;
  background: linear-gradient(135deg, rgba(122, 162, 255, 0.35), rgba(170, 100, 255, 0.3));
  border: 1px solid rgba(255, 255, 255, 0.2);
}
h1 { margin: 0 0 4px; font-size: 24px; font-weight: 700; }
.sub { margin: 0 0 26px; font-size: 13px; }
.submit { width: 100%; margin-top: 4px; font-size: 15px; }
</style>
