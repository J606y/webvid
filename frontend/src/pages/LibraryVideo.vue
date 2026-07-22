<template>
  <div class="page">
    <!-- Featured 大幅轮播：随机 5 个视频（根视图才显示，Infuse 首页顶部横幅）。
         轮播骨架抽到 FeaturedCarousel：整块点击 → openDetail（取 .feat-item 作转场锚），
         右下角「立即播放」按钮经 action 插槽。ref=carousel 供 swipe 触屏翻页（见 useMediaLibrary）。 -->
    <FeaturedCarousel v-if="isHome && hero.length" ref="carousel" :items="hero"
      :height="featHeight" :autoplay="heroActive" :swipe="swipe"
      @select="(v, i, ev) => openDetail(v, ev)">
      <template #action="{ item }">
        <el-button type="primary" size="large" round :icon="VideoPlay" class="hero-btn"
          @click.stop="$router.push(playRoute(item.path))">
          立即播放
        </el-button>
      </template>
    </FeaturedCarousel>

    <!-- 目录/全部/最近播放视图大标题（Infuse 资料库式） -->
    <div v-if="!isHome" class="lib-head">
      <button class="back-btn" @click="$router.push('/library/video')">
        <el-icon :size="18"><ArrowLeft /></el-icon>
      </button>
      <h1 class="lib-title">{{ all ? '所有视频' : historyView ? '最近播放' : dirName }}</h1>
      <div class="spacer" />
      <!-- 最近播放视图按播放时间天然有序，不给排序 -->
      <el-select v-if="!historyView" v-model="sort" size="default" style="width: 130px">
        <el-option label="最新在前" value="modified" />
        <el-option label="按名称" value="name" />
      </el-select>
    </div>

    <!-- 空态引导 -->
    <div v-if="loaded && !grid.length" class="empty glass glass-panel">
      <template v-if="historyView">
        <el-icon :size="52" class="dim"><VideoCamera /></el-icon>
        <p>还没有播放记录</p>
        <p class="dim">播放过的视频会按时间顺序出现在这里</p>
      </template>
      <template v-else>
        <el-icon :size="52" class="dim"><FolderOpened /></el-icon>
        <p>这里还没有视频</p>
        <p class="dim">上传一些视频，这里会自动变成你的私人影院</p>
        <router-link to="/files">
          <el-button type="primary" round>去文件管理</el-button>
        </router-link>
      </template>
    </div>

    <!-- 「最近添加」横向货架 -->
    <section v-if="isHome && recent.length" class="shelf">
      <div class="shelf-head"><h2>最近添加</h2></div>
      <div class="shelf-row">
        <VideoCard v-for="v in recent" :key="v.path" class="shelf-card" :video="v"
          @open="openDetail(v, $event)" @play="$router.push(playRoute(v.path))">
          {{ formatTime(v.modified) }}
        </VideoCard>
      </div>
    </section>

    <!-- 「最近播放」横向货架（按本用户播放历史），「查看更多」进 50 条完整视图 -->
    <section v-if="isHome && played.length" class="shelf">
      <div class="shelf-head">
        <h2>最近播放</h2>
        <div class="shelf-ops">
          <button class="see-more"
            @click="$router.push({ path: '/library/video', query: { played: '1' } })">
            查看更多<el-icon :size="14"><ArrowRight /></el-icon>
          </button>
        </div>
      </div>
      <div class="shelf-row">
        <VideoCard v-for="v in played" :key="v.path" class="shelf-card" :video="v" show-progress
          @open="openDetail(v, $event)" @play="$router.push(playRoute(v.path))">
          {{ subText(v) }}
        </VideoCard>
      </div>
    </section>

    <!-- 「所有视频」网格：主页随机挑选 200（每次进入重新抽），「查看全部」进完整有序列表（滚动加载） -->
    <section v-if="grid.length" class="shelf">
      <div v-if="isHome" class="shelf-head">
        <h2>所有视频</h2>
        <div class="shelf-ops">
          <button class="see-all"
            @click="$router.push({ path: '/library/video', query: { all: '1' } })">
            查看全部<el-icon :size="14"><ArrowRight /></el-icon>
          </button>
        </div>
      </div>
      <div class="v-grid">
        <VideoCard v-for="v in grid" :key="v.path" :video="v" show-progress
          @open="openDetail(v, $event)" @play="$router.push(playRoute(v.path))">
          <!-- 历史视图副标题=「看到 x% · 时间」；否则大小·时间（显式两分支，不依赖空插槽回落） -->
          <template v-if="historyView">{{ subText(v) }}</template>
          <template v-else>{{ formatSize(v.size) }} · {{ formatTime(v.modified) }}</template>
        </VideoCard>
      </div>
    </section>

    <div ref="sentinel" class="sentinel" />
    <div v-if="loading && grid.length" class="dim loading-more">加载中…</div>

    <!-- 视频详情二级卡片（反馈#16） -->
    <VideoDetailCard ref="detail" />
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ArrowLeft, ArrowRight, VideoCamera, VideoPlay, FolderOpened } from '@element-plus/icons-vue'
import { api } from '../utils/api'
import VideoDetailCard from '../components/VideoDetailCard.vue'
import VideoCard from '../components/VideoCard.vue'
import FeaturedCarousel from '../components/FeaturedCarousel.vue'
import { playRoute } from '../utils/path'
import { formatSize, formatTime, progressPct } from '../utils/file'
import { useMediaLibrary } from '../composables/useMediaLibrary'

