// 轻量会话状态：当前登录用户，供路由守卫与布局共享
import { reactive } from 'vue'
import { authApi, getToken, clearToken } from './api/index.js'

export const session = reactive({
  ready: false, // 首次会话恢复是否完成
  user: null,
})

export function isAdmin() {
  const u = session.user
  return !!(u && Array.isArray(u.roles) && u.roles.some(r => r.code === 'admin'))
}

export async function initSession(force = false) {
  if (session.ready && !force) return
  if (getToken()) {
    try {
      session.user = await authApi.me()
    } catch (e) {
      clearToken()
      session.user = null
    }
  } else {
    session.user = null
  }
  session.ready = true
}

export async function refreshMe() {
  try {
    session.user = await authApi.me()
  } catch (e) { /* 保持现状 */ }
}

export async function logout() {
  try { await authApi.logout() } catch (e) { /* 忽略 */ }
  clearToken()
  session.user = null
}
