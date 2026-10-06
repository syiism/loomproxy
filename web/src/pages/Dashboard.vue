<template>
  <div>
    <PageHeader :title="isAdmin ? '每日数据源额度' : '我的额度'" :subtitle="subtitle">
      <template #actions>
        <UiTag v-if="planName && !isAdmin" tone="blue" :label="planName" />
        <span v-if="planExpireAt && !isAdmin" class="text-xs font-mono" :class="planExpireSoon ? '' : 'text-text-muted'" :style="planExpireSoon ? 'color:#956400' : ''">{{ fmtDate(planExpireAt) }} 到期</span>
      </template>
    </PageHeader>

    <!-- 书源导入入口提示：仅在书源文件已托管就位时展示 -->
    <router-link v-if="bookSourceReady" to="/profile" class="card reveal card-hover mb-6 md:mb-8 flex items-center justify-between gap-3 group">
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

      <!-- 额度转移（待办清单 P97）：只在自己的两个源之间重排**日限额增量**。
           可选列表用后端的 `effective_total >= 0` 判据，与网关拒「不限额端」是同一条 —— 遮按钮不算校验。
           放在四张汇总卡正下方（维护者 2026-10-06 指定）：这是唯一会改写卡上数字的用户操作，不该沉到页尾。 -->
      <section v-if="!isAdmin" class="mb-8 md:mb-10 reveal">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">额度转移</h2>
        <p class="text-sm text-text-muted leading-relaxed mb-4">
          把自己两个源之间的<strong>当日限额</strong>重排：转出方减、转入方加，总数不变。
          已用量与流水不回改；不限额的源不参与，目标源必须是你有权限的。
        </p>
        <div class="flex flex-wrap items-end gap-3">
          <label class="flex flex-col gap-1 text-xs text-text-muted">从
            <select v-model="tf.from" class="input font-mono">
              <option v-for="s in numericSources" :key="'f'+s.source_code" :value="s.source_code">{{ s.name }}（{{ s.effective_total }}）</option>
            </select>
          </label>
          <label class="flex flex-col gap-1 text-xs text-text-muted">到
            <select v-model="tf.to" class="input font-mono">
              <option v-for="s in numericSources" :key="'t'+s.source_code" :value="s.source_code">{{ s.name }}（{{ s.effective_total }}）</option>
            </select>
          </label>
          <label class="flex flex-col gap-1 text-xs text-text-muted">数量
            <input v-model.number="tf.amount" type="number" min="1" class="input w-24 font-mono">
          </label>
          <button type="button" class="btn-primary" :disabled="tf.saving || !tfReady" @click="onTransfer">
            {{ tf.saving ? '转移中' : '转移' }}
          </button>
        </div>
        <!-- 转移成功的**持久**回显：toast 会消失，而「到底挪没挪」的怀疑不会——
             before→after 落在页面上与重拉后的下拉数字互相印证（onTransfer 不在本地加减数字）。 -->
        <p v-if="tf.result" class="mt-4 text-sm text-pale-green-fg">
          最近一次转移：{{ groupNameOf(tf.result.from.code) }} {{ tf.result.from.before }}→{{ tf.result.from.after }}，
          {{ groupNameOf(tf.result.to.code) }} {{ tf.result.to.before }}→{{ tf.result.to.after }}（共 {{ tf.result.amount }}）。上方卡片与下拉已按服务端重算刷新。
        </p>
        <!-- 历史来自 GET /quota/transfers（服务端分页，这里只取最近 5 条）；口径由响应的 scope 一格下发，
             面板不自己复述"改的是增量还是当日已用"。空列表整块不渲染——从没转过的页面上它是噪声。 -->
        <div v-if="tfHistory.length" class="mt-5">
          <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-2">最近转移</div>
          <div class="space-y-1.5 text-sm">
            <div v-for="h in tfHistory" :key="h.id" class="flex flex-wrap items-baseline justify-between gap-x-3">
              <span class="font-mono text-xs text-text-muted">{{ fmtDate(h.created_at) }}</span>
              <span>{{ groupNameOf(h.from) }} <span class="font-mono">{{ h.from_before }}→{{ h.from_after }}</span>，{{ groupNameOf(h.to) }} <span class="font-mono">{{ h.to_before }}→{{ h.to_after }}</span>（{{ h.amount }}）</span>
            </div>
          </div>
        </div>
      </section>

      <!-- 筛选栏：分组（归类视图）+ 名称/源码/分组名搜索 -->
      <div v-if="groups.length" class="flex flex-col sm:flex-row sm:items-center gap-3 mb-6 md:mb-8">
        <div class="flex items-center gap-2 overflow-x-auto pb-1">
          <button v-for="c in chips" :key="c.key" type="button" class="btn btn-sm shrink-0"
            :class="groupFilter === c.key ? 'bg-accent text-white' : 'border border-border text-text-muted hover:text-text'"
            @click="pickGroup(c.key)">
            <span>{{ c.label }}</span>
            <span class="font-mono">{{ c.count }}</span>
          </button>
        </div>
        <input v-model.trim="query" type="search" class="input input-sm sm:w-64 sm:ml-auto" placeholder="搜索名称 / 源码 / 分组" />
      </div>
      <div v-else class="flex justify-end mb-6 md:mb-8">
        <input v-model.trim="query" type="search" class="input input-sm w-full sm:w-64" placeholder="搜索名称 / 源码" />
      </div>

      <UiEmpty v-if="sections.length === 0" title="没有匹配的数据源" text="换个关键词，或切回「全部」分组。" />

      <!-- 各数据源明细：按分组分节，节内 faux-OS 窗口卡片 -->
      <section v-for="sec in sections" :key="sec.key" class="mb-10 last:mb-0">
        <div v-if="sec.name" class="flex items-center gap-4 mb-5">
          <h2 class="font-serif text-lg md:text-xl font-medium tracking-tight whitespace-nowrap">{{ sec.name }}</h2>
          <span class="font-mono text-xs uppercase tracking-wider text-text-muted whitespace-nowrap">{{ sec.items.length }} 个平台</span>
          <span class="flex-1 h-px bg-border"></span>
        </div>
        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 md:gap-6">
          <div v-for="s in sec.items" :key="s.source_code" class="reveal border border-border rounded-xl overflow-hidden bg-surface card-hover">
            <div class="flex items-center gap-1.5 px-4 py-2.5 border-b border-border bg-surface-alt">
              <!-- 额度指示灯：亮几颗与颜色都由 quotaBand(剩余额度) 决定，与右上角百分比标签同一处判据
                   （待办清单 P42）。没有额度概念（不限额、管理员视角）时一颗都不亮——那是缺格，不是绿档。 -->
              <span v-for="i in 3" :key="i" class="w-2.5 h-2.5 rounded-full"
                    :class="quotaLitCount(quotaBandOf(s)) >= i ? QUOTA_DOT_CLASS[quotaBandOf(s)] : 'bg-border'"></span>
              <span class="ml-2 font-mono text-xs text-text-muted">{{ s.source_code }}</span>
            </div>
            <div class="p-5 md:p-6">
              <div class="flex items-center justify-between mb-4">
                <div class="font-medium text-base">{{ s.name }}</div>
                <UiTag v-if="isAdmin" tone="blue" :label="'活跃 ' + (s.active_users || 0)" />
                <UiTag v-else :tone="quotaTag(s).tone" :label="quotaTag(s).label" />
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
                <!-- 覆盖增量是 P97 之后 dashboard 才开始说真话的一格：转移不随每日刷新回退，
                     "日额度怎么跟别人不一样"的答案在这里。未设置整行不渲染（row 是噪声）。 -->
                <div v-if="!isAdmin && s.override && s.override !== '未设置'" class="flex justify-between">
                  <span class="text-text-muted">覆盖增量</span><span class="font-mono text-xs">{{ s.override }}</span>
                </div>
                <div class="flex justify-between"><span class="text-text-muted">下次重置</span><span class="font-mono text-xs">{{ s.next_reset }}</span></div>
              </div>

              <div v-if="isAdmin" class="mt-4 pt-4 border-t border-border space-y-3">
                <div>
                  <router-link to="/admin/interfaces" class="font-mono text-xs uppercase tracking-wider text-text-muted mb-2 hover:text-text transition-colors" title="只读；消耗与限流的编辑都在「接口限流」页">接口消耗 ↗</router-link>
                  <div class="flex flex-wrap gap-1.5">
                    <span v-for="i in s.interfaces" :key="i.name" class="px-2 py-0.5 rounded text-xs font-mono" :class="i.enabled ? 'bg-pale-green-bg text-pale-green-fg' : 'bg-pale-gray-bg text-text-muted'">{{ i.name }} ×{{ i.enabled ? i.cost : 0 }}</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </section>
    </template>

    <!-- 调用流水：管理员看全站，普通用户看自己的 -->
    <section class="mt-12 md:mt-16 reveal">
      <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">{{ isAdmin ? '全站调用流水' : '调用流水' }}</h2>
      <UiSpinner v-if="logsLoading" />
      <UiEmpty v-else-if="logs.length === 0 && logsTotal < 0" title="读数不可用"
               text="流水没读到，不是没有记录。稍后刷新；服务端日志里有那一条 ERROR（读库失败，按空结果处理）。" />
      <UiEmpty v-else-if="logs.length === 0" title="暂无调用记录" :text="isAdmin ? '今日还没有用户调用过数据源接口。' : '成功调用数据源接口后，这里会展示每次的额度消耗。'" />
      <template v-else>
        <!-- 移动端 -->
        <div class="sm:hidden space-y-2">
          <div v-for="l in visibleLogs" :key="l.id" class="card !p-4 flex items-center justify-between">
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
              <tr v-for="l in visibleLogs" :key="l.id">
                <td class="font-mono text-xs text-text-muted">{{ fmtDate(l.created_at) }}</td>
                <td v-if="isAdmin" class="text-sm">{{ l.username || ('#' + l.user_id) }}</td>
                <td><UiTag tone="blue" :label="l.group_name || l.group_code" /></td>
                <td class="font-mono text-xs">{{ l.interface }}</td>
                <td class="font-mono text-xs">-{{ l.cost }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <!-- 管理员视角收成「最近 5 条 + 跳转」：整表本来就是 /admin/usage-logs 的无筛选子集，
           在两页各显示一遍只会漂移（待办清单 P28·B1，收法照 P17）。普通用户仍看自己的全量分页。 -->
        <router-link v-if="isAdmin" to="/admin/usage-logs" class="inline-block mt-3 text-xs text-text-muted hover:text-text transition-colors">{{ logsTotal < 0 ? '条数不可用（本次读取失败）' : `共 ${logsTotal} 条` }} · 全部流水与筛选在「调用流水」页 ↗</router-link>
        <UiPagination v-else :page="logsPage" :total="logsTotal" :page-size="logsPageSize" @change="goLogsPage" />
      </template>
    </section>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, nextTick } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import UiTag from '../components/UiTag.vue'
