<template>
  <div v-if="!session.ready" class="min-h-screen flex items-center justify-center bg-bg text-text-muted text-sm">加载中</div>
  <template v-else>
    <div class="ambient-glow"></div>

    <header v-if="showLayout" class="sticky top-0 z-40 bg-bg/85 backdrop-blur-md border-b border-border">
      <div class="max-w-6xl mx-auto px-4 md:px-6 py-3 md:py-4 flex items-center justify-between">
        <div class="flex items-center gap-2 cursor-pointer" @click="$router.push('/dashboard')">
          <span class="font-serif text-lg md:text-xl lg:text-2xl tracking-tighter font-medium">LoomProxy</span>
          <span class="font-mono text-xs text-text-muted uppercase tracking-wider hidden md:block">Console</span>
        </div>
        <nav class="hidden md:flex gap-6 md:gap-7 items-center">
          <router-link to="/dashboard" class="text-sm transition-colors" :class="navClass('/dashboard')">概览</router-link>
          <router-link to="/datasources" class="text-sm transition-colors" :class="navClass('/datasources')">接入指南</router-link>
          <router-link to="/profile" class="text-sm transition-colors" :class="navClass('/profile')">个人中心</router-link>
          <router-link v-if="admin" to="/admin" class="text-sm transition-colors" :class="navClass('/admin', true)">管理</router-link>
        </nav>
        <div class="flex items-center gap-2 md:gap-3">
          <div class="hidden sm:flex items-center gap-2 px-2 md:px-3 py-1 md:py-1.5 border border-border rounded-md bg-surface">
            <span class="text-xs md:text-sm font-medium">{{ displayName }}</span>
            <UiTag v-if="primaryRole" :tone="roleTone(primaryRole)" :label="primaryRole" />
          </div>
          <button @click="onLogout" class="hidden md:block text-xs md:text-sm text-text-muted hover:text-text transition-colors">退出</button>
          <button class="md:hidden p-2 -mr-2 text-text-muted hover:text-text transition-colors" @click="mobileOpen = !mobileOpen" aria-label="菜单">
            <svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M4 6h16M4 12h16M4 18h16"></path></svg>
          </button>
        </div>
      </div>
    </header>

    <!-- 站内公告横幅：系统设置 announcement 非空时展示；关闭后记住内容，公告变更后重新展示 -->
    <div v-if="showLayout && announcement" class="border-b border-border bg-surface">
      <div class="max-w-6xl mx-auto px-4 md:px-6 py-2.5 flex items-start gap-3">
        <span class="font-mono text-xs uppercase tracking-wider text-text-muted shrink-0 mt-0.5">公告</span>
        <p class="flex-1 text-sm leading-relaxed whitespace-pre-wrap">{{ announcement }}</p>
        <button @click="dismissAnnouncement" class="shrink-0 p-0.5 text-text-muted hover:text-text transition-colors" aria-label="关闭公告">
          <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M6 18L18 6M6 6l12 12"></path></svg>
        </button>
      </div>
    </div>

    <!-- 移动端抽屉 -->
    <div v-if="mobileOpen" class="fixed inset-0 z-50 bg-black/35 md:hidden" @click="mobileOpen = false">
      <div class="absolute right-0 top-0 h-full w-64 bg-surface animate-slide-in" @click.stop>
        <div class="p-4 border-b border-border flex items-center justify-between">
          <span class="font-serif text-lg font-medium">菜单</span>
          <button @click="mobileOpen = false" class="text-text-muted hover:text-text" aria-label="关闭">
            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M6 18L18 6M6 6l12 12"></path></svg>
          </button>
        </div>
        <div class="p-4 space-y-1">
          <router-link v-for="t in userTabs" :key="t.to" :to="t.to" @click="mobileOpen = false" class="block px-3 py-2 rounded-lg text-sm text-text-muted hover:text-text hover:bg-surface-alt transition-colors">{{ t.label }}</router-link>
          <template v-if="admin">
            <div class="mt-4 pt-4 border-t border-border">
              <div class="font-mono text-xs text-text-muted uppercase tracking-wider mb-2 px-3">管理</div>
              <router-link v-for="t in adminTabs" :key="t.to" :to="t.to" @click="mobileOpen = false" class="block px-3 py-2 rounded-lg text-sm text-text-muted hover:text-text hover:bg-surface-alt transition-colors">{{ t.label }}</router-link>
            </div>
          </template>
          <div class="mt-4 pt-4 border-t border-border">
            <button @click="onLogout" class="block w-full text-left px-3 py-2 rounded-lg text-sm text-text-muted hover:text-text hover:bg-surface-alt transition-colors">退出登录</button>
          </div>
        </div>
      </div>
    </div>

    <!-- 公共页（登录/注册/找回密码）自带整屏居中，main 不再套 padding 与 max-width，避免双重留白与纵向溢出 -->
    <main class="flex-1 w-full mx-auto" :class="route.meta.public ? '' : ('px-4 md:px-6 lg:px-8 py-8 md:py-12 ' + (inAdmin ? 'max-w-7xl' : 'max-w-6xl'))">
      <!-- 管理区：左侧边栏 + 内容 -->
      <div v-if="inAdmin" class="flex gap-8 lg:gap-10 items-start">
        <aside :class="['shrink-0 sticky top-24 hidden md:block transition-all duration-200 overflow-hidden', sidebarCollapsed ? 'w-12' : 'w-44 lg:w-48']">
          <!-- 折叠按钮 -->
          <button @click="sidebarCollapsed = !sidebarCollapsed" class="w-full flex items-center justify-center p-2 text-text-muted hover:text-text transition-colors" :title="sidebarCollapsed ? '展开侧边栏' : '折叠侧边栏'" aria-label="切换侧边栏">
            <SidebarIcon v-if="!sidebarCollapsed" name="panel-left" :size="16" :stroke-width="1.5" />
            <SidebarIcon v-else name="panel-left-close" :size="16" :stroke-width="1.5" />
          </button>
          <div class="mt-1" :class="sidebarCollapsed ? 'flex flex-col items-center' : ''">
            <div v-if="!sidebarCollapsed" class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3 px-3">管理</div>
            <nav :class="sidebarCollapsed ? 'space-y-1' : 'space-y-0.5'">
              <router-link
                v-for="t in adminTabs"
                :key="t.to"
                :to="t.to"
                :title="sidebarCollapsed ? t.label : ''"
                :class="[
                  'block text-sm transition-colors rounded-lg flex',
                  sidebarCollapsed ? 'h-9 justify-center px-0' : 'px-3 py-2',
                  isTabActive(t)
                    ? (sidebarCollapsed ? 'text-text' : 'bg-surface border border-border text-text font-medium')
                    : 'text-text-muted hover:text-text hover:bg-surface-alt',
                ]"
              >
                <span class="shrink-0" :class="sidebarCollapsed ? 'w-5 h-5 flex items-center justify-center' : 'mr-2.5 w-4 h-4'">
                  <SidebarIcon :name="tabIconMap[t.to]" :size="sidebarCollapsed ? 20 : 16" :stroke-width="1.5" />
                </span>
                <template v-if="!sidebarCollapsed">{{ t.label }}</template>
              </router-link>
            </nav>
          </div>
        </aside>
        <div class="flex-1 min-w-0">
          <router-view />
        </div>
      </div>
      <router-view v-else />
    </main>
  </template>
