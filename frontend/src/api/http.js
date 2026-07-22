import axios from 'axios'
import { ElMessage } from 'element-plus'
import 'element-plus/es/components/message/style/css'
import { getToken, clearToken } from '../utils/token'

const http = axios.create({ baseURL: '/api', timeout: 60000 })

// httpError 构造带 HTTP 状态码的错误：调用方可按 e.status 分支（如上传冲突 409）
// 而非匹配错误文案（文案会变、脆弱）。仍保留 e.message，向后兼容既有 catch。
function httpError(msg, status) {
  const e = new Error(msg || '请求失败')
  if (status != null) e.status = status
  return e
}

http.interceptors.request.use((cfg) => {
  const t = getToken()
  if (t) cfg.headers.Authorization = 'Bearer ' + t
  return cfg
})

http.interceptors.response.use(
  (res) => {
    const d = res.data
    if (d && typeof d === 'object' && 'code' in d && d.code !== 200) {
      return Promise.reject(httpError(d.message, d.code))
    }
    return d && typeof d === 'object' && 'data' in d ? d.data : d
  },
  (err) => {
    const status = err.response?.status
    const msg = err.response?.data?.message || err.message || '网络错误'
    if (status === 401) {
      // 登录页自己处理 401 提示；其他页面清 token 回登录
      if (!location.pathname.startsWith('/login')) {
        clearToken()
        location.href = '/login'
      }
    } else if (!err.config?.silent) {
      // silent：fire-and-forget 请求（如查看/播放上报）或调用方自行按码兜底时不打扰用户
      ElMessage.error(msg)
    }
    return Promise.reject(httpError(msg, status))
  }
)

export default http