import UiSpinner from '../components/UiSpinner.vue'
import UiEmpty from '../components/UiEmpty.vue'
import UiPagination from '../components/UiPagination.vue'
import { quotaApi, adminApi, userConfigApi } from '../api/index.js'
import { fmtDate, revealObserve, quotaBand, remainingPct, quotaLitCount, QUOTA_DOT_CLASS, toast } from '../utils.js'

const loading = ref(true)
const error = ref('')
const sources = ref([])
// 额度转移的本地状态：from/to/amount + 一条"这次成功的结果"回显（不静默改数字，让用户看得见挪了多少）
const tf = ref({ from: '', to: '', amount: 10, saving: false, result: null })
// 转移历史（P97 的「人也要能查」那一半）：转移改的是**永久**的日限额增量，不随每日刷新回退——
// 半年后"这个源怎么是 0"的人，最该在这一眼看到原因，而不是去怀疑源坏了。
const tfHistory = ref([])
const tfReady = computed(() => !!tf.value.from && !!tf.value.to && tf.value.from !== tf.value.to && (tf.value.amount | 0) > 0)
const groups = ref([])
const ungroupedCount = ref(0)
const groupFilter = ref('all')
const query = ref('')
const isAdmin = ref(false)
const planName = ref('')
const planExpireAt = ref(null)
const activeUsers = ref(0)
const callCount = ref(0)
const logs = ref([])
// 管理员只看最近 5 条——整表在「调用流水」页有带筛选的全量版，这里显示 20 条同内容就是第二份事实（P28·B1）。
// 普通用户看自己的流水，分页照旧，不受这条影响。
const visibleLogs = computed(() => (isAdmin.value ? logs.value.slice(0, 5) : logs.value))
const logsTotal = ref(0)
const logsPage = ref(1)
const logsPageSize = ref(10)
const logsLoading = ref(true)
const bookSourceReady = ref(false)

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
  // 展示口径（维护者 2026-10-06 定）：**还有数字额度就只报数字，全是不限才报「不限」**。
  // 旧写法在混合时给「123 + 不限」——那既不是一个可核对的数，也不是一个明确的档位。
  // 两处要一起记住的代价，写在这里免得下一轮当成 bug 改回去：
  // ①「今日剩余」在混合时是**只统计有限额那些源**的合计，不是全部可见源的合计（不限的源本来就没有"剩余"可言）；
  // ② 被这句丢掉的信息没有消失——每张源卡自己那一格仍显示「不限」，「平台数」卡也还在。
  let total, remaining
  if (limited.length === 0) {
    total = remaining = '不限'
  } else {
    total = limited.reduce((n, s) => n + (s.effective_total || 0), 0)
    remaining = limited.reduce((n, s) => n + (s.remaining || 0), 0)
  }
  return [
    { label: '平台数', value: sources.value.length },
    { label: '今日总额度', value: total },
    { label: '今日已用', value: used },
    { label: '今日剩余', value: remaining },
  ]
})

