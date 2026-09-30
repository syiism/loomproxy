<template>
  <div>
    <PageHeader title="卡密管理" subtitle="批量生成套餐兑换卡密。完整明文仅在生成时展示一次，请立即保存。" />

    <!-- 生成表单 -->
    <div class="card reveal mb-6 max-w-2xl">
      <div class="grid grid-cols-2 md:grid-cols-4 gap-3 mb-4">
        <UiField label="套餐">
          <select v-model="genForm.plan_id" class="input">
            <option v-for="p in plans" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </UiField>
        <UiField label="有效天数（0=永久）">
          <input v-model.number="genForm.duration_days" type="number" min="0" class="input font-mono">
        </UiField>
        <UiField label="数量">
          <input v-model.number="genForm.count" type="number" min="1" max="100" class="input font-mono">
        </UiField>
        <UiField label="备注（可选）">
          <input v-model="genForm.note" class="input" placeholder="批次备注">
        </UiField>
      </div>
      <button class="btn-primary" :disabled="generating" @click="onGenerate">{{ generating ? '生成中' : '生成卡密' }}</button>
    </div>

    <!-- 生成结果（仅此一次展示明文） -->
    <UiModal :open="resultOpen" title="生成成功 — 请立即保存卡密" wide @close="resultOpen = false" @confirm="resultOpen = false">
      <div class="text-xs text-text-muted mb-3">批次 {{ resultBatch }} · 共 {{ resultCodes.length }} 张。完整明文仅此一次展示。</div>
      <div class="max-h-72 overflow-y-auto font-mono text-xs leading-6 select-all bg-surface border border-border rounded-lg p-4 whitespace-pre-line">{{ resultCodes.join('\n') }}</div>
      <div class="mt-4 flex gap-3">
        <button class="btn-ghost btn-sm" @click="copyCodes">复制全部</button>
        <button class="btn-ghost btn-sm" @click="downloadCodes">导出 TXT</button>
      </div>
    </UiModal>

    <!-- 筛选 -->
    <div class="reveal flex flex-col sm:flex-row gap-3 mb-6">
      <input v-model="filters.batchNo" placeholder="批次号" class="input sm:w-52 font-mono" @keydown.enter="loadList(1)">
      <select v-model="filters.status" class="input sm:w-36">
        <option value="">全部状态</option>
        <option value="1">未使用</option>
        <option value="2">已使用</option>
        <option value="0">已作废</option>
      </select>
      <button @click="loadList(1)" class="btn-ghost whitespace-nowrap">筛选</button>
      <button v-if="selected.size > 0" @click="onRevokeSelected" class="btn-ghost whitespace-nowrap" :style="selectedArmed ? 'color:#9F2F2D' : ''">
        {{ selectedArmed ? '确认作废？' : '批量作废（' + selected.size + '）' }}
      </button>
      <button v-if="filters.batchNo" @click="onRevokeBatch" class="btn-ghost whitespace-nowrap" :style="batchArmed ? 'color:#9F2F2D' : ''">
        {{ batchArmed ? '确认整批作废？' : '整批作废' }}
      </button>
    </div>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <UiEmpty v-else-if="list.length === 0" title="暂无卡密" text="使用上方表单生成第一批卡密。" />
    <template v-else>
      <div class="table-wrap reveal overflow-x-auto">
        <table class="table-base">
          <thead>
            <tr><th><input type="checkbox" :checked="allUnusedSelected" @change="toggleSelectAll"></th><th>ID</th><th>卡密</th><th>套餐</th><th>天数</th><th>状态</th><th>批次</th><th>兑换用户</th><th>创建时间</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="r in list" :key="r.id">
              <td>
                <input v-if="r.status === 1" type="checkbox" :checked="selected.has(r.id)" @change="toggleSelect(r.id)">
              </td>
              <td class="font-mono text-xs">{{ r.id }}</td>
              <td class="font-mono text-xs">{{ r.code }}</td>
              <td class="text-xs">{{ r.plan_name }}</td>
              <td class="font-mono text-xs">{{ r.duration_days === 0 ? '永久' : r.duration_days }}</td>
              <td><UiTag :tone="statusTone(r.status)" :label="statusLabel(r.status)" /></td>
              <td class="font-mono text-xs text-text-muted">{{ r.batch_no }}</td>
              <td class="text-xs">{{ r.used_by_name || '—' }}</td>
              <td class="font-mono text-xs text-text-muted">{{ fmtDate(r.created_at) }}</td>
              <td class="whitespace-nowrap">
                <button class="text-xs text-text-muted hover:text-text transition-colors" :class="r.status === 1 ? 'mr-3' : ''" @click="copyCode(r.code)">复制</button>
                <button v-if="r.status === 1" class="text-xs text-pale-red-fg hover:opacity-70" @click="onRevoke(r)">作废</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <UiPagination :page="page" :total="total" :page-size="pageSize" @change="loadList" />
    </template>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, nextTick } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiField from '../../components/UiField.vue'
