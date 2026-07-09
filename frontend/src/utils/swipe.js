// 触屏左右滑动手势：横向位移超阈值且比纵向明显时，回调方向（'left' | 'right'）。
// 用于给无原生触屏支持的 el-carousel（随机推荐横幅）补左右滑动——桌面走 hover 箭头，
// 移动端只有触屏事件，滑动才是主要导航方式。不 preventDefault，纵向页面滚动不受影响。
export function swipeHandlers(onSwipe, threshold = 40) {
  let x0 = 0
  let y0 = 0
  return {
    onTouchstart(e) {
      const t = e.changedTouches[0]
      x0 = t.clientX
      y0 = t.clientY
    },
    onTouchend(e) {
      const t = e.changedTouches[0]
      const dx = t.clientX - x0
      const dy = t.clientY - y0
      if (Math.abs(dx) > threshold && Math.abs(dx) > Math.abs(dy)) {
        onSwipe(dx < 0 ? 'left' : 'right')
      }
    },
  }
}