defineOptions({ name: 'LibraryVideo' }) // App.vue keep-alive include 按此名匹配

const hero = ref([])   // Featured 随机推荐
const recent = ref([]) // 最近添加货架
const played = ref([]) // 最近播放（本用户历史）
const detail = ref(null) // 详情卡片组件

async function loadStatic() {
  try {
    // Featured/最近添加/最近播放三路互不依赖，并发请求缩短首屏等待
    const [r, d, h] = await Promise.all([
      // Featured：整库随机抽 5 个（封面 object-fit:cover，竖屏也能铺满横幅）
      api.media.list({ kind: 'video', limit: 5, sort: 'random' }),
      api.media.list({ kind: 'video', limit: 12, sort: 'modified', order: 'desc' }),
      api.media.history({ kind: 'video', limit: 12 }),
    ])
    hero.value = r.items || []
    recent.value = d.items || []
    played.value = h.items || []
  } catch (e) {
    // axios 响应拦截器已负责弹 toast，这里只吞掉避免未捕获 rejection
    console.error(e)
  }
}

// 共享骨架：网格分页 / 视图切换 / 无限滚动 / 轮播 / infuse-mode（见 composables/useMediaLibrary）
const { grid, loaded, loading, sort, sentinel, carousel, heroActive, all, historyView, isHome, dirName, featHeight, swipe } =
  useMediaLibrary({ kind: 'video', routePath: '/library/video', historyKey: 'played', dirDefault: '视频库', historyCap: 50, loadStatic })

// 把点击来源的缩略图元素交给详情卡，做 iOS 式「从哪来回哪去」转场：
// 视频卡取 16:9 封面框 .art（与卡片顶部封面区形状吻合），Featured 横幅取整块
function openDetail(v, ev) {
  const t = ev?.currentTarget
  detail.value?.open(v, t?.querySelector?.('.art') || t || null)
}

// pct 续播进度百分比（0 = 无进度不画条）
function pct(v) {
  return progressPct(v?.position, v?.duration)
}
// subText 货架/进度视图副标题：有续播位置显示「看到 x% · 时间」，否则只显示播放时间
function subText(v) {
  const p = pct(v)
  return (p ? `看到 ${p}% · ` : '') + formatTime(v.played_at) + ' 播放'
}

onMounted(() => import('./Play.vue')) // 预热播放页 chunk（含 ArtPlayer），首次点视频不用现下
</script>

<style scoped src="../assets/media-library.css"></style>

<style scoped>
/* ---- 视频卡片布局（卡片本体样式见 VideoCard.vue） ---- */
.shelf-card { flex: 0 0 264px; }

/* ---- 所有视频网格 ---- */
.v-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(216px, 1fr));
  gap: 20px 16px;
}

/* ---- 移动端（卡片布局；卡片内文字尺寸见 VideoCard.vue） ---- */
@media (max-width: 768px) {
  .shelf-card { flex: 0 0 190px; }
  .v-grid { grid-template-columns: repeat(2, 1fr); gap: 14px 10px; }
}
</style>
