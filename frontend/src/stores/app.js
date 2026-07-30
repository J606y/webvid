import { defineStore } from 'pinia'
import { api } from '../utils/api'

// publicOnce 首次公共设置请求，供 ensurePublic 去重
let publicOnce = null

export const useApp = defineStore('app', {
  state: () => ({
    siteTitle: 'WebVid',
    version: '',
    uploadWorkers: 2,  // 网页上传同传文件数，后台「任务设置」可调
    // 媒体库首页「所有视频/所有照片」的取法：random 随机抽样 | modified 最新在前
    mediaHomeSort: 'random',
    viewMode: localStorage.getItem('nl_view') || 'list', // list | grid
  }),
  actions: {
    async fetchPublic() {
      try {
        const d = await api.publicSettings()
        this.siteTitle = d.site_title || 'WebVid'
        this.version = d.version || ''
        this.uploadWorkers = d.upload_workers || 2
        this.mediaHomeSort = d.media_home_sort === 'modified' ? 'modified' : 'random'
        document.title = this.siteTitle
      } catch { /* 忽略，用默认标题 */ }
    },
    // ensurePublic 保证公共设置已到手，重复调用只发一次请求。
    // 媒体库首页的取法由 media_home_sort 决定：App.vue 的 onMounted 比子路由的
    // setup 晚，不等这一下就会先按默认值（随机）发一轮，后台设成「最新在前」在
    // 冷启动时形同失效。Admin 保存后仍直接调 fetchPublic 强刷。
    ensurePublic() {
      if (!publicOnce) publicOnce = this.fetchPublic()
      return publicOnce
    },
    setViewMode(m) {
      this.viewMode = m
      localStorage.setItem('nl_view', m)
    },
  },
})
