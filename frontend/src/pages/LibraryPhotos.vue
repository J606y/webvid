<template>
  <div class="page">
    <!-- Featured 大幅轮播：随机 5 张照片（根视图才显示，Infuse 首页顶部横幅）。
         轮播骨架抽到 FeaturedCarousel：整块点击 → openList 开灯箱（选择器按 DOM 类名定位缩略图做 hero 转场），
         右下角「查看大图」按钮经 action 插槽。ref=carousel 供 swipe 触屏翻页（见 useMediaLibrary）。 -->
    <FeaturedCarousel v-if="isHome && hero.length" ref="carousel" :items="hero"
      :height="featHeight" :autoplay="heroActive" :swipe="swipe"
      @select="(p, i) => openList(hero, i, 1200, '.feat .el-carousel__item .feat-img')">
      <template #action>
        <el-button type="primary" size="large" round :icon="View" class="hero-btn">
          查看大图
        </el-button>
      </template>
    </FeaturedCarousel>

    <!-- 目录/全部/最近查看视图大标题（Infuse 资料库式） -->
    <div v-if="!isHome" class="lib-head">
      <button class="back-btn" @click="$router.push('/library/photos')">
        <el-icon :size="18"><ArrowLeft /></el-icon>
      </button>
      <h1 class="lib-title">{{ all ? '所有照片' : historyView ? '最近查看' : dirName }}</h1>
      <div class="spacer" />
      <!-- 最近查看视图按查看时间天然有序，不给排序 -->
      <el-select v-if="!historyView" v-model="sort" size="default" style="width: 130px">
        <el-option label="最新在前" value="modified" />
        <el-option label="按名称" value="name" />
      </el-select>
    </div>

    <!-- 首屏加载反馈：loaded 为 false 期间原本整页空白，挂在慢速云盘（OneDrive/Google Drive）
         上首屏要等数秒，容易被当成卡死或者库是空的。延迟 200ms 才亮起：本地存储通常秒回，
         这块面板不会被看见闪一下；跟空态共用同一只 .glass glass-panel，两者互斥（一个要求
         loaded，一个要求 !loaded）不会同框。 -->
    <div v-if="!loaded && showLoading" class="empty glass glass-panel">
      <el-icon :size="36" class="dim is-loading"><Loading /></el-icon>
      <p class="dim">加载中…</p>
    </div>

    <!-- 空态引导 -->
    <div v-if="loaded && !grid.length" class="empty glass glass-panel">
      <template v-if="historyView">
        <el-icon :size="52" class="dim"><Picture /></el-icon>
        <p>还没有查看记录</p>
        <p class="dim">浏览过的照片会按时间顺序出现在这里</p>
      </template>
      <template v-else>
        <el-icon :size="52" class="dim"><FolderOpened /></el-icon>
        <p>这里还没有图片</p>
        <p class="dim">上传一些照片，这里会自动变成你的私人相册</p>
        <router-link to="/files">
          <el-button type="primary" round>去文件管理</el-button>
        </router-link>
      </template>
    </div>

    <!-- 「最近添加」横向货架（按修改时间倒序，对齐视频库的信息层级），让刚上传的照片有个快捷入口；
         卡片沿用本页「最近查看」货架自己的 p-card 写法。转场锚点选择器加 shelf-recent 前缀，
         跟下面「最近查看」货架的同名 .p-card 结构区分开，避免 querySelectorAll 把两条货架的图混在一起数 -->
    <section v-if="isHome && recent.length" class="shelf shelf-recent">
      <div class="shelf-head"><h2>最近添加</h2></div>
      <div class="shelf-row">
        <div v-for="(p, i) in recent" :key="p.path" class="p-card shelf-card"
          @click="openList(recent, i, 480, '.shelf-recent .p-card .art img')">
          <div class="art">
            <img :src="thumbUrl(p.path, 480)" loading="lazy" @error="hideImg" />
            <div class="thumb-fallback abs"><el-icon :size="30"><Picture /></el-icon></div>
          </div>
          <div class="p-name" :title="p.name">{{ stripExt(p.name) }}</div>
          <div class="dim p-sub">{{ formatTime(p.modified) }} 添加</div>
        </div>
      </div>
    </section>

    <!-- 「最近查看」横向货架（按本用户查看历史），「查看更多」进 50 张完整视图 -->
    <section v-if="isHome && viewed.length" class="shelf shelf-viewed">
      <div class="shelf-head">
        <h2>最近查看</h2>
        <div class="shelf-ops">
          <button class="see-more"
            @click="$router.push({ path: '/library/photos', query: { viewed: '1' } })">
            查看更多<el-icon :size="14"><ArrowRight /></el-icon>
          </button>
        </div>
      </div>
      <div class="shelf-row">
        <div v-for="(p, i) in viewed" :key="p.path" class="p-card shelf-card"
          @click="openList(viewed, i, 480, '.shelf-viewed .p-card .art img')">
          <div class="art">
            <img :src="thumbUrl(p.path, 480)" loading="lazy" @error="hideImg" />
            <div class="thumb-fallback abs"><el-icon :size="30"><Picture /></el-icon></div>
          </div>
          <div class="p-name" :title="p.name">{{ stripExt(p.name) }}</div>
          <div class="dim p-sub">{{ formatTime(p.played_at) }} 查看</div>
        </div>
      </div>
    </section>

    <!-- 「所有照片」网格：主页随机挑选 200（每次进入重新抽），「查看全部」进完整有序列表（滚动加载） -->
    <section v-if="grid.length" class="shelf">
      <div v-if="isHome" class="shelf-head">
        <h2>所有照片</h2>
        <div class="shelf-ops">
          <button class="see-all"
            @click="$router.push({ path: '/library/photos', query: { all: '1' } })">
            查看全部<el-icon :size="14"><ArrowRight /></el-icon>
          </button>
        </div>
      </div>
      <div class="photo-grid">
        <div v-for="(img, i) in grid" :key="img.path" class="cell" @click="openList(grid, i)">
          <img :src="thumbUrl(img.path, 320)" loading="lazy" @error="hideImg" />
          <div class="thumb-fallback abs"><el-icon :size="26"><Picture /></el-icon></div>
        </div>
      </div>
    </section>

    <div ref="sentinel" class="sentinel" />
    <div v-if="loading && grid.length" class="dim loading-more">加载中…</div>
  </div>
