// 移动端断点（与 CSS @media (max-width: 768px) 保持同一阈值）。
// 仅供无法用纯 CSS 表达的场景：el-carousel 高度 prop、el-table 列显隐、hero 转场等。
// CSS 媒体查询无法引用自定义属性，故各 @media 仍是 768px 字面量，与本常量共同维护同一阈值。
import { ref } from 'vue'

export const MOBILE_BREAKPOINT = 768 // JS 侧断点单一来源（heroDialog 等复用，勿散写 768）

const mq = window.matchMedia(`(max-width: ${MOBILE_BREAKPOINT}px)`)
export const isMobile = ref(mq.matches)
mq.addEventListener('change', (e) => { isMobile.value = e.matches })
