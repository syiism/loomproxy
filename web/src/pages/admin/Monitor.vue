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

    <!-- 近 7 天调用趋势（api_call_logs 明细按天×数据源聚合；7 天是展示窗口，与明细保留期无关） -->
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

    <!-- 内容维度榜单：某个搜索词/书名/章节/媒介在窗口内被调用得怎么样。
         口径 = 库中未清理明细 + 内存中尚未落库的明细（与总表一致） -->
    <div class="card reveal mb-8">
      <div class="flex flex-wrap items-center gap-2 mb-3">
        <div class="text-sm font-medium flex-1">内容维度榜</div>
        <div class="flex items-center gap-1">
          <button v-for="d in DIMS" :key="d.key" @click="switchDim(d.key)"
                  :class="subjectDim === d.key ? 'btn-primary btn-sm' : 'btn-ghost btn-sm'">
            {{ d.label }}
          </button>
        </div>
        <select v-model="subjectDays" class="input input-sm w-24" @change="loadSubjects">
          <option v-for="d in [1, 3, 7, 30]" :key="d" :value="d">近 {{ d }} 天</option>
        </select>
        <input v-model="subjectSource" list="source-options" placeholder="数据源"
               class="input input-sm w-36 font-mono" @keydown.enter="loadSubjects" @change="loadSubjects">
      </div>
      <div class="mb-2 text-xs text-text-muted">
        两列口径不同：<b>访问人数</b>是同数据源内按用户去重（匿名退到 IP，跨源相加不重复去重），
        <b>请求数</b>是接口调用条数。书名榜按<b>访问人数</b>排序（一条正文明细就是一章，按次数排等于把「谁在读」排成「被翻了多少章」），
        其余维度仍按请求数排序；成功率与平均耗时一律按请求数算。
      </div>
      <UiEmpty v-if="subjectItems.length === 0" title="该窗口内没有可统计的内容维度"
               text="只有真正拿到响应、且源声明了媒介的调用才会入榜。" />
      <div v-else class="table-wrap overflow-x-auto">
        <table class="table-base">
          <thead>
            <tr><th>{{ subjectColumn }}</th><th>访问人数</th><th>请求数</th><th>成功</th><th>失败</th><th>成功率</th><th>平均耗时</th><th>最大耗时</th><th>0 结果</th><th>数据源</th><th>最近</th></tr>
          </thead>
          <tbody>
            <tr v-for="(s, i) in subjectItems" :key="i">
              <td>
                <UiTag v-if="subjectDim === 'media'" :tone="mediaTone(s.name)" :label="s.label || mediaLabel(s.name)" />
                <div v-else class="max-w-56 truncate text-sm" :title="s.name">{{ s.name }}</div>
              </td>
              <td class="font-mono text-xs">{{ s.visitors }}</td>
              <td class="font-mono text-xs">{{ s.requests }}</td>
              <td class="font-mono text-xs">{{ s.success }}</td>
              <td class="font-mono text-xs">{{ s.failed }}</td>
              <td class="font-mono text-xs" :style="{ color: rateColor(s.success_rate) }">{{ s.success_rate.toFixed(1) }}%</td>
              <td class="font-mono text-xs">{{ s.avg_latency_ms }}ms</td>
              <td class="font-mono text-xs">{{ s.max_latency_ms }}ms</td>
              <td class="font-mono text-xs" :style="s.empty_results > 0 ? 'color:#956400' : ''">{{ s.empty_results }}</td>
              <td class="font-mono text-xs text-text-muted">{{ (s.sources || []).join(' ') }}</td>
              <td class="font-mono text-xs text-text-muted whitespace-nowrap">{{ s.last_called_at ? fmtDate(s.last_called_at) : '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="text-xs text-text-muted mt-3 reveal">
        媒介为「未判定」表示源既没声明、响应也没给类型——补 SourceMeta 的 media_type 或 tab 声明即可归位。
        命名缓存当前 {{ subjectCache.books }} 本书名 / {{ subjectCache.chapters }} 条章节名。
        <span v-if="subjectCache.persist"
              :class="(subjectCache.persist.dropped > 0 || subjectCache.persist.failed > 0) ? 'text-pale-red-fg' : ''">
          写 Redis：排队 {{ subjectCache.persist.queued }} 条、队列满丢 {{ subjectCache.persist.dropped }} 条、写失败 {{ subjectCache.persist.failed }} 条<template v-if="subjectCache.persist.dropped > 0 || subjectCache.persist.failed > 0">（丢的是跨重启的名称，内存里这轮仍在）</template>。
        </span>
      </div>
    </div>

    <!-- 数据源候选：启用中的源 + 本会话监控里出现过的源（下线源在 data_sources 里是硬删的、
         进不了下拉，而它们的明细是否已被清掉属运维决定，所以用 datalist 而不是 select——
         下拉可选、也要能手打一个不在列表里的旧源码） -->
    <datalist id="source-options">
      <option v-for="o in sourceOptions" :key="o.code" :value="o.code">{{ o.name }}</option>
    </datalist>

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
            <tr><th>时间</th><th>调用者</th><th>IP</th><th>数据源</th><th>接口</th><th>状态</th><th>耗时</th><th>搜索词</th><th>书名</th><th>章节</th><th>媒介</th><th>结果</th></tr>
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
              <td><div class="max-w-36 truncate text-sm" :title="r.keyword">{{ r.keyword || '—' }}</div></td>
              <td><div class="max-w-36 truncate text-sm" :title="r.book_name">{{ r.book_name || '—' }}</div></td>
              <td><div class="max-w-36 truncate text-sm" :title="r.chapter_title">{{ r.chapter_title || '—' }}</div></td>
              <td><UiTag :tone="mediaTone(r.media)" :label="mediaLabel(r.media)" /></td>
              <td class="font-mono text-xs" :style="r.result_count === 0 ? 'color:#956400' : ''">{{ r.result_count }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <div class="flex flex-col sm:flex-row sm:items-center gap-3 mb-3 reveal">
      <div class="font-serif text-lg font-medium tracking-tight flex-1">历史调用</div>
      <input v-model="historyFilter.source" list="source-options" placeholder="数据源"
             class="input input-sm sm:w-36 font-mono" @keydown.enter="loadHistory(1)" @change="loadHistory(1)">
      <input v-model="historyFilter.username" placeholder="调用者" class="input input-sm sm:w-32 font-mono" @keydown.enter="loadHistory(1)">
      <input v-model="historyFilter.keyword" placeholder="搜索词" class="input input-sm sm:w-32" @keydown.enter="loadHistory(1)">
      <input v-model="historyFilter.bookName" placeholder="书名" class="input input-sm sm:w-32" @keydown.enter="loadHistory(1)">
      <select v-model="historyFilter.mediaType" class="input input-sm sm:w-28" @change="loadHistory(1)">
        <option value="">全部媒介</option>
        <option v-for="m in MEDIAS" :key="m.value" :value="m.value">{{ m.label }}</option>
      </select>
      <button @click="loadHistory(1)" class="btn-ghost btn-sm">筛选</button>
    </div>
    <div class="text-xs text-text-muted mb-3 reveal">内存缓冲淘汰后批量落库的历史记录（保留期由环境变量 MONITOR_RETENTION_DAYS 决定，默认永久）。搜索词与书名属用户阅读内容，仅管理员可见。</div>
    <div class="table-wrap reveal overflow-x-auto">
      <table class="table-base">
        <thead>
          <tr><th>ID</th><th>时间</th><th>调用者</th><th>IP</th><th>数据源</th><th>接口</th><th>状态</th><th>耗时</th><th>搜索词</th><th>书名</th><th>章节</th><th>媒介</th><th>结果</th></tr>
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
            <td><div class="max-w-36 truncate text-sm" :title="r.keyword">{{ r.keyword || '—' }}</div></td>
            <td><div class="max-w-36 truncate text-sm" :title="r.book_name">{{ r.book_name || '—' }}</div></td>
            <td><div class="max-w-36 truncate text-sm" :title="r.chapter_title">{{ r.chapter_title || '—' }}</div></td>
            <td><UiTag :tone="mediaTone(r.media_type)" :label="mediaLabel(r.media_type)" /></td>
            <td class="font-mono text-xs" :style="r.result_count === 0 ? 'color:#956400' : ''">{{ r.result_count }}</td>
          </tr>
          <tr v-if="history.length === 0">
            <td colspan="13" class="text-center text-sm text-text-muted py-6">暂无历史记录（调用超过 200 条后旧记录才会落库）</td>
          </tr>
        </tbody>
      </table>
    </div>
    <UiPagination :page="historyPage" :total="historyTotal" :page-size="historyPageSize" @change="loadHistory" />
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiTrendChart from '../../components/UiTrendChart.vue'
import UiTag from '../../components/UiTag.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import UiPagination from '../../components/UiPagination.vue'
import { adminApi, miscApi } from '../../api/index.js'
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
const historyFilter = ref({ source: '', username: '', keyword: '', bookName: '', mediaType: '' })
const subjectDim = ref('keyword')
const subjectDays = ref(7)
const subjectSource = ref('')
const subjectItems = ref([])
const sourceOptions = ref([])
const subjectCache = ref({ books: 0, chapters: 0, persist: null })
let timer = null
let resetTimer = null

// 媒介枚举与骨架 base/media.go 对齐；未判定用 gray，让它在一堆淡彩标签里一眼可辨
const MEDIAS = [
  { value: 'novel', label: '小说', tone: 'blue' },
  { value: 'audio', label: '音频', tone: 'yellow' },
  { value: 'comic', label: '漫画', tone: 'green' },
  { value: 'video', label: '视频', tone: 'red' },
]
const DIMS = [
  { key: 'keyword', label: '搜索词', column: '搜索词' },
  { key: 'book', label: '书名（正文）', column: '书名' },
  { key: 'chapter', label: '章节（正文）', column: '章节标题' },
  { key: 'media', label: '媒介', column: '媒介类型' },
]
const mediaLabel = (m) => (MEDIAS.find((x) => x.value === m) || {}).label || '未判定'
const mediaTone = (m) => (MEDIAS.find((x) => x.value === m) || {}).tone || 'gray'
const subjectColumn = computed(() => (DIMS.find((d) => d.key === subjectDim.value) || {}).column || '名称')

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
    const f = historyFilter.value
    const data = await adminApi.getMonitorHistory({
      page,
      pageSize: historyPageSize,
      source: f.source || undefined,
      username: f.username || undefined,
      keyword: f.keyword || undefined,
      bookName: f.bookName || undefined,
      mediaType: f.mediaType || undefined,
    })
    history.value = data.list || []
    historyTotal.value = data.total || 0
    historyPage.value = data.page || 1
    nextTick(revealObserve)
  } catch (e) { toast(e.message, 'error') }
}

const loadSubjects = async () => {
  try {
    const data = await adminApi.getMonitorSubjects({
      dim: subjectDim.value,
      days: subjectDays.value,
      source: subjectSource.value || undefined,
    })
    subjectItems.value = data.items || []
    subjectCache.value = data.name_cache || { books: 0, chapters: 0, persist: null }
    nextTick(revealObserve)
  } catch (e) { toast(e.message, 'error') }
}

const switchDim = (key) => { subjectDim.value = key; loadSubjects() }

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

// 候选来自 /datasources（启用中的源，带展示名），加载失败不影响筛选本身（仍可手输）
const loadSourceOptions = async () => {
  try {
    const list = await miscApi.datasources()
    sourceOptions.value = (list || []).map(d => ({ code: d.id, name: d.name || d.id }))
  } catch (e) { /* 静默：筛选手输仍可用 */ }
}

onMounted(() => { load(); loadSubjects(); loadHistory(1); loadSourceOptions(); revealObserve() })
onUnmounted(() => { clearInterval(timer); clearTimeout(resetTimer) })
</script>
