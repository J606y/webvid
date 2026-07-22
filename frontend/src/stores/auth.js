import { defineStore } from 'pinia'
import { api } from '../utils/api'
import { getToken, setToken, clearToken } from '../utils/token'

export const useAuth = defineStore('auth', {
  state: () => ({
    token: getToken(),
    user: null,
  }),
  getters: {
    isAdmin: (s) => s.user?.role === 'admin',
  },
  actions: {
    async login(username, password) {
      const d = await api.auth.login(username, password)
      this.token = d.token
      this.user = d.user
      setToken(d.token)
    },
    async fetchMe() {
      this.user = await api.auth.me()
    },
    logout() {
      this.token = ''
      this.user = null
      clearToken()
      location.href = '/login'
    },
  },
})