</template>

<script setup>
import { ref, onUnmounted, watch } from 'vue'
import { ArrowLeft, ArrowRight, Picture, View, FolderOpened, Loading } from '@element-plus/icons-vue'
import { api } from '../utils/api'
import FeaturedCarousel from '../components/FeaturedCarousel.vue'
import { thumbUrl } from '../utils/path'
import { formatTime, hideImg, stripExt } from '../utils/file'
import { openLightbox } from '../utils/lightbox'
import { useMediaLibrary } from '../composables/useMediaLibrary'
import { useApp } from '../stores/app'

defineOptions({ name: 'LibraryPhotos' }) // App.vue keep-alive include 按此名匹配

const app = useApp()

const hero = ref([])   // Featured 随机推荐
const recent = ref([]) // 最近添加货架
const viewed = ref([]) // 最近查看（本用户查看历史）

async function loadStatic() {
  try {
    // Featured 的取法跟随后台「媒体库首页」设置：早先这里恒发 random，
    // 后台设成「最新在前」也关不掉它，同一个开关在网格和横幅上两种表现。
    await app.ensurePublic()
    const feat = { kind: 'image', limit: 5, sort: app.mediaHomeSort }
    if (feat.sort === 'modified') feat.order = 'desc'
    // Featured/最近添加/最近查看三路互不依赖，并发请求缩短首屏等待
    const [r, d, h] = await Promise.all([
      // 整库抽 5 张（封面 object-fit:cover，竖图也能铺满横幅）
      api.media.list(feat),
      api.media.list({ kind: 'image', limit: 12, sort: 'modified', order: 'desc' }),
      // 最近查看：本用户查看历史（灯箱打开照片时上报，见 utils/lightbox），文件删/移后自然消失
      api.media.history({ kind: 'image', limit: 12 }),
    ])
    hero.value = r.items || []
    recent.value = d.items || []
    viewed.value = h.items || []
  } catch (e) {
    // axios 响应拦截器已负责弹 toast，这里只吞掉避免未捕获 rejection
    console.error(e)
  }
}

