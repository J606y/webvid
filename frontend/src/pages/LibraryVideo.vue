<template>
  <div class="page">
    <!-- Featured 大幅轮播：最近 5 个视频（根视图才显示，Infuse 首页顶部横幅） -->
    <!-- 移动端无 hover 箭头，绑触屏左右滑动切换（swipe.onTouch* 落到 el-carousel 根元素） -->
    <el-carousel v-if="isHome && hero.length" ref="carousel" class="feat" :height="featHeight"
      :interval="6000" :autoplay="heroActive" arrow="hover"
      @touchstart="swipe.onTouchstart" @touchend="swipe.onTouchend">
      <el-carousel-item v-for="v in hero" :key="v.path">
        <div class="feat-item" @click="openDetail(v, $event)">
          <img :src="thumbUrl(v.path, 1200)" class="feat-img" @error="hideImg" />
          <div class="feat-mask" />
          <div class="feat-info">
            <div class="feat-kicker">随机推荐</div>
            <div class="feat-title">{{ stripExt(v.name) }}</div>
            <div class="dim feat-meta">{{ formatTime(v.modified) }} · {{ formatSize(v.size) }}</div>
            <el-button type="primary" size="large" round :icon="VideoPlay" class="hero-btn"
              @click.stop="$router.push(playRoute(v.path))">
              立即播放
            </el-button>
          </div>
        </div>
      </el-carousel-item>
    </el-carousel>

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
        <div v-for="v in recent" :key="v.path" class="v-card shelf-card"
          @click="openDetail(v, $event)">
          <div class="art">
            <img :src="thumbUrl(v.path, 480)" loading="lazy" @error="hideImg" />
            <div class="thumb-fallback abs"><el-icon :size="30"><VideoCamera /></el-icon></div>
            <el-icon class="play" :size="38"
              @click.stop="$router.push(playRoute(v.path))"><VideoPlay /></el-icon>
          </div>
          <div class="v-name" :title="v.name">{{ stripExt(v.name) }}</div>
          <div class="dim v-sub">{{ formatTime(v.modified) }}</div>
        </div>
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
        <div v-for="v in played" :key="v.path" class="v-card shelf-card"
          @click="openDetail(v, $event)">
          <div class="art">
            <img :src="thumbUrl(v.path, 480)" loading="lazy" @error="hideImg" />
            <div class="thumb-fallback abs"><el-icon :size="30"><VideoCamera /></el-icon></div>
            <el-icon class="play" :size="38"
              @click.stop="$router.push(playRoute(v.path))"><VideoPlay /></el-icon>
            <div v-if="pct(v)" class="prog-bar"><span :style="{ width: pct(v) + '%' }" /></div>
          </div>
          <div class="v-name" :title="v.name">{{ stripExt(v.name) }}</div>
          <div class="dim v-sub">{{ subText(v) }}</div>
        </div>
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
        <div v-for="v in grid" :key="v.path" class="v-card"
          @click="openDetail(v, $event)">
          <div class="art">
            <img :src="thumbUrl(v.path, 480)" loading="lazy" @error="hideImg" />
            <div class="thumb-fallback abs"><el-icon :size="30"><VideoCamera /></el-icon></div>
            <el-icon class="play" :size="38"
              @click.stop="$router.push(playRoute(v.path))"><VideoPlay /></el-icon>
            <div v-if="pct(v)" class="prog-bar"><span :style="{ width: pct(v) + '%' }" /></div>
          </div>
          <div class="v-name" :title="v.name">{{ stripExt(v.name) }}</div>
          <div class="dim v-sub">
            <template v-if="historyView">{{ subText(v) }}</template>
            <template v-else>{{ formatSize(v.size) }} · {{ formatTime(v.modified) }}</template>
          </div>
        </div>
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
import http from '../api/http'
import VideoDetailCard from '../components/VideoDetailCard.vue'
import { thumbUrl, playRoute } from '../utils/path'
import { formatSize, formatTime, hideImg, stripExt } from '../utils/file'
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
      http.get('/media/list', { params: { kind: 'video', limit: 5, sort: 'random' } }),
      http.get('/media/list', { params: { kind: 'video', limit: 12, sort: 'modified', order: 'desc' } }),
      http.get('/media/history', { params: { kind: 'video', limit: 12 } }),
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
  if (!v?.duration || !v?.position) return 0
  return Math.min(100, Math.round((v.position / v.duration) * 100))
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
/* ---- 视频卡片（16:9 画框 + 播放钮 + 续播条） ---- */
.shelf-card { flex: 0 0 264px; }
.v-card { cursor: pointer; min-width: 0; }
.art {
  position: relative; aspect-ratio: 16/9;
  border-radius: 12px; overflow: hidden;
  background: #14141d;
  transition: transform 0.22s ease, box-shadow 0.22s ease;
}
.v-card:hover .art {
  transform: scale(1.045);
  box-shadow: 0 10px 30px rgba(0, 0, 0, 0.55), 0 0 0 2px rgba(255, 255, 255, 0.35);
}
.play {
  position: absolute; z-index: 2; left: 50%; top: 50%;
  transform: translate(-50%, -50%);
  color: #fff; opacity: 0; transition: opacity 0.2s;
  filter: drop-shadow(0 2px 10px rgba(0, 0, 0, 0.7));
}
.v-card:hover .play { opacity: 1; }
/* 触屏没有 hover：隐形的播放图标会拦截卡片中心的点击（点哪儿决定进播放还是弹详情），
   整个移除——统一「点卡片=详情，详情里立即播放」 */
@media (hover: none) {
  .play { display: none; }
}
/* 续播进度条：贴缩略图底沿，暗底 + accent 已看比例 */
.prog-bar {
  position: absolute; z-index: 2; left: 0; right: 0; bottom: 0;
  height: 4px; background: rgba(0, 0, 0, 0.5);
}
.prog-bar span {
  display: block; height: 100%;
  background: var(--accent, #7aa2ff);
  border-radius: 0 2px 2px 0;
}
.v-name {
  margin-top: 9px; font-size: 13.5px; font-weight: 600;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.v-sub { font-size: 12px; margin-top: 2px; }

/* ---- 所有视频网格 ---- */
.v-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(216px, 1fr));
  gap: 20px 16px;
}

/* ---- 移动端（视频卡片专属） ---- */
@media (max-width: 768px) {
  .shelf-card { flex: 0 0 190px; }
  .v-grid { grid-template-columns: repeat(2, 1fr); gap: 14px 10px; }
  .v-name { font-size: 12.5px; margin-top: 6px; }
  .v-sub { font-size: 11px; }
}
</style>
