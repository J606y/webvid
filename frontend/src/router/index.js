import { createRouter, createWebHistory } from 'vue-router'
import { useAuth } from '../stores/auth'

const routes = [
  { path: '/login', component: () => import('../pages/Login.vue') },
  { path: '/', redirect: '/library/video' },
  { path: '/library/video', component: () => import('../pages/LibraryVideo.vue'), meta: { requiresAuth: true } },
  { path: '/library/photos', component: () => import('../pages/LibraryPhotos.vue'), meta: { requiresAuth: true } },
  { path: '/play/:path(.*)*', component: () => import('../pages/Play.vue'), meta: { requiresAuth: true } },
  { path: '/files/:path(.*)*', component: () => import('../pages/Files.vue'), meta: { requiresAuth: true } },
  { path: '/search', component: () => import('../pages/Search.vue'), meta: { requiresAuth: true } },
  { path: '/@admin', component: () => import('../pages/Admin.vue'), meta: { requiresAuth: true, admin: true } },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
  // 后退/前进恢复原滚动位置（列表页由 App.vue keep-alive 驻留，返回时高度立即可用）
  scrollBehavior(to, from, savedPosition) {
    return savedPosition || { top: 0 }
  },
})

router.beforeEach(async (to) => {
  if (!to.meta.requiresAuth) return true
  const auth = useAuth()
  if (!auth.token) return '/login'
  if (!auth.user) {
    try {
      await auth.fetchMe()
    } catch {
      return '/login'
    }
  }
  if (to.meta.admin && auth.user?.role !== 'admin') return '/'
  return true
})

export default router
