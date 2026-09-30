<template>
  <div>
    <PageHeader :title="isAdmin ? '每日数据源额度' : '我的额度'" :subtitle="subtitle">
      <template #actions>
        <UiTag v-if="planName && !isAdmin" tone="blue" :label="planName" />
        <span v-if="planExpireAt && !isAdmin" class="text-xs font-mono" :class="planExpireSoon ? '' : 'text-text-muted'" :style="planExpireSoon ? 'color:#956400' : ''">{{ fmtDate(planExpireAt) }} 到期</span>
      </template>
    </PageHeader>

    <!-- 书源导入入口提示：仅在管理员配置了书源直链时展示 -->
    <router-link v-if="hasImportUrl" to="/profile" class="card reveal card-hover mb-6 md:mb-8 flex items-center justify-between gap-3 group">
      <div class="flex items-center gap-3 min-w-0">
        <span class="font-mono text-xs uppercase tracking-wider text-text-muted shrink-0">提示</span>
        <span class="text-sm">阅读书源在个人中心，点击前往导入。</span>
      </div>
      <svg class="w-4 h-4 shrink-0 text-text-muted group-hover:text-text transition-colors" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M9 5l7 7-7 7"></path></svg>
    </router-link>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <UiEmpty v-else-if="sources.length === 0" title="暂无额度配置" text="管理员尚未配置任何数据源额度。" />

    <template v-else>
      <!-- 汇总卡片 -->
      <div class="grid grid-cols-2 lg:grid-cols-4 gap-4 md:gap-6 mb-8 md:mb-10">
        <div v-for="c in summaryCards" :key="c.label" class="card reveal">
          <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">{{ c.label }}</div>
          <div class="font-serif text-3xl md:text-4xl font-medium tracking-tighter">{{ c.value }}</div>
        </div>
      </div>

      <!-- 各数据源明细：faux-OS 窗口 -->
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 md:gap-6">
        <div v-for="s in sources" :key="s.group" class="reveal border border-border rounded-xl overflow-hidden bg-surface card-hover">
          <div class="flex items-center gap-1.5 px-4 py-2.5 border-b border-border bg-surface-alt">
            <span class="w-2.5 h-2.5 rounded-full bg-border"></span>
            <span class="w-2.5 h-2.5 rounded-full bg-border"></span>
            <span class="w-2.5 h-2.5 rounded-full bg-border"></span>
            <span class="ml-2 font-mono text-xs text-text-muted">{{ s.group }}</span>
          </div>
          <div class="p-5 md:p-6">
            <div class="flex items-center justify-between mb-4">
              <div class="font-medium text-base">{{ s.name }}</div>
              <UiTag v-if="isAdmin" tone="blue" :label="'活跃 ' + (s.active_users || 0)" />
              <UiTag v-else :tone="usageTone(s.usage_pct)" :label="s.usage_pct + '%'" />
            </div>

            <!-- 管理员：全站消耗视角；普通用户：个人额度视角 -->
            <div v-if="isAdmin" class="grid grid-cols-2 gap-2 mb-4">
              <div>
                <div class="font-mono text-xs text-text-muted">今日消耗</div>
                <div class="font-medium text-lg">{{ s.used_today }}</div>
              </div>
              <div>
                <div class="font-mono text-xs text-text-muted">活跃用户</div>
                <div class="font-medium text-lg">{{ s.active_users || 0 }}</div>
              </div>
            </div>
            <template v-else>
              <div class="grid grid-cols-3 gap-2 mb-4">
                <div>
                  <div class="font-mono text-xs text-text-muted">有效总额</div>
                  <div class="font-medium text-lg">{{ fmtQuota(s.effective_total) }}</div>
                </div>
                <div>
                  <div class="font-mono text-xs text-text-muted">今日已用</div>
                  <div class="font-medium text-lg">{{ s.used_today }}</div>
                </div>
                <div>
                  <div class="font-mono text-xs text-text-muted">今日剩余</div>
                  <div class="font-medium text-lg">{{ fmtQuota(s.remaining) }}</div>
                </div>
              </div>
              <div class="mb-4">
                <div class="w-full h-1.5 bg-border rounded-full overflow-hidden">
                  <div class="h-full bg-text transition-all duration-500" :style="{ width: Math.min(100, s.usage_pct) + '%' }"></div>
                </div>
              </div>
            </template>

            <div class="space-y-2 text-sm">
              <div class="flex justify-between"><span class="text-text-muted">日额度</span><span class="font-mono">{{ fmtQuota(s.effective_total) }}</span></div>
              <div class="flex justify-between"><span class="text-text-muted">下次重置</span><span class="font-mono text-xs">{{ s.next_reset }}</span></div>
            </div>

            <div v-if="isAdmin" class="mt-4 pt-4 border-t border-border space-y-3">
              <div>
                <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-2">接口消耗</div>
                <div class="flex flex-wrap gap-1.5">
                  <span v-for="i in s.interfaces" :key="i.name" class="px-2 py-0.5 rounded text-xs font-mono" :class="i.enabled ? 'bg-pale-green-bg text-pale-green-fg' : 'bg-pale-gray-bg text-text-muted'">{{ i.name }} ×{{ i.enabled ? i.cost : 0 }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </template>

    <!-- 调用流水：管理员看全站，普通用户看自己的 -->
    <section class="mt-12 md:mt-16 reveal">
      <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">{{ isAdmin ? '全站调用流水' : '调用流水' }}</h2>
      <UiSpinner v-if="logsLoading" />
      <UiEmpty v-else-if="logs.length === 0" title="暂无调用记录" :text="isAdmin ? '今日还没有用户调用过数据源接口。' : '成功调用数据源接口后，这里会展示每次的额度消耗。'" />
      <template v-else>
        <!-- 移动端 -->
        <div class="sm:hidden space-y-2">
          <div v-for="l in logs" :key="l.id" class="card !p-4 flex items-center justify-between">
            <div>
              <div class="flex items-center gap-2 mb-1">
                <UiTag tone="blue" :label="l.group_name || l.group_code" />
                <span class="font-mono text-xs">{{ l.interface }}</span>
                <span v-if="isAdmin" class="text-xs text-text-muted">{{ l.username }}</span>
              </div>
              <div class="font-mono text-xs text-text-muted">{{ fmtDate(l.created_at) }}</div>
            </div>
            <div class="font-mono text-sm">-{{ l.cost }}</div>
          </div>
        </div>
        <!-- 桌面端 -->
        <div class="hidden sm:block table-wrap">
          <table class="table-base">
            <thead>
              <tr><th>时间</th><th v-if="isAdmin">用户</th><th>数据源</th><th>接口</th><th class="text-right">消耗</th></tr>
            </thead>
            <tbody>
              <tr v-for="l in logs" :key="l.id">
                <td class="font-mono text-xs text-text-muted">{{ fmtDate(l.created_at) }}</td>
                <td v-if="isAdmin" class="text-sm">{{ l.username || ('#' + l.user_id) }}</td>
                <td><UiTag tone="blue" :label="l.group_name || l.group_code" /></td>
                <td class="font-mono text-xs">{{ l.interface }}</td>
                <td class="font-mono text-xs">-{{ l.cost }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <UiPagination :page="logsPage" :total="logsTotal" :page-size="logsPageSize" @change="goLogsPage" />
      </template>
    </section>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, nextTick } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import UiTag from '../components/UiTag.vue'
import UiSpinner from '../components/UiSpinner.vue'
import UiEmpty from '../components/UiEmpty.vue'
import UiPagination from '../components/UiPagination.vue'
import { quotaApi, adminApi, userConfigApi } from '../api/index.js'
import { fmtDate, revealObserve } from '../utils.js'

const loading = ref(true)
const error = ref('')
const sources = ref([])
const isAdmin = ref(false)
const planName = ref('')
const planExpireAt = ref(null)
const activeUsers = ref(0)
const callCount = ref(0)
const logs = ref([])
const logsTotal = ref(0)
const logsPage = ref(1)
const logsPageSize = ref(10)
const logsLoading = ref(true)
const hasImportUrl = ref(false)

const subtitle = computed(() => {
  return isAdmin.value
    ? '各平台当日全站消耗，北京时间每日零点进入新周期。'
    : '各平台独立计费，每日零点重置。'
})

const summaryCards = computed(() => {
  const used = sources.value.reduce((n, s) => n + (s.used_today || 0), 0)
  if (isAdmin.value) {
    return [
      { label: '平台数', value: sources.value.length },
      { label: '今日全站消耗', value: used },
      { label: '今日活跃用户', value: activeUsers.value },
      { label: '今日调用次数', value: callCount.value },
    ]
  }
  const limited = sources.value.filter(s => (s.effective_total ?? 0) >= 0)
  const unlimitedCount = sources.value.length - limited.length
  let total, remaining
  if (unlimitedCount === 0) {
    total = limited.reduce((n, s) => n + (s.effective_total || 0), 0)
    remaining = limited.reduce((n, s) => n + (s.remaining || 0), 0)
  } else if (limited.length === 0) {
    total = remaining = '不限'
  } else {
    total = limited.reduce((n, s) => n + (s.effective_total || 0), 0) + ' + 不限'
    remaining = limited.reduce((n, s) => n + (s.remaining || 0), 0) + ' + 不限'
  }
  return [
    { label: '平台数', value: sources.value.length },
    { label: '今日总额度', value: total },
    { label: '今日已用', value: used },
    { label: '今日剩余', value: remaining },
  ]
})

const goLogsPage = (p) => { logsPage.value = p; loadLogs() }

// group_code 列存数据源码，按数据源显示名展示
const groupNameOf = (code) => {
  const hit = sources.value.find(s => s.source_code === code)
  return hit ? hit.name : code
}

const loadLogs = async () => {
  logsLoading.value = true
  try {
    const data = isAdmin.value
      ? await adminApi.listUsageLogs({ page: logsPage.value, pageSize: logsPageSize.value })
      : await quotaApi.myUsageLogs({ page: logsPage.value, pageSize: logsPageSize.value })
    logs.value = (data.list || []).map(l => ({ ...l, group_name: l.group_name || groupNameOf(l.group_code) }))
    logsTotal.value = data.total || 0
    logsPageSize.value = data.page_size || 10
  } catch (e) { /* 流水加载失败不影响主内容 */ }
  logsLoading.value = false
  nextTick(revealObserve)
}

const usageTone = (pct) => {
  if (pct >= 90) return 'red'
  if (pct >= 60) return 'yellow'
  return 'green'
}

const fmtQuota = (v) => (v ?? 0) >= 0 ? v : '不限'

const planExpireSoon = computed(() => {
  if (!planExpireAt.value) return false
  return new Date(planExpireAt.value) - Date.now() < 3 * 24 * 3600 * 1000
})

const load = async () => {
  try {
    const data = await quotaApi.dashboard()
    sources.value = data.sources || []
    isAdmin.value = data.is_admin || false
    planName.value = data.plan_name || ''
    planExpireAt.value = data.plan_expire_at || null
    activeUsers.value = data.active_users || 0
    callCount.value = data.call_count || 0
  } catch (e) {
    error.value = e.message
  }
  loading.value = false
  nextTick(revealObserve)
  loadLogs()
}

onMounted(() => { revealObserve() })
load()

// 书源直链未配置时隐藏入口提示
const loadImportConfig = async () => {
  try {
    const data = await userConfigApi.getImportConfig()
    hasImportUrl.value = !!data.legado_import_url
    nextTick(revealObserve)
  } catch (e) { /* 未配置时隐藏提示 */ }
}
loadImportConfig()
</script>
