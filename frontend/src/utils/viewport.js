// 移动端断点（与 CSS @media (max-width: 768px) 保持同一阈值）。
// 仅供无法用纯 CSS 表达的场景：el-carousel 高度 prop、el-table 列显隐等。
import { ref } from 'vue'

const mq = window.matchMedia('(max-width: 768px)')
export const isMobile = ref(mq.matches)
mq.addEventListener('change', (e) => { isMobile.value = e.matches })
