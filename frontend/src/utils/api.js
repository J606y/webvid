// 集中所有 REST endpoint 调用：改路径/参数只此一处，组件按域调 api.fs.list() 等。
// 复用共享 http 实例（baseURL /api + Bearer 鉴权 + 统一错误拦截，见 api/http.js）。
//
// 不收入此处的两类，有意保留在原处：
//   1. 媒体二进制 URL（<img>/<video>/下载用，token 走 query）——utils/path.js 的 rawUrl/thumbUrl/hlsUrl。
//   2. TextDrawer 的裸 axios 取文件原文——需 Range 头 + responseType:text + transformResponse 透传，
//      绕过共享 http 的 baseURL 与响应解包，属刻意特例（见该组件注释）。
import http from '../api/http'

export const api = {
  auth: {
    login: (username, password) => http.post('/auth/login', { username, password }),
    me: () => http.get('/auth/me'),
  },
  publicSettings: () => http.get('/public/settings'),

  fs: {
    list: (path) => http.get('/fs/list', { params: { path } }),
    search: (params) => http.get('/fs/search', { params }),
    mkdir: (path) => http.post('/fs/mkdir', { path }),
    rename: (path, name) => http.post('/fs/rename', { path, name }),
    remove: (paths) => http.post('/fs/remove', { paths }),
    move: (paths, dst_dir) => http.post('/fs/move', { paths, dst_dir }),
    copy: (paths, dst_dir) => http.post('/fs/copy', { paths, dst_dir }),
    offline: (urls, dst_dir, name) => http.post('/fs/offline', { urls, dst_dir, name }),
    // 上传：path 含中文/特殊字符走 query，body 为原始字节流；config 传 headers/timeout/onUploadProgress
    upload: (target, overwrite, file, config) =>
      http.put(`/fs/upload?path=${encodeURIComponent(target)}${overwrite ? '&overwrite=1' : ''}`, file, config),
  },

  media: {
    list: (params) => http.get('/media/list', { params }),
    history: (params) => http.get('/media/history', { params }),
    progress: (path) => http.get('/media/progress', { params: { path } }),
    // played：fire-and-forget 的查看/播放上报，silent=失败不弹 toast
    played: (body) => http.post('/media/played', body, { silent: true }),
  },
  video: {
    info: (path) => http.get('/video/info', { params: { path } }),
  },

  tasks: {
    list: () => http.get('/tasks'),
    cancel: (id) => http.post(`/tasks/${id}/cancel`),
    retry: (id) => http.post(`/tasks/${id}/retry`),
    remove: (id) => http.post(`/tasks/${id}/remove`),
    clearDone: () => http.delete('/tasks/done'),
  },

  admin: {
    settings: {
      get: () => http.get('/admin/settings'),
      save: (body) => http.put('/admin/settings', body),
    },
    drivers: () => http.get('/admin/drivers'),
    storages: {
      list: () => http.get('/admin/storages'),
      get: (id) => http.get(`/admin/storages/${id}`),
      create: (body) => http.post('/admin/storages', body),
      update: (id, body) => http.put(`/admin/storages/${id}`, body),
      remove: (id) => http.delete(`/admin/storages/${id}`),
      reload: (id) => http.post(`/admin/storages/${id}/reload`),
    },
    users: {
      list: () => http.get('/admin/users'),
      create: (body) => http.post('/admin/users', body),
      update: (id, body) => http.put(`/admin/users/${id}`, body),
      remove: (id) => http.delete(`/admin/users/${id}`),
    },
    telegram: {
      // send_code：后端 MTProto 握手 60s 预算，请求超时须大于它（见 AdminStorage 注释）
      sendCode: (id) => http.post(`/admin/telegram/${id}/send_code`, null, { timeout: 90000 }),
      signIn: (id, body) => http.post(`/admin/telegram/${id}/sign_in`, body),
    },
    googledrive: {
      // 返回 Google OAuth 同意页 URL；body 带浏览器 origin 用于拼回调 redirect_uri
      authUrl: (id, body) => http.post(`/admin/googledrive/${id}/auth_url`, body),
    },
    index: {
      progress: () => http.get('/admin/index/progress'),
      rebuild: () => http.post('/admin/index/rebuild'),
    },
    preload: {
      progress: () => http.get('/admin/preload/progress'),
      run: () => http.post('/admin/preload/run'),
    },
  },
}
