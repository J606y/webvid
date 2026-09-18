<template>
  <div class="aurora"><span /><span /><span /><span /></div>

  <header v-if="showNav" class="topbar glass" :class="{ 'immersive-hide': immersive }">
    <router-link to="/" class="brand">
      <img class="brand-logo" src="/favicon.svg" alt="">
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
  <nav v-if="showNav" ref="tabRef" class="tabbar glass" :class="{ 'immersive-hide': immersive }"
    @touchstart="onTabDown" @touchmove="onTabMove" @touchend="onTabUp" @touchcancel="onTabCancel">
    <span class="tab-pill" :class="{ 'is-grabbed': tabGrab }" :style="tabPill" />
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
  User, ArrowDown, Setting, SwitchButton,
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

// ---- 移动端 Tab 滑块跟手 ----
// 原来是「点一下才滑过去」。iOS 的分段控件是滑块跟着手指实时走、松手吸附到最近一项。
// 拖动期间把 transition 临时设成 none 直接跟手 —— 不关的话每帧都在追一条 0.35s 的缓动，
// 手感是「拖不动」；松手交还 movePill（它设 transition:'' 回落到 CSS 的缓动）做吸附。
// 只动 transform，无 filter 参与，iOS 安全（#35 教训）。
let tabDrag = null
// 手指按在 dock 上的整段时间里滑块涨一圈（iOS 分段控件被抓住的手感），松手落回
const tabGrab = ref(false)

function onTabDown(e) {
  if (e.touches.length !== 1 || !tabRef.value) return
  const items = [...tabRef.value.querySelectorAll('.tab-item')]
  const active = tabRef.value.querySelector('.tab-item.active')
  if (!items.length || !active) return // 后台/播放页没有选中项，没东西可拖
  tabGrab.value = true
  tabDrag = {
    fromX: e.touches[0].clientX,
    baseX: active.offsetLeft,
    items,
    min: items[0].offsetLeft,
    max: items[items.length - 1].offsetLeft,
    moved: 0,
  }
}

function onTabMove(e) {
  if (!tabDrag || !e.touches.length) return
  const dx = e.touches[0].clientX - tabDrag.fromX
  tabDrag.moved = Math.max(tabDrag.moved, Math.abs(dx))
  const x = Math.min(tabDrag.max, Math.max(tabDrag.min, tabDrag.baseX + dx))
  // 不能整条 transition:none —— 那会把 scale 也一起掐掉，滑块变成硬邦邦地瞬间涨/缩。
  // 只留 scale 那一档：transform 不在列表里即为瞬时，跟手；涨缩仍有缓动。
  tabPill.value = {
    ...tabPill.value,
    transform: `translateX(${x}px)`,
    transition: 'scale 0.22s cubic-bezier(0.32, 0.72, 0, 1), box-shadow 0.22s ease',
  }
}

function onTabUp(e) {
  const d = tabDrag
  tabDrag = null
  tabGrab.value = false
  if (!d) return
  // 几乎没动就是在点，交给 router-link 自己导航，滑块随路由变化就位
  if (d.moved < 8) {
    nextTick(() => movePill(tabRef.value, tabPill))
    return
  }
  // 真拖过了：松手这一下不该再触发底下 router-link 的跳转
  e.preventDefault()
  const dx = (e.changedTouches[0]?.clientX ?? d.fromX) - d.fromX
  const x = Math.min(d.max, Math.max(d.min, d.baseX + dx))
  // 吸附到滑块此刻压着的那一项（四项等宽，按步长取整即可）
  const step = d.items[0].offsetWidth || 1
  const idx = Math.min(d.items.length - 1, Math.max(0, Math.round((x - d.min) / step)))
  const to = navs[idx]?.to
  if (to && to !== route.path) router.push(to)
  // 原地松手不会触发路由变化，滑块得自己摆回去，否则僵在手指离开的位置
  else nextTick(() => movePill(tabRef.value, tabPill))
}

function onTabCancel() {
  tabGrab.value = false
  if (!tabDrag) return
  tabDrag = null
  nextTick(() => movePill(tabRef.value, tabPill))
}
watch(() => route.path, () => nextTick(syncPills))

function onUserCmd(cmd) {
  if (cmd === 'logout') auth.logout()
  else if (cmd === 'admin') router.push('/@admin')
}

// iOS Safari 只在元素或其祖先挂过 touchstart 时才会触发 :active —— 挂一个空的被动监听
// 把按压反馈打开（卡片按下放大，见 glass.css）。它不拦截、不 preventDefault，
// 对任何既有事件处理都没有影响。
const enableTapActive = () => {}

onMounted(() => {
  app.fetchPublic()
  syncPills()
  window.addEventListener('resize', syncPills)
  document.addEventListener('touchstart', enableTapActive, { passive: true })
})
onUnmounted(() => {
  window.removeEventListener('resize', syncPills)
  document.removeEventListener('touchstart', enableTapActive)
})
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
.brand-logo { width: 22px; height: 22px; display: block; } /* 圆角已烙在 svg 里 */
.nav { display: flex; gap: 4px; position: relative; }
/* dock 指示丸：选中背景不再画在项上，由这枚胶囊在项间滑动（transform+width 过渡） */
.nav-pill {
  position: absolute; top: 0; bottom: 0; left: 0;
  border-radius: 10px;
  background: rgba(var(--accent-rgb), 0.22);
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

/* 导航项都是 router-link，iOS 长按会弹 Safari 的链接预览浮层，盖住 dock 也打断拖动。
   两个属性都可继承，设在容器上一次盖住所有项。缘由见 docs/BUGS-0918.md。 */
.topbar, .tabbar { -webkit-touch-callout: none; -webkit-user-select: none; user-select: none; }

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
    background: rgba(var(--accent-rgb), 0.22);
    transition: transform 0.35s cubic-bezier(0.32, 0.72, 0, 1),
      width 0.35s cubic-bezier(0.32, 0.72, 0, 1),
      scale 0.22s cubic-bezier(0.32, 0.72, 0, 1),
      box-shadow 0.22s ease, opacity 0.15s ease;
    pointer-events: none;
  }
  /* 按住/拖动时滑块涨一圈、投影加深，像被指尖提起来（同 iOS 分段控件）。
     用独立的 scale 属性而不是并进 transform：拖动期间 transform 必须瞬时跟手
     （从 transition 列表里摘掉），而涨缩要缓动，两者得分开管。
     涨的那 6% 落在 .tabbar 的 5px 内边距里，不会溢出圆角；只动 scale/box-shadow，
     无 filter 参与，iOS 安全（#35 教训）。 */
  .tab-pill.is-grabbed {
    scale: 1.06;
    box-shadow: 0 6px 18px rgba(0, 0, 0, 0.28);
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