// 能参与转移的源：限额是非负数字的那些（-1/不限 一律排除，判据与后端同源）
const numericSources = computed(() => sources.value.filter(s => (s.effective_total ?? -1) >= 0))

const onTransfer = async () => {
  if (!tfReady.value || tf.value.saving) return
  tf.value.saving = true
  try {
    const d = await quotaApi.transfer(tf.value.from, tf.value.to, tf.value.amount)
    // 先落持久回显再 toast：回显与重拉后的下拉数字互相印证，「到底挪没挪」不用人去翻流水
    tf.value.result = { amount: d.amount, from: d.from, to: d.to }
    toast(`已转移 ${d.amount}：${d.from.code} ${d.from.before}→${d.from.after}，${d.to.code} ${d.to.before}→${d.to.after}`, 'success')
    await load() // 数字由后端重算，不在本地加减——两侧都要看的是判定读的那一格
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    tf.value.saving = false
  }
}

const goLogsPage = (p) => { logsPage.value = p; loadLogs() }

// 分组按钮：后端只下发在本次可见源里至少有一个成员的组。统计卡不受筛选影响，
// 始终按全部可见源算（额度口径跟着筛选摆动会把「今日剩余」读成组内剩余额度）
const chips = computed(() => {
  const out = [{ key: 'all', label: '全部', count: sources.value.length }]
  for (const g of groups.value) out.push({ key: g.id, label: g.name, count: g.count })
  if (ungroupedCount.value > 0) out.push({ key: 'none', label: '未分组', count: ungroupedCount.value })
  return out
})

