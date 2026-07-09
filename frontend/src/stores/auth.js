import { defineStore } from 'pinia'
import http from '../api/http'

export const useAuth = defineStore('auth', {
  state: () => ({
    token: localStorage.getItem('nl_token') || '',
    user: null,
  }),
  getters: {
    isAdmin: (s) => s.user?.role === 'admin',
  },
  actions: {
    async login(username, password) {
      const d = await http.post('/auth/login', { username, password })
      this.token = d.token
      this.user = d.user
      localStorage.setItem('nl_token', d.token)
    },
    async fetchMe() {
      this.user = await http.get('/auth/me')
    },
    logout() {
      this.token = ''
      this.user = null
      localStorage.removeItem('nl_token')
      location.href = '/login'
    },
  },
})
