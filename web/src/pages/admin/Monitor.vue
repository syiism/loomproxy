<template>
  <div>
    <PageHeader title="接口监控" subtitle="数据源接口调用统计。内存计数，服务重启后清零，与额度计费无关。" />

    <div class="grid grid-cols-2 md:grid-cols-4 gap-3 mb-6 reveal">
      <div class="card !p-4">
        <div class="text-xs text-text-muted mb-1">总调用</div>
        <div class="font-mono text-xl font-medium">{{ overview.total }}</div>
        <div class="text-xs text-text-muted mt-1">历史累计 {{ overview.lifetime_total }}</div>
      </div>
      <div class="card !p-4">
        <div class="text-xs text-text-muted mb-1">整体成功率</div>
        <div class="font-mono text-xl font-medium" :style="{ color: rateColor(overview.success_rate) }">{{ overview.success_rate.toFixed(1) }}%</div>
      </div>
      <div class="card !p-4">
        <div class="text-xs text-text-muted mb-1">失败次数</div>
        <div class="font-mono text-xl font-medium" :style="overview.failed > 0 ? 'color:#9F2F2D' : ''">{{ overview.failed }}</div>
      </div>
      <div class="card !p-4">
        <div class="text-xs text-text-muted mb-1">统计起点</div>
        <div class="font-mono text-sm mt-1.5">{{ fmtDate(overview.started_at) }}</div>
      </div>
    </div>

    <!-- 近 7 天调用趋势（api_call_logs 明细按天×数据源聚合，与明细保留期一致） -->
    <div class="card reveal mb-6">
      <div class="text-sm font-medium mb-3">近 7 天调用趋势</div>
      <UiTrendChart :days="trend.days" :sources="trend.sources" :rows="trend.rows" />
    </div>

    <div class="reveal flex items-center gap-4 mb-4">
      <button @click="load()" class="btn-ghost btn-sm">刷新</button>
      <label class="text-sm text-text-muted flex items-center gap-1.5 cursor-pointer">
        <input type="checkbox" v-model="autoRefresh"> 每 10 秒自动刷新
      </label>
      <button @click="onReset" class="btn-ghost btn-sm" :style="resetArmed ? 'color:#9F2F2D' : ''">
        {{ resetArmed ? '确认清零？' : '清零计数' }}
      </button>
    </div>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <UiEmpty v-else-if="items.length === 0" title="暂无调用数据" text="有数据源接口被调用后，这里会显示统计。" />
    <template v-else>
      <div class="table-wrap reveal overflow-x-auto mb-8">
        <table class="table-base">
          <thead>
            <tr><th>数据源</th><th>接口</th><th>总调用</th><th>历史累计</th><th>成功</th><th>失败</th><th>成功率</th><th>平均耗时</th><th>最大耗时</th><th>最近调用</th></tr>
          </thead>
          <tbody>
            <tr v-for="r in items" :key="r.source + '/' + r.action">
              <td><UiTag tone="blue" :label="r.source" /></td>
              <td class="font-mono text-xs">{{ r.action }}</td>
              <td class="font-mono text-xs">{{ r.total }}</td>
              <td class="font-mono text-xs text-text-muted">{{ r.lifetime }}</td>
              <td class="font-mono text-xs">{{ r.success }}</td>
              <td class="font-mono text-xs" :style="r.failed > 0 ? 'color:#9F2F2D' : ''">{{ r.failed }}</td>
              <td class="font-mono text-xs" :style="{ color: rateColor(r.success_rate) }">{{ r.success_rate.toFixed(1) }}%</td>
              <td class="font-mono text-xs">{{ r.avg_latency_ms }}ms</td>
              <td class="font-mono text-xs text-text-muted">{{ r.max_latency_ms }}ms</td>
              <td class="font-mono text-xs text-text-muted">{{ fmtDate(r.last_called_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="font-serif text-lg font-medium tracking-tight mb-3 reveal">最近调用</div>
      <div class="table-wrap reveal overflow-x-auto mb-8">
        <table class="table-base">
          <thead>
            <tr><th>时间</th><th>调用者</th><th>IP</th><th>数据源</th><th>接口</th><th>状态</th><th>耗时</th></tr>
          </thead>
          <tbody>
            <tr v-for="(r, i) in recent" :key="i">
              <td class="font-mono text-xs text-text-muted whitespace-nowrap">{{ fmtDate(r.time) }}</td>
              <td class="text-sm">
                <span v-if="r.username" class="font-medium">{{ r.username }}</span>
                <span v-else class="text-text-muted">匿名</span>
              </td>
              <td class="font-mono text-xs text-text-muted">{{ r.ip || '—' }}</td>
              <td><UiTag tone="blue" :label="r.source" /></td>
              <td class="font-mono text-xs">{{ r.action }}</td>
              <td class="font-mono text-xs" :style="{ color: statusColor(r.status) }">{{ r.status }}</td>
              <td class="font-mono text-xs">{{ r.latency_ms }}ms</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <div class="flex flex-col sm:flex-row sm:items-center gap-3 mb-3 reveal">
      <div class="font-serif text-lg font-medium tracking-tight flex-1">历史调用</div>
      <input v-model="historyFilter.source" placeholder="数据源" class="input input-sm sm:w-32 font-mono" @keydown.enter="loadHistory(1)">
      <input v-model="historyFilter.username" placeholder="调用者" class="input input-sm sm:w-32 font-mono" @keydown.enter="loadHistory(1)">
      <button @click="loadHistory(1)" class="btn-ghost btn-sm">筛选</button>
    </div>
    <div class="text-xs text-text-muted mb-3 reveal">内存缓冲淘汰后批量落库的历史记录（保留 7 天）</div>
    <div class="table-wrap reveal overflow-x-auto">
      <table class="table-base">
        <thead>
          <tr><th>ID</th><th>时间</th><th>调用者</th><th>IP</th><th>数据源</th><th>接口</th><th>状态</th><th>耗时</th></tr>
        </thead>
        <tbody>
          <tr v-for="r in history" :key="r.id">
            <td class="font-mono text-xs text-text-muted">{{ r.id }}</td>
            <td class="font-mono text-xs text-text-muted whitespace-nowrap">{{ fmtDate(r.created_at) }}</td>
            <td class="text-sm">
              <span v-if="r.username" class="font-medium">{{ r.username }}</span>
              <span v-else class="text-text-muted">匿名</span>
            </td>
            <td class="font-mono text-xs text-text-muted">{{ r.ip || '—' }}</td>
            <td><UiTag tone="blue" :label="r.source" /></td>
            <td class="font-mono text-xs">{{ r.action }}</td>
            <td class="font-mono text-xs" :style="{ color: statusColor(r.status) }">{{ r.status }}</td>
            <td class="font-mono text-xs">{{ r.latency_ms }}ms</td>
          </tr>
          <tr v-if="history.length === 0">
            <td colspan="8" class="text-center text-sm text-text-muted py-6">暂无历史记录（调用超过 200 条后旧记录才会落库）</td>
          </tr>
        </tbody>
      </table>
    </div>
    <UiPagination :page="historyPage" :total="historyTotal" :page-size="historyPageSize" @change="loadHistory" />
  </div>
</template>

<script setup>
import { ref, watch, onMounted, onUnmounted, nextTick } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiTrendChart from '../../components/UiTrendChart.vue'
import UiTag from '../../components/UiTag.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import UiPagination from '../../components/UiPagination.vue'
import { adminApi } from '../../api/index.js'
import { fmtDate, toast, revealObserve } from '../../utils.js'

const loading = ref(true)
const error = ref('')
const items = ref([])
const recent = ref([])
const overview = ref({ total: 0, success: 0, failed: 0, success_rate: 0, lifetime_total: 0, started_at: null })
const trend = ref({ days: [], sources: [], rows: [] })
const autoRefresh = ref(false)
const resetArmed = ref(false)
const history = ref([])
const historyPage = ref(1)
const historyTotal = ref(0)
const historyPageSize = 20
const historyFilter = ref({ source: '', username: '' })
let timer = null
let resetTimer = null

const rateColor = (r) => r >= 95 ? '#346538' : r >= 80 ? '#956400' : '#9F2F2D'
const statusColor = (s) => s >= 500 ? '#9F2F2D' : s >= 400 ? '#956400' : '#346538'

const load = async (silent) => {
  if (!silent) loading.value = true
  error.value = ''
  try {
    const data = await adminApi.getMonitor()
    items.value = data.items || []
    recent.value = data.recent || []
    overview.value = {
      total: data.total || 0,
      success: data.success || 0,
      failed: data.failed || 0,
      success_rate: data.success_rate || 0,
      lifetime_total: data.lifetime_total || 0,
      started_at: data.started_at,
    }
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
  }
  // 趋势独立加载，失败不影响主表
  try {
    const t = await adminApi.getMonitorTrend()
    trend.value = { days: t.days || [], sources: t.sources || [], rows: t.rows || [] }
  } catch (e) { /* 保留旧数据 */ }
  // 表格在异步加载后才渲染，需重新触发渐入观察，否则 .reveal 元素保持透明
  nextTick(revealObserve)
}

const loadHistory = async (page) => {
  try {
    const data = await adminApi.getMonitorHistory({
      page,
      pageSize: historyPageSize,
      source: historyFilter.value.source || undefined,
      username: historyFilter.value.username || undefined,
    })
    history.value = data.list || []
    historyTotal.value = data.total || 0
    historyPage.value = data.page || 1
    nextTick(revealObserve)
  } catch (e) { toast(e.message, 'error') }
}

const onReset = async () => {
  if (!resetArmed.value) {
    resetArmed.value = true
    clearTimeout(resetTimer)
    resetTimer = setTimeout(() => { resetArmed.value = false }, 3000)
    return
  }
  resetArmed.value = false
  try {
    await adminApi.resetMonitor()
    toast('监控计数已清零', 'success')
    await load(true)
  } catch (e) { toast(e.message, 'error') }
}

watch(autoRefresh, (v) => {
  clearInterval(timer)
  if (v) timer = setInterval(() => load(true), 10000)
})

onMounted(() => { load(); loadHistory(1); revealObserve() })
onUnmounted(() => { clearInterval(timer); clearTimeout(resetTimer) })
</script>
