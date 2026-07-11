<template>
  <div class="aurora"><span /><span /><span /><span /></div>

  <header v-if="showNav" class="topbar glass" :class="{ 'immersive-hide': immersive }">
    <router-link to="/" class="brand">
      <el-icon :size="20"><Platform /></el-icon>
      <span>{{ app.siteTitle }}</span>
    </router-link>
    <nav ref="navRef" class="nav">
      <span class="nav-pill" :style="navPill" />
      <router-link v-for="n in navs" :key="n.to" :to="n.to"
        class="nav-item" :class="{ active: isActive(n) }">{{ n.label }}</router-link>
    </nav>
    <div class="spacer" />
    <el-dropdown trigger="click" @command="onUserCmd">
      <span class="user-chip">
        <el-icon><User /></el-icon>{{ auth.user?.username }}
        <el-icon><ArrowDown /></el-icon>
      </span>
      <template #dropdown>
        <el-dropdown-menu>
          <el-dropdown-item v-if="auth.isAdmin" command="admin">
            <el-icon><Setting /></el-icon>后台管理
          </el-dropdown-item>
          <el-dropdown-item divided command="logout">
            <el-icon><SwitchButton /></el-icon>退出登录
          </el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>
  </header>

  <!-- 移动端底部 Tab 栏（≤768px 显示，替代顶栏导航） -->
  <nav v-if="showNav" ref="tabRef" class="tabbar glass" :class="{ 'immersive-hide': immersive }">
    <span class="tab-pill" :style="tabPill" />
    <router-link v-for="n in navs" :key="n.to" :to="n.to"
      class="tab-item" :class="{ active: isActive(n) }">
      <el-icon :size="21"><component :is="n.icon" /></el-icon>
      <span>{{ n.label }}</span>
    </router-link>
  </nav>

  <!-- 列表页 keep-alive 驻留：进播放页再返回时数据与滚动位置原样保留 -->
  <router-view v-slot="{ Component }">
    <keep-alive :include="['LibraryVideo', 'LibraryPhotos', 'Files', 'Search']">
      <component :is="Component" />
    </keep-alive>
  </router-view>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  Platform, User, ArrowDown, Setting, SwitchButton,
  VideoCamera, Picture, Folder, Search,
} from '@element-plus/icons-vue'
import { useAuth } from './stores/auth'
import { useApp } from './stores/app'

const route = useRoute()
const router = useRouter()
const auth = useAuth()
const app = useApp()

const navs = [
  { to: '/library/video', label: '视频库', icon: VideoCamera },
  { to: '/library/photos', label: '照片墙', icon: Picture },
  { to: '/files', label: '文件', icon: Folder },
  { to: '/search', label: '搜索', icon: Search },
]

const showNav = computed(() => route.path !== '/login' && !!auth.user)
// 移动端播放页沉浸式：隐藏顶栏与底部 Tab 栏（页面内保留返回按钮），CSS 见 App/glass 媒体查询
const immersive = computed(() => route.path.startsWith('/play/'))

function isActive(n) {
  return route.path === n.to || route.path.startsWith(n.to + '/')
}

// ---- dock 指示丸（反馈#47）：一枚胶囊背景在选中项之间滑动 ----
// 桌面顶栏 .nav 与移动底部 .tabbar 共用一套逻辑：量选中项 offsetLeft/offsetWidth，
// translateX 滑过去（width 过渡兼容桌面端不等宽项）；只动小元素的 transform/width，
// 无 filter 参与（iOS 安全，#35 教训）。无选中项（后台/播放页）淡出隐藏。
const navRef = ref(null)
const tabRef = ref(null)
const navPill = ref({ opacity: 0 })
const tabPill = ref({ opacity: 0 })

function movePill(container, style) {
  const el = container?.querySelector('.active')
  if (!el || !el.offsetWidth) { style.value = { ...style.value, opacity: 0 }; return }
  // 从隐藏态出现（刷新进页/后台·播放页回来）直接就位淡入；项间切换才滑动
  const appearing = style.value.opacity !== 1
  style.value = {
    opacity: 1,
    width: `${el.offsetWidth}px`,
    transform: `translateX(${el.offsetLeft}px)`,
    transition: appearing ? 'opacity 0.15s ease' : '',
  }
}
function syncPills() {
  movePill(navRef.value, navPill)
  movePill(tabRef.value, tabPill)
}
watch(() => route.path, () => nextTick(syncPills))

