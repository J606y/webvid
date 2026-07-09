<template>
  <div class="page">
    <!-- Featured 大幅轮播：随机 5 张照片（根视图才显示，Infuse 首页顶部横幅） -->
    <!-- 移动端无 hover 箭头，绑触屏左右滑动切换（swipe.onTouch* 落到 el-carousel 根元素） -->
    <el-carousel v-if="isHome && hero.length" ref="carousel" class="feat" :height="featHeight"
      :interval="6000" :autoplay="heroActive" arrow="hover"
      @touchstart="swipe.onTouchstart" @touchend="swipe.onTouchend">
      <el-carousel-item v-for="(p, i) in hero" :key="p.path">
        <div class="feat-item" @click="openList(hero, i, 1200)">
          <img :src="thumbUrl(p.path, 1200)" class="feat-img" @error="hideImg" />
          <div class="feat-mask" />
          <div class="feat-info">
            <div class="feat-kicker">随机推荐</div>
            <div class="feat-title">{{ stripExt(p.name) }}</div>
            <div class="dim feat-meta">{{ formatTime(p.modified) }} · {{ formatSize(p.size) }}</div>
            <el-button type="primary" size="large" round :icon="View" class="hero-btn">
              查看大图
            </el-button>
          </div>
        </div>
      </el-carousel-item>
    </el-carousel>

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

    <!-- 「最近查看」横向货架（按本用户查看历史），「查看更多」进 50 张完整视图 -->
    <section v-if="isHome && viewed.length" class="shelf">
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
          @click="openList(viewed, i, 480)">
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
import { ref } from 'vue'
import { ArrowLeft, ArrowRight, Picture, View, FolderOpened } from '@element-plus/icons-vue'
import http from '../api/http'
import { thumbUrl } from '../utils/path'
import { formatSize, formatTime, hideImg, stripExt } from '../utils/file'
import { openLightbox } from '../utils/lightbox'
import { useMediaLibrary } from '../composables/useMediaLibrary'

defineOptions({ name: 'LibraryPhotos' }) // App.vue keep-alive include 按此名匹配

const hero = ref([])   // Featured 随机推荐
const viewed = ref([]) // 最近查看（本用户查看历史）

async function loadStatic() {
  try {
    // Featured/最近查看两路互不依赖，并发请求缩短首屏等待
    const [r, h] = await Promise.all([
      // Featured：整库随机抽 5 张（封面 object-fit:cover，竖图也能铺满横幅）
      http.get('/media/list', { params: { kind: 'image', limit: 5, sort: 'random' } }),
      // 最近查看：本用户查看历史（灯箱打开照片时上报，见 utils/lightbox），文件删/移后自然消失
      http.get('/media/history', { params: { kind: 'image', limit: 12 } }),
    ])
    hero.value = r.items || []
    viewed.value = h.items || []
  } catch (e) {
    // axios 响应拦截器已负责弹 toast，这里只吞掉避免未捕获 rejection
    console.error(e)
  }
}

// 共享骨架：网格分页 / 视图切换 / 无限滚动 / 轮播 / infuse-mode（见 composables/useMediaLibrary）
const { grid, loaded, loading, sort, sentinel, carousel, heroActive, all, historyView, isHome, dirName, featHeight, swipe } =
  useMediaLibrary({ kind: 'image', routePath: '/library/photos', historyKey: 'viewed', dirDefault: '照片墙', historyCap: 50, loadStatic })

// msize 传该列表正在展示的缩略图尺寸，灯箱占位图可直接命中浏览器缓存
function openList(list, i, msize = 320) {
  openLightbox(list.map((x) => x.path), i, msize)
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
