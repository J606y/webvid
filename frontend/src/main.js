import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import 'element-plus/theme-chalk/dark/css-vars.css'
import './assets/glass.css'

const app = createApp(App)
// 兜底：组件渲染/生命周期抛错与未捕获 promise rejection 只记录，不留白屏噪音
app.config.errorHandler = (err, instance, info) => {
  console.error('[app error]', err, info)
}
window.addEventListener('unhandledrejection', (e) => {
  console.error('[unhandled rejection]', e.reason)
})

app.use(createPinia()).use(router).mount('#app')
