import { ref, computed, watch, onMounted, onUnmounted, onActivated, onDeactivated } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../utils/api'
import { isMobile } from '../utils/viewport'
import { swipeHandlers } from '../utils/swipe'

const PAGE = 120
const HOME_CAP = 200 // 主页网格默认展示上限，看更多走「查看全部」

// useMediaLibrary 抽出视频库 / 照片墙两页共享的骨架：分页网格 + 目录/全部/历史视图切换 +
// 无限滚动 + 随机推荐轮播 + keep-alive 下的 query 冻结与 infuse-mode 类。
// 差异由参数注入：
//   kind        'video' | 'image'
//   routePath   本页路径（keep-alive 驻留期 query 冻结判断用）
//   historyKey  历史视图的 query 键：'played'（最近播放）| 'viewed'（最近查看）
//   dirDefault  目录名兜底（'视频库' | '照片墙'）
//   historyCap  历史视图一次取的条数
//   loadStatic  主页 hero/货架加载回调（页面自备，因两页货架构成不同）
export function useMediaLibrary(opts) {
  const { kind, routePath, historyKey, dirDefault, historyCap = 50, loadStatic } = opts
  const route = useRoute()

  const grid = ref([]) // 主网格（分页累积）
  const loaded = ref(false)
  const loading = ref(false)
  const hasMore = ref(true)
  const offset = ref(0)
  const sort = ref('modified')
  const sentinel = ref(null)
  const carousel = ref(null) // 随机推荐横幅，触屏滑动调其 prev/next
  const heroActive = ref(true) // 轮播自动播放开关：keep-alive 切走时置 false 暂停空转
  let gen = 0 // 请求代际：dir/sort 切换后丢弃在途响应

  // el-carousel 高度只收 prop，媒体查询够不着，用 isMobile 切换
  const featHeight = computed(() => (isMobile.value ? '240px' : '420px'))
  // 触屏左右滑动切换随机推荐（左滑=下一张，右滑=上一张）
  const swipe = swipeHandlers((dir) => (dir === 'left' ? carousel.value?.next() : carousel.value?.prev()))

  // keep-alive 驻留期路由切走时 query 会变空——冻结取值，返回后值未变就不触发重载
  const onPage = computed(() => route.path === routePath)
  const dir = computed((prev) => (onPage.value ? route.query.dir || '' : prev ?? ''))
  const all = computed((prev) => (onPage.value ? route.query.all === '1' : prev ?? false))
  const historyView = computed((prev) => (onPage.value ? route.query[historyKey] === '1' : prev ?? false))
  const isHome = computed(() => !dir.value && !all.value && !historyView.value)
  const dirName = computed(() => dir.value.split('/').filter(Boolean).pop() || dirDefault)

  async function loadMore(reset = false) {
    if (reset) {
      gen++
      grid.value = []
      offset.value = 0
      hasMore.value = true
      loaded.value = false
      loading.value = false
    }
    if (loading.value || !hasMore.value) return
    loading.value = true
    const g = gen
    try {
      // 历史视图（最近播放/查看）：一次取 historyCap 条，不分页
      if (historyView.value) {
        const d = await api.media.history({ kind, limit: historyCap })
        if (g !== gen) return
        grid.value = d.items || []
        hasMore.value = false
        return
      }
      const limit = isHome.value ? HOME_CAP : PAGE
      const params = { kind, limit, offset: offset.value }
      if (isHome.value) {
        params.sort = 'random' // 主页随机挑选；完整有序列表走「查看全部」
      } else {
        params.sort = sort.value
        params.order = sort.value === 'name' ? 'asc' : 'desc'
      }
      if (dir.value) params.parent = dir.value
      const d = await api.media.list(params)
      if (g !== gen) return
      const items = d.items || []
      grid.value.push(...items)
      offset.value += items.length
      hasMore.value = !isHome.value && items.length === limit
    } finally {
      if (g === gen) { loading.value = false; loaded.value = true }
    }
  }

  let io
  onMounted(() => {
    io = new IntersectionObserver(
      (es) => { if (es[0].isIntersecting) loadMore() },
      { rootMargin: '600px' },
    )
    if (sentinel.value) io.observe(sentinel.value)
  })
  onUnmounted(() => io?.disconnect())
  // keep-alive 下 body 样式类随激活状态切换，切走时不残留
  // keep-alive 激活/驻留：切换 infuse-mode 类；同时暂停/恢复轮播自动播放，
  // 避免页面被缓存驻留期 el-carousel 的 autoplay timer 仍每 6s 空转。
  onActivated(() => { document.body.classList.add('infuse-mode'); heroActive.value = true })
  onDeactivated(() => { document.body.classList.remove('infuse-mode'); heroActive.value = false })

  watch([dir, all, historyView], () => {
    window.scrollTo({ top: 0 })
    loadMore(true)
    if (isHome.value) loadStatic?.()
  }, { immediate: true })
  watch(sort, () => loadMore(true))

  return {
    grid, loaded, loading, sort, sentinel, carousel, heroActive,
    dir, all, historyView, isHome, dirName, featHeight, swipe, loadMore,
  }
}
