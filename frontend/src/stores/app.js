import { defineStore } from 'pinia'
import { api } from '../utils/api'

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
    setViewMode(m) {
      this.viewMode = m
      localStorage.setItem('nl_view', m)
    },
  },
})
