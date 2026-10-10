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
          <router-link to="/ranking" class="text-sm transition-colors" :class="navClass('/ranking')">排行榜</router-link>
          <router-link to="/datasources" class="text-sm transition-colors" :class="navClass('/datasources')">接入指南</router-link>
          <router-link v-if="admin" to="/admin" class="text-sm transition-colors" :class="navClass('/admin', true)">管理</router-link>
        </nav>
        <div class="flex items-center gap-2 md:gap-3">
          <!-- 头像下拉：个人中心与退出收进这里，顶栏导航不再单挂「个人中心」 -->
          <div class="relative">
            <button @click="userMenuOpen = !userMenuOpen"
                    class="w-9 h-9 rounded-full bg-surface-alt border border-border flex items-center justify-center font-serif text-sm hover:border-text transition-colors"
                    :title="displayName" aria-label="账户菜单">
              {{ initial }}
            </button>
            <div v-if="userMenuOpen" class="fixed inset-0 z-40" @click="userMenuOpen = false"></div>
            <div v-if="userMenuOpen" class="absolute right-0 top-full mt-2 w-52 card !p-0 overflow-hidden z-50 animate-rise">
              <div class="px-4 py-3 border-b border-border">
                <div class="font-medium text-sm truncate">{{ displayName }}</div>
                <div class="font-mono text-xs text-text-muted truncate mt-0.5">@{{ session.user && session.user.username }}</div>
              </div>
              <router-link to="/profile" @click="userMenuOpen = false"
                           class="flex items-center justify-between px-4 py-2.5 text-sm hover:bg-surface-alt transition-colors">
                个人中心
                <span v-if="primaryRole" class=""><UiTag :tone="roleTone(primaryRole)" :label="primaryRole" /></span>
              </router-link>
              <button @click="onLogout" class="block w-full text-left px-4 py-2.5 text-sm text-text-muted hover:text-text hover:bg-surface-alt transition-colors">退出登录</button>
            </div>
          </div>
          <button class="md:hidden p-2.5 -mr-2.5 text-text-muted hover:text-text transition-colors" @click="mobileOpen = !mobileOpen" aria-label="菜单">
            <svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M4 6h16M4 12h16M4 18h16"></path></svg>
          </button>
        </div>
      </div>
    </header>

    <!-- 维护模式横幅（待办清单 P95）：**没有关闭按钮**——该被看见的说明不该依赖用户主动去看，
         关一次就等于把"这页为什么空白"的答案藏回去。文案也不写恢复时间：我们没有那个值。
         管理员看到的是另一句（他本来就不被拦，给他看用户条会误导），并带一个停止入口。 -->
    <div v-if="showLayout && maintenance" class="border-b border-border bg-surface-alt">
      <div class="max-w-6xl mx-auto px-4 md:px-6 py-2.5 flex items-start gap-3">
        <span class="font-mono text-xs uppercase tracking-wider text-text-muted shrink-0 mt-0.5">维护</span>
        <p class="flex-1 text-sm leading-relaxed min-w-0 max-w-full">
          <template v-if="admin">维护模式已开启 — 非管理员的数据面请求正在返回 503；管理面板与登录正常。</template>
          <template v-else>系统维护中 — 阅读与书籍接口暂时停用，登录、账单与额度查询不受影响。</template>
        </p>
        <button v-if="admin" type="button" :disabled="stoppingMaintenance"
                class="btn-ghost btn-sm shrink-0" @click="stopMaintenance">停止维护</button>
      </div>
    </div>

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
      <!-- overflow-y-auto 是这条链上唯一的短板：12 个管理页签 + 用户页签 + 退出总高 700px+，
           不内滚的话小屏手机上尾部条目（设置、号池…）滚不到，等于移动端管理员进不去那些页。 -->
      <div class="absolute right-0 top-0 h-full w-64 overflow-y-auto bg-surface animate-slide-in" @click.stop>
        <div class="p-4 border-b border-border flex items-center justify-between">
          <span class="font-serif text-lg font-medium">菜单</span>
          <button @click="mobileOpen = false" class="text-text-muted hover:text-text" aria-label="关闭">
            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M6 18L18 6M6 6l12 12"></path></svg>
          </button>
        </div>
        <div class="p-4 space-y-1">
          <router-link v-for="t in visibleUserTabs" :key="t.to" :to="t.to" @click="mobileOpen = false" class="block px-3 py-2 rounded-lg text-sm text-text-muted hover:text-text hover:bg-surface-alt transition-colors">{{ t.label }}</router-link>
          <template v-if="admin">
            <div class="mt-4 pt-4 border-t border-border">
              <div class="font-mono text-xs text-text-muted uppercase tracking-wider mb-2 px-3">管理</div>
              <template v-for="grp in adminNavGroups" :key="grp.label || 'root'">
                <div v-if="grp.label" class="text-xs text-text-muted/70 px-3 mt-3 mb-1">{{ grp.label }}</div>
                <router-link v-for="t in grp.items" :key="t.to" :to="t.to" @click="mobileOpen = false" class="block px-3 py-2 rounded-lg text-sm text-text-muted hover:text-text hover:bg-surface-alt transition-colors">{{ t.label }}</router-link>
              </template>
            </div>
          </template>
          <div class="mt-4 pt-4 border-t border-border">
            <div class="font-mono text-xs text-text-muted uppercase tracking-wider mb-2 px-3">账户</div>
            <router-link to="/profile" @click="mobileOpen = false" class="block px-3 py-2 rounded-lg text-sm text-text-muted hover:text-text hover:bg-surface-alt transition-colors">个人中心</router-link>
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
              <template v-for="grp in adminNavGroups" :key="grp.label || 'root'">
                <div v-if="!sidebarCollapsed && grp.label" class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 mt-4 mb-1.5 first:mt-0">{{ grp.label }}</div>
                <router-link
                  v-for="t in grp.items"
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
              </template>
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
import { roleTone, toast } from './utils.js'
import { miscApi, adminApi } from './api/index.js'