// 共享骨架：网格分页 / 视图切换 / 无限滚动 / 轮播 / infuse-mode（见 composables/useMediaLibrary）
const { grid, loaded, loading, sort, sentinel, carousel, heroActive, all, historyView, isHome, dirName, featHeight, swipe } =
  useMediaLibrary({ kind: 'image', routePath: '/library/photos', historyKey: 'viewed', dirDefault: '照片墙', historyCap: 50, loadStatic })

// showLoading：loaded 转 false 后延迟 200ms 才置真，进 Doherty 阈值内、又明显长于本地存储的
// 响应耗时，快速返回时定时器还没到就被 loaded=true 清掉，面板压根不会挂出来。
const showLoading = ref(false)
let loadingTimer = null
watch(loaded, (v) => {
  clearTimeout(loadingTimer)
  if (v) showLoading.value = false
  else loadingTimer = setTimeout(() => { showLoading.value = true }, 200)
}, { immediate: true })
onUnmounted(() => clearTimeout(loadingTimer))

// msize 传该列表正在展示的缩略图尺寸，灯箱占位图可直接命中浏览器缓存。
// sel 为该列表缩略图元素的选择器（querySelectorAll 文档序与 v-for 同序），
// 喂给灯箱做 iOS 同款 hero 转场（反馈#49）：从点击的缩略图放大、关闭缩回当前张处
function openList(list, i, msize = 320, sel = '.photo-grid .cell img') {
  openLightbox(list.map((x) => x.path), i, msize,
    (idx) => document.querySelectorAll(sel)[idx])
}
</script>

<style scoped src="../assets/media-library.css"></style>

<style scoped>
/* ---- 照片货架卡片（1:1 画框） ---- */
.shelf-card { flex: 0 0 188px; }
.p-card { cursor: pointer; min-width: 0; }
.p-card .art {
  position: relative; aspect-ratio: 1;
  border-radius: 12px; overflow: hidden;
  background: #14141d;
  transition: transform 0.22s ease, box-shadow 0.22s ease;
}
.p-card:hover .art {
  transform: scale(1.045);
  box-shadow: 0 10px 30px rgba(0, 0, 0, 0.55), 0 0 0 2px rgba(255, 255, 255, 0.35);
}
.p-name {
  margin-top: 9px; font-size: 13.5px; font-weight: 600;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.p-sub { font-size: 12px; margin-top: 2px; }

/* ---- 所有照片网格（纯图片墙，无文字） ---- */
.photo-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(168px, 1fr));
  gap: 16px;
}
.cell {
  position: relative; aspect-ratio: 1;
  border-radius: 12px; overflow: hidden;
  background: #14141d; cursor: zoom-in;
  transition: transform 0.22s ease, box-shadow 0.22s ease;
}
.cell:hover {
  transform: scale(1.045);
  box-shadow: 0 10px 30px rgba(0, 0, 0, 0.55), 0 0 0 2px rgba(255, 255, 255, 0.35);
}
.cell img { position: relative; z-index: 1; width: 100%; height: 100%; object-fit: cover; display: block; }

/* ---- 移动端（照片卡片专属） ---- */
@media (max-width: 768px) {
  .shelf-card { flex: 0 0 140px; }
  .p-name { font-size: 12.5px; margin-top: 6px; }
  .p-sub { font-size: 11px; }
  .photo-grid { grid-template-columns: repeat(3, 1fr); gap: 8px; }
  .cell { border-radius: 10px; }
}
</style>
