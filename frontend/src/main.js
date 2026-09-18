import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import 'element-plus/theme-chalk/dark/css-vars.css'
import './assets/glass.css'
import './assets/player.css' // 网页全屏时播放器会被搬到 body，scoped 跟不过去，样式只能全局

const app = createApp(App)
// 兜底：组件渲染/生命周期抛错与未捕获 promise rejection 只记录，不留白屏噪音
app.config.errorHandler = (err, instance, info) => {
  console.error('[app error]', err, info)
}
window.addEventListener('unhandledrejection', (e) => {
  console.error('[unhandled rejection]', e.reason)
})

app.use(createPinia()).use(router).mount('#app')