</template>

<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import UiTag from './components/UiTag.vue'
import SidebarIcon from './components/SidebarIcon.vue'
import { session, isAdmin, logout } from './store.js'
import { roleTone } from './utils.js'
import { miscApi } from './api/index.js'

// 站内公告：拉取系统设置 announcement；关闭状态按内容记忆（localStorage），内容变更后重新展示
const announcement = ref('')

const fetchAnnouncement = async () => {
  try {
    const data = await miscApi.announcement()
    const content = ((data && data.content) || '').trim()
    if (!content) { announcement.value = ''; return }
    let dismissed = ''
    try { dismissed = localStorage.getItem('announcement_dismissed') || '' } catch {}
    announcement.value = content !== dismissed ? content : ''
  } catch { /* 公告获取失败静默处理，不影响主流程 */ }
}

const dismissAnnouncement = () => {
  try { localStorage.setItem('announcement_dismissed', announcement.value) } catch {}
  announcement.value = ''
}

// Lucide icon path map — all icons use stroke-width 1.5 to match project style
const iconMap = {
  '/admin': 'layout-dashboard',
  '/admin/users': 'users',
  '/admin/roles': 'shield',
  '/admin/quotas': 'credit-card',
  '/admin/datasources': 'database',
  '/admin/interfaces': 'code',
  '/admin/usage-logs': 'list',
  '/admin/redeem-codes': 'ticket',
  '/admin/monitor': 'activity',
  '/admin/pools': 'layers',
  '/admin/blocked-ips': 'ban',
  '/admin/settings': 'settings',
}

const router = useRouter()
const route = useRoute()

const mobileOpen = ref(false)
const sidebarCollapsed = ref(false)
const userTabs = [
  { to: '/dashboard', label: '概览' },
  { to: '/datasources', label: '接入指南' },
  { to: '/profile', label: '个人中心' },
]
const adminTabs = [
  { to: '/admin', label: '总览', exact: true },
  { to: '/admin/users', label: '用户' },
  { to: '/admin/roles', label: '角色' },
  { to: '/admin/quotas', label: '套餐' },
  { to: '/admin/datasources', label: '数据源' },
  { to: '/admin/interfaces', label: '接口' },
  { to: '/admin/usage-logs', label: '用量流水' },
  { to: '/admin/redeem-codes', label: '卡密' },
  { to: '/admin/monitor', label: '监控' },
  { to: '/admin/pools', label: '号池' },
  { to: '/admin/blocked-ips', label: 'IP 拉黑' },
  { to: '/admin/settings', label: '设置' },
]
const tabIconMap = iconMap

const showLayout = computed(() => !!session.user && !route.meta.public)

const inAdmin = computed(() => {
  return isAdmin() && route.path.startsWith('/admin')
})

const displayName = computed(() => {
  const u = session.user
  return u ? (u.nickname || u.username) : ''
})

const primaryRole = computed(() => {
  const u = session.user
  if (!u || !Array.isArray(u.roles) || u.roles.length === 0) return ''
  const order = ['admin', 'vip', 'user']
  const codes = u.roles.map(r => r.code)
  return order.find(c => codes.includes(c)) || codes[0]
})

const admin = computed(() => isAdmin())

const navClass = (path, prefix = false) => {
  const current = route.path
  const active = prefix ? current.startsWith(path) : current === path
  return active ? 'text-text font-medium' : 'text-text-muted hover:text-text'
}

const isTabActive = (t) => {
  return t.exact ? route.path === t.to : route.path.startsWith(t.to)
}

const onLogout = async () => {
  await logout()
  router.push('/login')
}

watch(() => route.path, () => { mobileOpen.value = false })

watch(showLayout, (v) => { if (v) fetchAnnouncement() }, { immediate: true })

watch(sidebarCollapsed, (val) => {
  try { localStorage.setItem('sidebar_collapsed', val ? '1' : '0') } catch {}
})

onMounted(() => {
  try {
    const saved = localStorage.getItem('sidebar_collapsed')
    if (saved === '1') sidebarCollapsed.value = true
  } catch {}
})
</script>
