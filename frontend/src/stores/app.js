import { defineStore } from 'pinia'
import http from '../api/http'

export const useApp = defineStore('app', {
  state: () => ({
    siteTitle: 'WebVid',
    version: '',
    uploadWorkers: 2,  // 网页上传同传文件数，后台「任务设置」可调
    viewMode: localStorage.getItem('nl_view') || 'list', // list | grid
  }),
  actions: {
    async fetchPublic() {
      try {
        const d = await http.get('/public/settings')
        this.siteTitle = d.site_title || 'WebVid'
        this.version = d.version || ''
        this.uploadWorkers = d.upload_workers || 2
        document.title = this.siteTitle
      } catch { /* 忽略，用默认标题 */ }
    },
    setViewMode(m) {
      this.viewMode = m
      localStorage.setItem('nl_view', m)
    },
  },
})
