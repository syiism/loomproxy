<template>
  <div>
    <PageHeader title="排行榜" subtitle="站内搜索热词与在读书目排名，取自接口调用明细的内容维度（只统计窗口内的真实调用）。">
      <template #actions>
        <select v-model.number="days" class="input font-mono text-sm w-auto" @change="load">
          <option :value="1">今天</option>
          <option :value="7">近 7 天</option>
          <option :value="30">近 30 天</option>
        </select>
      </template>
    </PageHeader>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="denied" title="榜单未开放" text="管理员尚未在「系统设置 · 站点」开启「普通用户可见排行榜」。" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />

    <div v-else-if="boards.length" class="grid grid-cols-1 lg:grid-cols-2 gap-4 md:gap-6 items-start">
      <section v-for="b in boards" :key="b.dim" class="card reveal">
        <div class="flex items-baseline justify-between gap-3 mb-4">
          <div class="font-serif text-lg font-medium tracking-tight">{{ b.title }}</div>
          <div class="font-mono text-xs text-text-muted">{{ b.rows.length }} 项 · 合计 {{ b.totalCalls }}</div>
        </div>

        <UiEmpty v-if="b.rows.length === 0" title="窗口内没有数据" :text="b.emptyHint" />
        <ol v-else class="divide-y divide-border">
          <li v-for="(it, i) in b.rows" :key="it.name" class="flex items-center gap-3 py-2.5 first:pt-0 last:pb-0">
            <span class="w-6 shrink-0 font-mono text-xs" :class="i < 3 ? 'text-text' : 'text-text-muted'">{{ i + 1 }}</span>
            <div class="min-w-0 flex-1">
              <div class="truncate text-sm" :title="it.name">{{ it.name }}</div>
            </div>
            <span class="shrink-0 font-serif text-lg tracking-tight">{{ it.total }}</span>
          </li>
        </ol>
      </section>
    </div>

    <p v-if="!loading && !error && !denied && boards.length" class="mt-5 text-xs leading-relaxed text-text-muted">
      口径：阅读榜只统计正文（content）接口——一次「打开书目」会连着产生详情与多页目录，
      全计入等于把同一本书凭空乘上几倍；搜索热词榜统计全部搜索请求。名称靠「标识 → 名称」
      缓存跨请求反查，只在服务进程内留存（24 小时、上限 2 万条），重启后或从未请求过该书的前序
      接口时，正文条目会因反查不到书名而不入榜。本页只取聚合的名称与次数，不含调用者、时间与
      数据源明细（那些在管理端「监控」页）；是否对普通用户开放由管理员在「系统设置 · 站点」决定。
    </p>
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted, nextTick } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import UiSpinner from '../components/UiSpinner.vue'
import UiEmpty from '../components/UiEmpty.vue'
import { adminApi } from '../api/index.js'
import { revealObserve } from '../utils.js'

const EMPTY_HINTS = {
  keyword: '窗口内没有任何搜索请求留下搜索词。',
  book: '窗口内没有留下书名的正文调用（只统计正文接口，打开书目与翻目录都不计入）。',
}
const loading = ref(true)
const error = ref('')
const denied = ref(false)
const days = ref(7)
const boards = ref([])

const load = async () => {
  loading.value = true
  error.value = ''
  denied.value = false
  try {
    // 公开榜端点：只回聚合的名称与次数，与管理员用的 /admin/monitor/subjects 错开
    const data = await adminApi.rankBoards(days.value)
    boards.value = (data.boards || []).map(b => ({
      ...b,
      emptyHint: EMPTY_HINTS[b.dim] || '窗口内没有数据。',
      totalCalls: (b.rows || []).reduce((n, it) => n + (it.total || 0), 0),
    }))
  } catch (e) {
    if (e.status === 403) denied.value = true
    else error.value = e.message
  } finally {
    loading.value = false
    // 必须在 loading 落回 false 之后再观察：卡片的 .reveal 此刻才存在于 DOM，
    // 而 revealObserve 只观察调用时已存在的 .reveal:not(.in)，观察不到就永远停在 opacity:0
    nextTick(revealObserve)
  }
}

// 榜单是实时聚合，且名称反查依赖进程内缓存（重启即空）——
// 切回标签页时主动刷一次，否则看到的是上次进入时的快照
const onVisible = () => { if (document.visibilityState === 'visible' && !loading.value) load() }
onMounted(() => { load(); document.addEventListener('visibilitychange', onVisible) })
onUnmounted(() => document.removeEventListener('visibilitychange', onVisible))
</script>
