<template>
  <div>
    <PageHeader title="设备与密钥" subtitle="按账号看近期的登录设备、来源 IP 与密钥数量。这一页只给读数；自动处置只在登录时按「活跃会话数」发生，IP 数再多也不踢人。" />

    <!-- 口径跟着读数走：「8 个 IP 算不算异常」取决于当时配的阈值与开关，不写出来这页就没法读 -->
    <div v-if="cfg" class="reveal card p-4 mb-6 text-sm leading-relaxed">
      <div class="flex flex-wrap gap-x-6 gap-y-2">
        <span><span class="text-text-muted">自动处置：</span>
          <UiTag :tone="cfg.device_watch_enabled ? 'green' : 'gray'"
                 :label="cfg.device_watch_enabled ? '已开启' : '未开启（只读数）'" /></span>
        <span><span class="text-text-muted">活跃会话上限：</span><span class="font-mono">{{ cfg.max_active_sessions }}</span></span>
        <span><span class="text-text-muted">标红阈值（不同 IP 数）：</span><span class="font-mono">{{ cfg.suspect_distinct_ips }}</span></span>
        <span><span class="text-text-muted">统计窗口：</span><span class="font-mono">{{ cfg.window_days }}</span> 天</span>
      </div>
      <p v-if="!cfg.device_watch_enabled" class="text-xs text-text-muted mt-2">
        开关在「系统设置」的 <span class="font-mono">device_watch_enabled</span>。
        关闭时超上限的会话不会被移出——开启前请先看几天读数，避免误伤家庭与出口 NAT 后的多真人。
      </p>
    </div>

    <div class="reveal flex flex-col sm:flex-row gap-3 mb-4">
      <input v-model="keyword" placeholder="搜索用户名 / 邮箱 / 昵称" class="input flex-1" @keydown.enter="doSearch">
      <button @click="doSearch" class="btn-ghost whitespace-nowrap">搜索</button>
    </div>

    <!-- 窄屏可排序：桌面端点表头，移动端那些列本身被裁掉了（P30），表头点不到，
         所以并排给一个下拉——排序这件事在两种屏幕上都得做得到。 -->
    <div class="reveal flex flex-wrap items-center gap-2 mb-4 text-xs text-text-muted">
      <span>排序</span>
      <select v-model="sort" class="input input-sm sm:hidden" @change="applySort">
        <option v-for="c in SORTS" :key="c.key" :value="c.key">{{ c.label }}</option>
      </select>
      <span class="hidden sm:inline font-mono">{{ currentSortLabel }} · {{ dir === 'desc' ? '从大到小' : '从小到大' }}（点表头可切换）</span>
      <button @click="toggleDir" class="btn-ghost btn-sm whitespace-nowrap">{{ dir === 'desc' ? '降序 ▼' : '升序 ▲' }}</button>
      <button v-if="sort !== defaultSort || dir !== 'desc'" @click="resetSort" class="btn-ghost btn-sm whitespace-nowrap">回默认排序</button>
    </div>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <UiEmpty v-else-if="list.length === 0" title="暂无数据" text="没有匹配的用户。" />
    <template v-else>
      <div class="table-wrap reveal overflow-x-auto">
        <table class="table-base">
          <thead>
            <tr>
              <th>用户</th><th>套餐</th>
              <th><UiSortHead label="活跃会话" :active="sort === 'sessions'" :dir="dir" @pick="pickSort('sessions')" /></th>
              <th class="hidden md:table-cell"><UiSortHead label="登录设备" :active="sort === 'devices'" :dir="dir" @pick="pickSort('devices')" /></th>
              <th class="hidden md:table-cell"><UiSortHead label="登录 IP" :active="sort === 'login_ips'" :dir="dir" @pick="pickSort('login_ips')" /></th>
              <th><UiSortHead label="调用 IP" :active="sort === 'call_ips'" :dir="dir" @pick="pickSort('call_ips')" /></th>
              <th class="hidden md:table-cell"><UiSortHead label="密钥" :active="sort === 'keys'" :dir="dir" @pick="pickSort('keys')" /></th>
              <th class="hidden md:table-cell"><UiSortHead label="近期移出" :active="sort === 'revoked'" :dir="dir" @pick="pickSort('revoked')" /></th>
              <th class="hidden lg:table-cell"><UiSortHead label="最近登录" :active="sort === 'last_login'" :dir="dir" @pick="pickSort('last_login')" /></th>
              <th>标记</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="d in list" :key="d.user_id">
              <td class="text-sm">
                <router-link :to="'/admin/users?keyword=' + encodeURIComponent(d.username)" class="hover:text-text transition-colors">
                  {{ d.username }}
                </router-link>
              </td>
              <td class="text-xs text-text-muted">{{ d.plan_name || '免费版' }}</td>
              <td class="font-mono text-sm" :class="overCap(d) ? 'text-pale-red-fg' : ''">{{ d.active_sessions }}</td>
              <td class="hidden md:table-cell font-mono text-xs text-text-muted">{{ d.login_devices }}</td>
              <td class="hidden md:table-cell font-mono text-xs text-text-muted">{{ d.login_ips }}</td>
              <td class="font-mono text-xs">{{ d.call_ips }}</td>
              <td class="hidden md:table-cell font-mono text-xs text-text-muted">{{ d.api_keys }}</td>
              <td class="hidden md:table-cell font-mono text-xs text-text-muted">{{ d.revoked_recent }}</td>
              <td class="hidden lg:table-cell font-mono text-xs text-text-muted whitespace-nowrap">{{ fmtDate(d.last_login_at) }}</td>
              <td>
                <UiTag v-if="d.suspect" tone="yellow" label="IP 分散" />
                <UiTag v-else-if="overCap(d)" tone="red" :label="'超上限 ' + (cfg ? cfg.max_active_sessions : '')" />
                <span v-else class="text-xs text-text-muted">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="text-xs text-text-muted mt-3 leading-relaxed">
        「调用 IP」含用 API 密钥发起的调用（密钥调用归属到用户名下），所以它比「登录 IP」更能反映实际使用者；
        两者都只标红不处置。<span class="font-mono">api_keys</span> 表没有来源 IP 记录，
        因此这页答不了「具体哪把密钥在被别人用」，只能答「这个账号有多少把密钥、从几个 IP 来过」。
      </p>
      <UiPagination :page="page" :total="total" :page-size="pageSize" @change="goPage" />
    </template>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, nextTick } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiTag from '../../components/UiTag.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import UiPagination from '../../components/UiPagination.vue'
