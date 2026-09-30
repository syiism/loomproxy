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
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />

    <div v-else class="grid grid-cols-1 lg:grid-cols-2 gap-4 md:gap-6 items-start">
      <section v-for="b in boards" :key="b.dim" class="card reveal">
        <div class="flex items-baseline justify-between gap-3 mb-4">
          <div class="font-serif text-lg font-medium tracking-tight">{{ b.title }}</div>
          <div class="font-mono text-xs text-text-muted">{{ b.rows.length }} / {{ b.totalCalls }}</div>
        </div>

        <UiEmpty v-if="b.rows.length === 0" title="窗口内没有数据" :text="b.emptyHint" />
        <ol v-else class="divide-y divide-border">
          <li v-for="(it, i) in b.rows" :key="it.name" class="flex items-center gap-3 py-2.5 first:pt-0 last:pb-0">
            <span class="w-6 shrink-0 font-mono text-xs" :class="i < 3 ? 'text-text' : 'text-text-muted'">{{ i + 1 }}</span>
            <div class="min-w-0 flex-1">
              <div class="truncate text-sm" :title="it.name">{{ it.name }}</div>
              <div class="mt-1 flex flex-wrap items-center gap-1.5">
                <UiTag v-for="s in it.sources" :key="s" tone="pale-gray" :label="s" />
                <span v-if="it.empty_results" class="font-mono text-xs text-pale-yellow-fg"
                      :title="'返回 2xx 但结果为 0 的次数'">空结果 {{ it.empty_results }}</span>
                <span v-if="it.last_called_at" class="font-mono text-xs text-text-muted">{{ relTime(it.last_called_at) }}</span>
              </div>
            </div>
            <span class="shrink-0 font-serif text-lg tracking-tight">{{ it.total }}</span>
          </li>
        </ol>
      </section>
    </div>

    <p v-if="!loading && !error" class="mt-5 text-xs leading-relaxed text-text-muted">
      口径：同一本书的多次搜索/详情/正文调用都会计入对应条目；名称靠「标识 → 名称」缓存跨请求反查，
      只在服务进程内留存（24 小时、上限 2 万条），重启或从未请求过该书的前序接口时，条目会以空名称出现而不上榜。
    </p>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, nextTick } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import UiTag from '../components/UiTag.vue'
import UiSpinner from '../components/UiSpinner.vue'
import UiEmpty from '../components/UiEmpty.vue'
import { adminApi } from '../api/index.js'
import { revealObserve } from '../utils.js'

const TOP_N = 20
const loading = ref(true)
const error = ref('')
const days = ref(7)

const boards = ref([
  {
    dim: 'keyword', title: '搜索热词榜', rows: [], totalCalls: 0,
    emptyHint: '窗口内没有任何搜索请求留下搜索词。',
  },
  {
    dim: 'book', title: '阅读榜 TOP20', rows: [], totalCalls: 0,
    emptyHint: '窗口内没有可归属书名的调用（详情/目录/正文都需先能反查到书名）。',
  },
])

const relTime = (iso) => {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const min = Math.floor((Date.now() - t) / 60000)
  if (min < 1) return '刚刚'
  if (min < 60) return min + ' 分钟前'
  const h = Math.floor(min / 60)
  if (h < 24) return h + ' 小时前'
  return Math.floor(h / 24) + ' 天前'
}

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    const res = await Promise.all(
      boards.value.map(b => adminApi.getMonitorSubjects({ dim: b.dim, days: days.value })))
    boards.value = boards.value.map((b, i) => {
      const items = res[i]?.items || []
      return {
        ...b,
        rows: items.slice(0, TOP_N),
        totalCalls: items.reduce((n, it) => n + (it.total || 0), 0),
      }
    })
    nextTick(revealObserve)
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>
