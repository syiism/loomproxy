import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import Login from './pages/Login.vue'
import Register from './pages/Register.vue'
import ForgotPassword from './pages/ForgotPassword.vue'
import Dashboard from './pages/Dashboard.vue'
import Datasources from './pages/Datasources.vue'
import Ranking from './pages/Ranking.vue'
import Profile from './pages/Profile.vue'
import Admin from './pages/Admin.vue'
import AdminUsers from './pages/admin/Users.vue'
import AdminSettings from './pages/admin/Settings.vue'
import AdminQuotas from './pages/admin/Quotas.vue'
import AdminRoles from './pages/admin/Roles.vue'
import AdminDatasources from './pages/admin/Datasources.vue'
import AdminUsageLogs from './pages/admin/UsageLogs.vue'
import AdminMonitor from './pages/admin/Monitor.vue'
import AdminPools from './pages/admin/Pools.vue'
import AdminBlockedIPs from './pages/admin/BlockedIPs.vue'
import AdminRedeemCodes from './pages/admin/RedeemCodes.vue'
import AdminInterfaces from './pages/admin/Interfaces.vue'
import NotFound from './pages/NotFound.vue'
import { session, initSession, isAdmin } from './store.js'
import { toast } from './utils.js'
import './styles.css'

const routes = [
  { path: '/', redirect: '/dashboard' },
  { path: '/login', component: Login, meta: { public: true } },
  { path: '/register', component: Register, meta: { public: true } },
  { path: '/forgot-password', component: ForgotPassword, meta: { public: true } },
  { path: '/dashboard', component: Dashboard },
  { path: '/ranking', component: Ranking },
  { path: '/datasources', component: Datasources },
  { path: '/profile', component: Profile },
  { path: '/admin', component: Admin, meta: { admin: true } },
  { path: '/admin/users', component: AdminUsers, meta: { admin: true } },
  { path: '/admin/roles', component: AdminRoles, meta: { admin: true } },
  { path: '/admin/settings', component: AdminSettings, meta: { admin: true } },
  { path: '/admin/quotas', component: AdminQuotas, meta: { admin: true } },
  { path: '/admin/datasources', component: AdminDatasources, meta: { admin: true } },
  { path: '/admin/interfaces', component: AdminInterfaces, meta: { admin: true } },
  { path: '/admin/usage-logs', component: AdminUsageLogs, meta: { admin: true } },
  { path: '/admin/monitor', component: AdminMonitor, meta: { admin: true } },
  { path: '/admin/pools', component: AdminPools, meta: { admin: true } },
  { path: '/admin/blocked-ips', component: AdminBlockedIPs, meta: { admin: true } },
  { path: '/admin/redeem-codes', component: AdminRedeemCodes, meta: { admin: true } },
  { path: '/:pathMatch(.*)*', component: NotFound, meta: { public: true } },
]

const router = createRouter({ history: createWebHistory('/panel/'), routes })

router.beforeEach(async (to) => {
  if (!session.ready) await initSession()
  const authed = !!session.user

  if (to.meta.public) {
    // 已登录访问登录/注册/找回密码页时送回各自首页
    if (authed && (to.path === '/login' || to.path === '/register' || to.path === '/forgot-password')) {
      return isAdmin() ? '/admin' : '/dashboard'
    }
    return true
  }
  if (!authed) return '/login'
  if (to.meta.admin && !isAdmin()) {
    toast('需要管理员权限', 'error')
    return '/dashboard'
  }
  return true
})

createApp(App).use(router).mount('#app')
