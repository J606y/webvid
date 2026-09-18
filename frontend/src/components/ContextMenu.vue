<template>
  <!-- 右键 / 长按菜单（唤出方式见 utils/contextMenu）。
       Teleport 到 body：卡片所在的网格有 overflow 与层叠上下文，菜单留在原处会被裁掉。
       全屏遮罩负责吃掉外部点击——也顺带挡住了长按松手补的那次 click。 -->
  <Teleport to="body">
    <div v-if="open" class="cm-mask" @click="close" @contextmenu.prevent="close">
      <div class="cm glass" :style="pos" @click.stop>
        <button v-for="it in items" :key="it.cmd" class="cm-item" :class="{ danger: it.danger }"
          @click="pick(it)">
          <el-icon :size="15"><component :is="it.icon" /></el-icon>
          <span>{{ it.label }}</span>
        </button>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'

const emit = defineEmits(['select'])

const open = ref(false)
const items = ref([])
const x = ref(0)
const y = ref(0)
let payload = null // 唤出菜单的那一项，随 select 事件原样交回

const pos = computed(() => ({ left: x.value + 'px', top: y.value + 'px' }))

// show 在指定视口坐标弹出菜单。data 是这次针对的数据项，选中后随事件回传。
function show(at, menuItems, data) {
  items.value = menuItems
  payload = data
  // 贴边翻转：靠近右/下边缘时朝反方向展开，别让菜单探出视口
  const w = 176
  const h = menuItems.length * 40 + 12
  x.value = Math.max(8, Math.min(at.x, window.innerWidth - w - 8))
  y.value = Math.max(8, Math.min(at.y, window.innerHeight - h - 8))
  open.value = true
}

function close() { open.value = false }

function pick(it) {
  open.value = false
  emit('select', it.cmd, payload)
}

// 菜单锚死在一个视口坐标上：页面一滚，它要么跟内容错位、要么傻停在原地，
// 两种都不对 —— 直接关掉，这也是各家系统菜单的一致行为。
// capture 是为了收到内部滚动容器（货架横向滚动）冒泡不上来的那些。
function onScroll() { if (open.value) close() }
function onKey(e) { if (e.key === 'Escape') close() }
onMounted(() => {
  window.addEventListener('scroll', onScroll, { passive: true, capture: true })
  window.addEventListener('keydown', onKey)
})
onUnmounted(() => {
  window.removeEventListener('scroll', onScroll, { capture: true })
  window.removeEventListener('keydown', onKey)
})

// 遮罩 teleport 在 body 上，不随宿主页面的 keep-alive 隐藏（同 VideoDetailCard 的教训）
onBeforeRouteLeave(() => { open.value = false })

defineExpose({ show, close })
</script>

<style scoped>
.cm-mask { position: fixed; inset: 0; z-index: 3000; }
.cm {
  position: fixed;
  min-width: 176px;
  padding: 6px;
  border-radius: 14px;
  box-shadow: 0 18px 50px rgba(0, 0, 0, .45);
  animation: cm-in .14s ease-out;
}
/* 从触发点方向轻微放大浮现，和项目其它弹层同一种出场节奏 */
@keyframes cm-in {
  from { opacity: 0; transform: scale(.94); }
  to { opacity: 1; transform: scale(1); }
}
.cm-item {
  display: flex;
  align-items: center;
  gap: 9px;
  width: 100%;
  padding: 9px 11px;
  border: none;
  border-radius: 9px;
  background: transparent;
  color: var(--text, #e8e8ef);
  font-size: 13.5px;
  text-align: left;
  cursor: pointer;
  transition: background .15s ease;
}
.cm-item:hover { background: rgba(255, 255, 255, .1); }
.cm-item.danger { color: #ff6b6b; }
.cm-item.danger:hover { background: rgba(255, 107, 107, .14); }
</style>
