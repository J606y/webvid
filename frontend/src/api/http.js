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
    // 优先用后端返回的人话 message；无响应（服务没起/网络断/超时）时把 axios 英文文案翻成人话
    let msg = err.response?.data?.message
    if (!msg) {
      if (err.code === 'ECONNABORTED' || /timeout/i.test(err.message || '')) {
        msg = '请求超时，请稍后重试'
      } else if (/network error/i.test(err.message || '')) {
        msg = '连不上服务器：请检查网络，或服务是否在运行'
      } else {
        msg = err.message || '网络错误'
      }
    }
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