function onUserCmd(cmd) {
  if (cmd === 'logout') auth.logout()
  else if (cmd === 'admin') router.push('/@admin')
}

onMounted(() => {
  app.fetchPublic()
  syncPills()
  window.addEventListener('resize', syncPills)
})
onUnmounted(() => window.removeEventListener('resize', syncPills))
</script>

<style scoped>
.topbar {
  position: fixed;
  top: 12px; left: 50%;
  transform: translateX(-50%);
  width: min(1440px, calc(100% - 32px));
  height: 56px;
  display: flex;
  align-items: center;
  gap: 20px;
  padding: 0 20px;
  z-index: 100;
  border-radius: 18px;
}
.brand {
  display: flex; align-items: center; gap: 8px;
  font-weight: 700; font-size: 17px; letter-spacing: 0.5px;
}
.nav { display: flex; gap: 4px; position: relative; }
/* dock 指示丸：选中背景不再画在项上，由这枚胶囊在项间滑动（transform+width 过渡） */
.nav-pill {
  position: absolute; top: 0; bottom: 0; left: 0;
  border-radius: 10px;
  background: rgba(122, 162, 255, 0.22);
  transition: transform 0.35s cubic-bezier(0.32, 0.72, 0, 1),
    width 0.35s cubic-bezier(0.32, 0.72, 0, 1), opacity 0.15s ease;
  pointer-events: none;
}
.nav-item {
  position: relative; z-index: 1;
  padding: 7px 14px;
  border-radius: 10px;
  font-size: 14px;
  color: var(--text-dim);
  transition: all 0.2s;
}
.nav-item:hover { color: var(--text-main); background: rgba(255, 255, 255, 0.07); }
.nav-item.active { color: #fff; }
.spacer { flex: 1; }
.user-chip {
  display: flex; align-items: center; gap: 6px;
  padding: 7px 12px; border-radius: 10px; cursor: pointer;
  font-size: 14px; color: var(--text-main);
  outline: none;
}
.user-chip:hover { background: rgba(255, 255, 255, 0.07); }

/* ---- 移动端：顶栏只留 brand+用户，导航下沉为底部 Tab 栏 ---- */
.tabbar { display: none; }
@media (max-width: 768px) {
  /* 播放页沉浸式：顶栏与底部 Tab 栏隐藏（页面内保留返回，Play.vue 自管安全区留白） */
  .topbar.immersive-hide,
  .tabbar.immersive-hide { display: none; }
  .topbar {
    top: calc(8px + env(safe-area-inset-top));
    width: calc(100% - 16px);
    height: 48px;
    padding: 0 14px;
    gap: 10px;
    border-radius: 15px;
  }
  .brand { font-size: 15px; }
  .nav { display: none; }
  .user-chip { padding: 6px 8px; }
  .tabbar {
    position: fixed;
    left: 50%; transform: translateX(-50%);
    bottom: calc(8px + env(safe-area-inset-bottom));
    z-index: 100;
    display: flex;
    align-items: stretch;
    width: calc(100% - 16px);
    max-width: 440px;
    height: 60px;
    padding: 5px;
    border-radius: 19px;
  }
  /* dock 指示丸（与桌面 .nav-pill 同机制）：top/bottom 对齐 .tabbar 的 5px 内边距，
     translateX 的 offsetLeft 本身含内边距，故 left:0 起算 */
  .tab-pill {
    position: absolute; top: 5px; bottom: 5px; left: 0;
    border-radius: 14px;
    background: rgba(122, 162, 255, 0.22);
    transition: transform 0.35s cubic-bezier(0.32, 0.72, 0, 1),
      width 0.35s cubic-bezier(0.32, 0.72, 0, 1), opacity 0.15s ease;
    pointer-events: none;
  }
  .tab-item {
    position: relative; z-index: 1;
    flex: 1;
    display: flex; flex-direction: column;
    align-items: center; justify-content: center; gap: 2px;
    border-radius: 14px;
    font-size: 11px;
    color: var(--text-dim);
    transition: all 0.2s;
    -webkit-tap-highlight-color: transparent;
  }
  .tab-item.active { color: #fff; }
}
</style>
