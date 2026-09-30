<template>
  <div>
    <PageHeader title="用量流水" subtitle="额度消耗记录。计费中间件尚未启用，当前可能暂无数据。" />

    <div class="reveal flex flex-col sm:flex-row gap-3 mb-6">
      <input v-model="filters.username" placeholder="用户名" class="input sm:w-40 font-mono" @keydown.enter="doSearch">
      <select v-model="filters.sourceCode" class="input sm:w-48">
        <option value="">全部数据源</option>
        <option v-for="s in sourceCodes" :key="s" :value="s">{{ s }}</option>
      </select>
      <button @click="doSearch" class="btn-ghost whitespace-nowrap">筛选</button>
    </div>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <UiEmpty v-else-if="list.length === 0" title="暂无流水记录" text="额度计费中间件启用后，这里将展示每次数据源调用的消耗明细。" />
    <template v-else>
      <div class="table-wrap reveal overflow-x-auto">
        <table class="table-base">
          <thead>
            <tr>          <th>ID</th><th>用户</th><th>数据源</th><th>接口</th><th>消耗</th><th>时间</th></tr>
          </thead>
          <tbody>
            <tr v-for="l in list" :key="l.id">
              <td class="font-mono text-xs">{{ l.id }}</td>
              <td>
                <span class="font-medium">{{ l.username || ('#' + l.user_id) }}</span>
              </td>
              <td><UiTag tone="blue" :label="l.group_code" /></td>
              <td class="font-mono text-xs">{{ l.interface }}</td>
              <td class="font-mono text-xs">{{ l.cost }}</td>
              <td class="font-mono text-xs text-text-muted">{{ fmtDate(l.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <UiPagination :page="page" :total="total" :page-size="pageSize" @change="goPage" />
    </template>
  </div>
</template>

<script setup>
import { ref, onMounted, nextTick } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiTag from '../../components/UiTag.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import UiPagination from '../../components/UiPagination.vue'
import { adminApi } from '../../api/index.js'
import { fmtDate, revealObserve } from '../../utils.js'

const loading = ref(true)
const error = ref('')
const list = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const filters = ref({ username: '', sourceCode: '' })
const sourceCodes = ref([])

const doSearch = () => { page.value = 1; load() }
const goPage = (p) => { page.value = p; load() }

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    const data = await adminApi.listUsageLogs({
      page: page.value,
      username: filters.value.username.trim(),
      group: filters.value.sourceCode,
    })
    list.value = data.list || []
    total.value = data.total || 0
    pageSize.value = data.page_size || 20
    sourceCodes.value = data.source_codes || []
  } catch (e) {
    error.value = e.message
  }
  loading.value = false
  nextTick(revealObserve)
}

onMounted(() => { revealObserve() })
load()
</script>