const matched = computed(() => {
  const q = query.value.toLowerCase()
  if (!q) return sources.value
  return sources.value.filter(s =>
    [s.name, s.source_code, s.group_name].some(v => String(v || '').toLowerCase().includes(q)))
})

// 一个组都没建时保持平铺（骨架新装即此形态），建了组才分节
const sections = computed(() => {
  const pool = matched.value
  if (!groups.value.length) return [{ key: 'flat', name: '', items: pool }]
  if (groupFilter.value === 'none') return [{ key: 'none', name: '未分组', items: pool.filter(s => !s.group_id) }]
  if (groupFilter.value !== 'all') {
    const hit = groups.value.find(g => g.id === groupFilter.value)
    return hit ? [{ key: hit.id, name: hit.name, items: pool.filter(s => s.group_id === hit.id) }] : []
  }
  const out = []
  for (const g of groups.value) {
    const items = pool.filter(s => s.group_id === g.id)
    if (items.length) out.push({ key: g.id, name: g.name, items })
  }
  const rest = pool.filter(s => !s.group_id)
  if (rest.length) out.push({ key: 'none', name: '未分组', items: rest })
  return out
})

const pickGroup = (key) => {
  groupFilter.value = key
  nextTick(revealObserve)
}

// 筛选/搜索会重排 DOM，新节点上的 .reveal 必须在下一帧被重新观察，否则数据在、整块不可见
watch(query, () => nextTick(revealObserve))

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

// 档位在卡片这一层算一次（quotaBandOf），灯数与百分比标签都从它派生——
// 一张卡片上两个读数共用一套判据，不会出现「灯说黄、标签说绿」（待办清单 P42）
//
// 没有额度概念（不限额、管理员视角）算**满格绿**而不是缺格：
// 三颗不亮的灯读起来像「这里没有数据」，而真相是「这个源不受额度约束」——那是最充裕的一档。
// 「不限」这件事由标签的文字说清，不靠灯去表达。
const quotaBandOf = (s) => quotaBand(remainingPct(s)) ?? 'green'
const quotaTag = (s) => {
  const hasQuota = remainingPct(s) != null
  return { tone: quotaBandOf(s), label: hasQuota ? s.usage_pct + '%' : '不限' }
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
    groups.value = data.groups || []
    ungroupedCount.value = data.ungrouped_count || 0
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
  // 转移历史只在本人视角拉（管理员没有"自己的转移"可看）；读失败不影响主内容
  if (!isAdmin.value) loadTransfers()
}

const loadTransfers = async () => {
  try {
    const data = await quotaApi.myTransfers({ page: 1, pageSize: 5 })
    tfHistory.value = data.list || []
  } catch (e) { /* 历史读不到不影响转移与卡片读数 */ }
}

onMounted(() => { revealObserve() })
load()

// 书源文件未就位时隐藏入口提示（托管在 /data/shuyuan/bookSource.json）
const loadImportConfig = async () => {
  try {
    const data = await userConfigApi.getImportConfig()
    bookSourceReady.value = !!data.ready
    nextTick(revealObserve)
  } catch (e) { /* 未配置时隐藏提示 */ }
}
loadImportConfig()
</script>