import UiTag from '../../components/UiTag.vue'
import UiModal from '../../components/UiModal.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import UiPagination from '../../components/UiPagination.vue'
import { adminApi } from '../../api/index.js'
import { fmtDate, toast, revealObserve } from '../../utils.js'

const plans = ref([])
const genForm = ref({ plan_id: null, duration_days: 30, count: 10, note: '' })
const generating = ref(false)
const resultOpen = ref(false)
const resultCodes = ref([])
const resultBatch = ref('')
const loading = ref(true)
const error = ref('')
const list = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const filters = ref({ batchNo: '', status: '' })
const batchArmed = ref(false)
const selected = ref(new Set())
const selectedArmed = ref(false)
let batchTimer = null
let selectedTimer = null

// 仅未使用的卡密可勾选；全选仅覆盖当前页的未使用卡密
const allUnusedSelected = computed(() => {
  const unused = list.value.filter(r => r.status === 1)
  return unused.length > 0 && unused.every(r => selected.value.has(r.id))
})

const toggleSelect = (id) => {
  const s = new Set(selected.value)
  s.has(id) ? s.delete(id) : s.add(id)
  selected.value = s
}

const toggleSelectAll = () => {
  if (allUnusedSelected.value) {
    selected.value = new Set()
  } else {
    selected.value = new Set(list.value.filter(r => r.status === 1).map(r => r.id))
  }
}

const onRevokeSelected = async () => {
  if (!selectedArmed.value) {
    selectedArmed.value = true
    clearTimeout(selectedTimer)
    selectedTimer = setTimeout(() => { selectedArmed.value = false }, 3000)
    return
  }
  selectedArmed.value = false
  try {
    const data = await adminApi.revokeRedeemCodes([...selected.value])
    toast(data.message || '已批量作废', 'success')
    selected.value = new Set()
    loadList(page.value)
  } catch (e) { toast(e.message, 'error') }
}

const statusTone = (s) => s === 1 ? 'green' : s === 2 ? 'blue' : 'gray'
const statusLabel = (s) => s === 1 ? '未使用' : s === 2 ? '已使用' : '已作废'

const loadPlans = async () => {
  try {
    plans.value = (await adminApi.listPlans()) || []
    if (plans.value.length && !genForm.value.plan_id) genForm.value.plan_id = plans.value[0].id
  } catch (e) { /* 忽略 */ }
}

const onGenerate = async () => {
  if (generating.value) return
  generating.value = true
  try {
    const data = await adminApi.createRedeemCodes({
      plan_id: genForm.value.plan_id,
      duration_days: genForm.value.duration_days,
      count: genForm.value.count,
      note: genForm.value.note,
    })
    resultCodes.value = data.codes || []
    resultBatch.value = data.batch_no
    resultOpen.value = true
    loadList(1)
  } catch (e) { toast(e.message, 'error') } finally { generating.value = false }
}

const copyCode = async (code) => {
  try {
    await navigator.clipboard.writeText(code)
    toast('卡密已复制', 'success')
  } catch (e) { toast('复制失败，请手动选择复制', 'error') }
}

const copyCodes = async () => {
  try {
    await navigator.clipboard.writeText(resultCodes.value.join('\n'))
    toast('已复制', 'success')
  } catch (e) { toast('复制失败，请手动选择复制', 'error') }
}

const downloadCodes = () => {
  const blob = new Blob([resultCodes.value.join('\n')], { type: 'text/plain' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = 'redeem-codes-' + resultBatch.value + '.txt'
  a.click()
  URL.revokeObjectURL(a.href)
}

const loadList = async (p) => {
  loading.value = true
  error.value = ''
  try {
    const data = await adminApi.listRedeemCodes({
      page: p, pageSize: pageSize.value,
      batchNo: filters.value.batchNo || undefined,
      status: filters.value.status || undefined,
    })
    list.value = data.list || []
    total.value = data.total || 0
    page.value = data.page || 1
    pageSize.value = data.page_size || 20
    selected.value = new Set() // 翻页/刷新后清空勾选
  } catch (e) { error.value = e.message }
  loading.value = false
  nextTick(revealObserve)
}

const onRevoke = async (r) => {
  try {
    await adminApi.revokeRedeemCode(r.id)
    toast('已作废', 'success')
    loadList(page.value)
  } catch (e) { toast(e.message, 'error') }
}

const onRevokeBatch = async () => {
  if (!batchArmed.value) {
    batchArmed.value = true
    clearTimeout(batchTimer)
    batchTimer = setTimeout(() => { batchArmed.value = false }, 3000)
    return
  }
  batchArmed.value = false
  try {
    const data = await adminApi.revokeRedeemBatch(filters.value.batchNo)
    toast(data.message || '已整批作废', 'success')
    loadList(1)
  } catch (e) { toast(e.message, 'error') }
}

onMounted(() => { loadPlans(); loadList(1); revealObserve() })
</script>