// 站内公告：拉取系统设置 announcement；关闭状态按内容记忆（localStorage），内容变更后重新展示
const announcement = ref('')
// 维护模式横幅（待办清单 P95）：同一个请求带回的只读信号，不新开端点、不做设置项式的前端副本。
// **必须读在公告那个「空公告就 return」之前**——公告为空是常态，那样会把维护状态一起漏掉。
const maintenance = ref(false)
const stoppingMaintenance = ref(false)

const fetchAnnouncement = async () => {
  try {
    const data = await miscApi.announcement()
    maintenance.value = !!(data && data.maintenance)
    const content = ((data && data.content) || '').trim()
    if (!content) { announcement.value = ''; return }
    let dismissed = ''
    try { dismissed = localStorage.getItem('announcement_dismissed') || '' } catch {}
    announcement.value = content !== dismissed ? content : ''
  } catch { /* 公告获取失败静默处理，不影响主流程 */ }
}

// 管理员那条横幅上的「停止维护」：写的是同一个设置项（面板设置页那个开关），
// 不在横幅里再造第二个写入口径——成功就本地收敛，失败把后端的句子报出来、不改本地状态。
const stopMaintenance = async () => {
  if (stoppingMaintenance.value) return
  stoppingMaintenance.value = true
  try {
    await adminApi.updateSetting('maintenance_mode', 'false')
    maintenance.value = false
    toast('维护模式已关闭', 'success')
  } catch (e) {
    toast(e.message || '关闭失败', 'error')
  } finally {
    stoppingMaintenance.value = false
  }
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
  '/admin/devices': 'monitor',
  '/admin/settings': 'settings',
}

const router = useRouter()
const route = useRoute()

const mobileOpen = ref(false)

// 抽屉打开时锁 body 滚动：不锁的话手指在面板里滑动会带着背景走，滚回原位很难
watch(mobileOpen, (on) => {
  document.body.style.overflow = on ? 'hidden' : ''
})
const sidebarCollapsed = ref(false)
const userMenuOpen = ref(false)
const userTabs = [
  { to: '/dashboard', label: '概览' },
  // 排行榜取自 /admin/monitor/subjects（后端 AdminRequired），数据是用户阅读行为，只给管理员看
  { to: '/ranking', label: '排行榜' },
  { to: '/datasources', label: '接入指南' },
  // 「个人中心」不占顶部导航，收进头像下拉与抽屉「账户」组
]
// 管理导航按职能分组：组标题在侧边栏展开态显示；折叠态只留图标，组间自然留白
const adminNavGroups = [
  { label: '', items: [{ to: '/admin', label: '总览', exact: true }] },
  { label: '权限', items: [
    { to: '/admin/users', label: '用户' },
    { to: '/admin/roles', label: '角色' },
    { to: '/admin/quotas', label: '套餐' },
    { to: '/admin/redeem-codes', label: '卡密' },
  ] },
  { label: '数据', items: [
    { to: '/admin/datasources', label: '数据源' },
    { to: '/admin/interfaces', label: '接口' },
  ] },
  { label: '运维', items: [
    { to: '/admin/usage-logs', label: '用量流水' },
    { to: '/admin/monitor', label: '监控' },
    { to: '/admin/pools', label: '号池' },
    { to: '/admin/blocked-ips', label: 'IP 拉黑' },
    { to: '/admin/devices', label: '设备与密钥' },
  ] },
  { label: '系统', items: [
    { to: '/admin/settings', label: '设置' },
  ] },
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

// 头像首字母：与个人中心概览卡同一口径
const initial = computed(() => (displayName.value.trim() || '?').charAt(0).toUpperCase())

const primaryRole = computed(() => {
  const u = session.user
  if (!u || !Array.isArray(u.roles) || u.roles.length === 0) return ''
  const order = ['admin', 'vip', 'user']
  const codes = u.roles.map(r => r.code)
  return order.find(c => codes.includes(c)) || codes[0]
})

const admin = computed(() => isAdmin())
// 移动端抽屉与顶部导航同源：adminOnly 项只对管理员出现
const visibleUserTabs = computed(() => userTabs.filter(t => !t.adminOnly || admin.value))

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

watch(() => route.path, () => { mobileOpen.value = false; userMenuOpen.value = false })

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
