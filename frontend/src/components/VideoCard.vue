<template>
  <!-- 视频卡片（16:9 画框 + 悬停播放钮 + 可选续播条）：LibraryVideo 的最近添加/最近播放/网格
       三处原本各写一份近同结构，统一到此。副标题经默认插槽定制（缺省=大小·时间）。 -->
  <div class="v-card" @click="$emit('open', $event)">
    <div class="art">
      <img :src="thumbUrl(video.path, 480)" loading="lazy" @error="hideImg" />
      <div class="thumb-fallback abs"><el-icon :size="30"><VideoCamera /></el-icon></div>
      <el-icon class="play" :size="38" @click.stop="$emit('play')"><VideoPlay /></el-icon>
      <div v-if="showProgress && pct" class="prog-bar"><span :style="{ width: pct + '%' }" /></div>
    </div>
    <div class="v-name" :title="video.name">{{ stripExt(video.name) }}</div>
    <div class="dim v-sub">
      <slot>{{ formatSize(video.size) }} · {{ formatTime(video.modified) }}</slot>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { VideoCamera, VideoPlay } from '@element-plus/icons-vue'
import { thumbUrl } from '../utils/path'
import { hideImg, stripExt, formatSize, formatTime, progressPct } from '../utils/file'

const props = defineProps({
  video: { type: Object, required: true },
  showProgress: { type: Boolean, default: false }, // 是否显示续播进度条
})
defineEmits(['open', 'play']) // open：弹详情（带原生 click 事件供取 .art 转场锚）；play：直达播放页

const pct = computed(() => progressPct(props.video?.position, props.video?.duration))
</script>

<style scoped>
.v-card { cursor: pointer; min-width: 0; }
.art {
  position: relative; aspect-ratio: 16/9;
  border-radius: 12px; overflow: hidden;
  background: #14141d;
  transition: transform 0.22s ease, box-shadow 0.22s ease;
}
.art img { position: relative; z-index: 1; width: 100%; height: 100%; object-fit: cover; display: block; }
.abs { position: absolute; inset: 0; }
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
/* 触屏没有 hover：隐形播放图标会拦截卡片中心点击，整个移除（统一「点卡片=详情」） */
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
  background: var(--accent);
  border-radius: 0 2px 2px 0;
}
.v-name {
  margin-top: 9px; font-size: 13.5px; font-weight: 600;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.v-sub { font-size: 12px; margin-top: 2px; }

@media (max-width: 768px) {
  .v-name { font-size: 12.5px; margin-top: 6px; }
  .v-sub { font-size: 11px; }
}
</style>
