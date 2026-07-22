// 单一登录令牌访问器：令牌存 localStorage['nl_token']，供三处共用——
// http 鉴权头（api/http.js）、媒体二进制 URL 的 query token（utils/path.js）、auth store（stores/auth.js）。
// 原 'nl_token' 字面量散在这三处，收拢到此避免键名漂移。
const KEY = 'nl_token'

export const getToken = () => localStorage.getItem(KEY) || ''
export const setToken = (t) => localStorage.setItem(KEY, t)
export const clearToken = () => localStorage.removeItem(KEY)