import UiSortHead from '../../components/UiSortHead.vue'
import { adminApi } from '../../api/index.js'
import { fmtDate, toast, revealObserve } from '../../utils.js'

const loading = ref(true)
const error = ref('')
const list = ref([])
const cfg = ref(null)
const total = ref(0)
const page = ref(1)
const pageSize = 20
const keyword = ref('')

// 排序键与后端 deviceSortAllowed 一一对应（键名不许自己起：非法值后端回默认，
// 面板就会「点了表头但没反应」——那是最容易被当成「这页坏了」的错）
const defaultSort = 'sessions'
const SORTS = [
  { key: 'sessions', label: '活跃会话' },
  { key: 'devices', label: '登录设备' },
  { key: 'login_ips', label: '登录 IP' },
  { key: 'call_ips', label: '调用 IP' },
  { key: 'keys', label: '密钥' },
  { key: 'revoked', label: '近期移出' },
  { key: 'last_login', label: '最近登录' },
]
const sort = ref(defaultSort)
const dir = ref('desc')
const currentSortLabel = computed(() => (SORTS.find(c => c.key === sort.value) || {}).label || sort.value)

const overCap = (d) => !!cfg.value && d.active_sessions > cfg.value.max_active_sessions

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    const data = await adminApi.listDeviceActivity({
      page: page.value, keyword: keyword.value.trim(), sort: sort.value, dir: dir.value,
    })
    list.value = data.list || []
    cfg.value = data.config || null
    total.value = data.total || 0
    // 后端把生效的 sort/dir 回显出来（非法值会被它兜掉），面板跟着它而不是跟着本地值，
    // 否则「我点了、它没理我」这一类就只在读数上看不出来
    sort.value = data.sort || sort.value
    dir.value = data.dir || dir.value
  } catch (e) {
    total.value = -1 // 读失败写成"不可用"，不许留成 0（待办清单 P99 列表页那一格）
    error.value = e.message
    toast(e.message, 'error')
  }
  loading.value = false
  nextTick(revealObserve)
}

const applySort = () => { page.value = 1; load() }
const pickSort = (key) => {
  if (sort.value === key) dir.value = dir.value === 'desc' ? 'asc' : 'desc'
  else { sort.value = key; dir.value = 'desc' }
  applySort()
}
const toggleDir = () => { dir.value = dir.value === 'desc' ? 'asc' : 'desc'; applySort() }
const resetSort = () => { sort.value = defaultSort; dir.value = 'desc'; applySort() }

const doSearch = () => { page.value = 1; load() }
const goPage = (p) => { page.value = p; load() }

onMounted(() => { revealObserve() })
load()
</script>
