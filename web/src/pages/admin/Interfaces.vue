<template>
  <div>
    <PageHeader title="接口管理" subtitle="配置接口消耗和速率限制。默认值由「默认」Tab 编辑，每个套餐可通过「按套餐」Tab 单独覆盖。限流口径：窗口计数（如 30 次/60 秒，允许突发），填 0 为不限。库里另有「固定间隔（每 N 秒 1 次，不可突发）」这一档仍存在且优先度低于窗口计数，但面板不再提供输入框（待办清单 P119 ③），已有的值会继续生效并照常显示。" />

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <div v-else class="space-y-6">
      <!-- 视图切换 -->
      <div class="flex items-center gap-3 flex-wrap">
        <input v-model="dsKeyword" class="input w-48 font-mono text-sm" placeholder="搜索数据源标识名/中文名">
        <div class="flex items-center gap-1 bg-surface border border-border rounded-lg p-0.5">
          <button
            v-for="v in viewModes"
            :key="v.key"
            @click="viewMode = v.key"
            :class="viewMode === v.key ? 'bg-surface-alt border border-border shadow-sm' : 'text-text-muted hover:text-text'"
            class="btn-ghost btn-sm px-3 rounded"
          >{{ v.label }}</button>
        </div>
        <select v-if="viewMode === 'plan'" v-model.number="selectedPlan" class="input">
          <option :value="0">默认（全局）</option>
          <option v-for="p in plans" :key="p.id" :value="p.id">{{ p.name }}</option>
        </select>
        <button v-if="viewMode === 'plan' && selectedPlan > 0" class="btn-ghost btn-sm" @click="resetPlanCosts">重置本套餐</button>
      </div>

      <div v-for="ds in filteredDataSources" :key="ds.id" class="card reveal !p-0 overflow-hidden">
        <div class="flex flex-wrap items-center justify-between gap-3 px-5 md:px-6 py-4 border-b border-border bg-surface-alt">
          <div>
            <div class="font-serif text-lg md:text-xl font-medium tracking-tight">{{ ds.name }}</div>
            <div class="flex items-center gap-2 font-mono text-xs text-text-muted mt-0.5">
              <span class="px-2 py-0.5 bg-surface border border-border rounded">{{ ds.name }}</span>
              <span class="px-2 py-0.5 bg-surface border border-border rounded">{{ ds.source_code }}</span>
            </div>
          </div>
          <div v-if="ds.interfaces.length > 0" class="flex items-center gap-3">
            <button class="btn-ghost btn-sm" @click="selectAll(ds, true)">全选</button>
            <button class="btn-ghost btn-sm" @click="selectAll(ds, false)">取消全选</button>
            <div v-if="selectedCount(ds) > 0" class="flex items-center gap-2 text-sm text-text-muted">
              <span>已选 {{ selectedCount(ds) }} 项</span>
              <button class="btn-ghost btn-sm" @click="batchEditCost(ds)">批量设置消耗</button>
              <button class="btn-ghost btn-sm" @click="batchToggleStatus(ds, 1)">批量启用</button>
              <button class="btn-ghost btn-sm" @click="batchToggleStatus(ds, 0)">批量禁用</button>
            </div>
          </div>
        </div>

        <div class="overflow-x-auto">
          <table class="table-base min-w-[760px]">
            <colgroup>
              <col class="w-10">
              <col>
              <col>
              <col>
              <col>
              <col>
            </colgroup>
            <thead>
              <tr>
                <th class="w-10 px-3 md:px-5"><input type="checkbox" :checked="allSelected(ds)" @change="selectAll(ds, $event.target.checked)" /></th>
                <th class="px-3 md:px-5">接口</th>
                <th class="px-3 md:px-5">单次消耗</th>
                <th class="px-3 md:px-5">速率限制</th>
                <th class="px-3 md:px-5">状态</th>
                <th class="px-3 md:px-5">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="iface in ds.interfaces" :key="iface.key" :class="{ 'bg-surface-alt': selected.get(ds.id)?.has(iface.id) }">
                <td class="w-10"><input type="checkbox" :checked="selected.get(ds.id)?.has(iface.id)" @change="toggleSelect(ds, iface, $event.target.checked)" /></td>
                <td class="font-mono text-xs">{{ iface.name }}</td>
                <td>
                  <input v-if="editing === iface.key" v-model.number="iface.editCost" type="number" min="0" class="input input-sm w-20 font-mono">
                  <span v-else class="font-mono text-xs">{{ iface.cost }}</span>
                </td>
                <td>
                  <div v-if="editing === iface.key" class="flex items-center gap-1 flex-wrap">
                    <input v-model.number="iface.editLimitCount" type="number" min="0" class="input input-sm w-14 font-mono" title="窗口内次数上限（0=不限）">
                    <span class="text-xs text-text-muted">次/</span>
                    <input v-model.number="iface.editWindowSec" type="number" min="1" class="input input-sm w-14 font-mono" title="窗口长度（秒）">
                    <span class="text-xs text-text-muted">秒</span>
                    <!-- 「固定间隔（每 N 秒 1 次）」的输入框按待办清单 P119 ③ 从面板去掉（A 档）：
                         后端字段 quota_costs.interval / quota_costs_plans.interval 与 specFrom 的判定**都原样保留**，
                         所以库里已有的间隔配置继续生效、formatLimit 也继续如实显示"每 N 秒 1 次"。
                         代价要写明：**面板从此清不掉一个已存在的 interval 值**（要清只能改库）。
                         这不是缺陷而是这一档的定义——B 档（连判定一起摘）等于放宽限流，是对外可见的变更，没拍。-->
                  </div>
                  <span v-else class="font-mono text-xs">{{ formatLimit(iface) }}</span>
                </td>
                <td>
                  <select v-if="editing === iface.key" v-model.number="iface.editStatus" class="input input-sm w-24">
                    <option :value="1">启用</option>
                    <option :value="0">禁用</option>
                  </select>
                  <UiTag v-else :tone="iface.enabled ? 'green' : 'gray'" :label="iface.enabled ? '启用' : '禁用'" />
                </td>
                <td>
                  <template v-if="editing === iface.key">
                    <button @click="saveIface(iface)" class="text-xs text-pale-green-fg hover:opacity-70 transition-opacity mr-3">保存</button>
                    <button @click="editing = null" class="text-xs text-text-muted hover:text-text transition-colors">取消</button>
                  </template>
                  <button v-else @click="startEdit(iface)" class="text-xs text-text-muted hover:text-text transition-colors">编辑</button>
                </td>
              </tr>
              <tr v-if="ds.interfaces.length === 0">
                <td colspan="6" class="text-center text-text-muted py-8">暂无接口配置</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <UiEmpty v-if="dataSources.length > 0 && filteredDataSources.length === 0" title="未匹配的数据源" text="尝试更换搜索关键词" />
    </div>

    <!-- 批量设置消耗确认 -->
    <UiModal :open="batchCostOpen" title="批量设置消耗" @close="batchCostOpen = false" @confirm="confirmBatchCost" confirm-text="设置" :confirm-loading="batchSubmitting">
      <p class="text-text-muted text-sm mb-4">将 <span class="font-mono">{{ dsName }}</span> 的 {{ batchCostCount }} 个接口消耗设置为：</p>
      <input v-model="batchCostInput" type="number" min="0" class="input font-mono w-32" placeholder="消耗值" @input="onBatchCostInput">
    </UiModal>
  </div>
</template>

<script setup>
import { ref, onMounted, nextTick, watch } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiTag from '../../components/UiTag.vue'
import UiModal from '../../components/UiModal.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import { adminApi, quotaApi } from '../../api/index.js'
import { toast, revealObserve } from '../../utils.js'

const loading = ref(true)
const error = ref('')
const dataSources = ref([])
const plans = ref([])
const editing = ref(null)
const selected = ref(new Map())
const batchCostOpen = ref(false)
const batchCostCount = ref(0)
const batchCostInput = ref('')
const batchSubmitting = ref(false)
const batchTarget = ref(null)

const viewModes = [
  { key: 'default', label: '默认' },
  { key: 'plan', label: '按套餐' },
]
const viewMode = ref('default')
const selectedPlan = ref(0)
const planCostsMap = ref({}) // { interface_name: { id, interval } }

const dsName = ref('')
const dsKeyword = ref('')
const filteredDataSources = ref([])

// 窗口长度缺省值（与后端 quota.defaultWindowSec 一致）
const DEFAULT_WINDOW_SEC = 60

// pickLimit 取一组配置（全局接口行 / 套餐覆盖行）中的有效限流：窗口计数优先于固定间隔
const pickLimit = (src) => {
  if (!src) return null
  const count = src.limit_count ?? 0
  if (count > 0) {
    const win = src.window_sec ?? 0
    return { mode: 'window', count, window: win > 0 ? win : DEFAULT_WINDOW_SEC, interval: 0 }
  }
  const interval = src.interval ?? 0
  if (interval > 0) return { mode: 'interval', count: 0, window: DEFAULT_WINDOW_SEC, interval }
  return null
}

// resolveLimit 解析生效限流（与后端 rateLimitMiddleware 一致）：
// 套餐级配置优先（套餐内窗口 > 间隔），套餐级无有效限流时回退全局
const resolveLimit = (globalIf, pc) =>
  pickLimit(pc) || pickLimit(globalIf) || { mode: 'none', count: 0, window: DEFAULT_WINDOW_SEC, interval: 0 }

const formatLimit = (iface) => {
  if ((iface.limitCount ?? 0) > 0) return `${iface.limitCount} 次 / ${iface.windowSec || DEFAULT_WINDOW_SEC} 秒`
  if ((iface.interval ?? 0) > 0) return `每 ${iface.interval} 秒 1 次`
  return '不限'
}

const updateFilter = () => {
  const kw = dsKeyword.value.trim().toLowerCase()
  if (!kw) {
    filteredDataSources.value = [...dataSources.value]
  } else {
    filteredDataSources.value = dataSources.value.filter(ds => {
      const sourceCode = (ds.source_code || '').toLowerCase()
      const name = (ds.name || '').toLowerCase()
      const id = String(ds.id).toLowerCase()
      return sourceCode.includes(kw) || name.includes(kw) || id.includes(kw)
    })
  }
}

const loadPlanName = () => {
  const p = plans.value.find(x => x.id === selectedPlan.value)
  dsName.value = p ? p.name : '全局'
}

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    const [dashboard, planCosts, planListResp] = await Promise.all([
      quotaApi.dashboard(),
      adminApi.listPlanQuotaCosts(),
      adminApi.listPlans(),
    ])
    plans.value = planListResp || []

    // 构建 planCost 查找 Map: { interface_name: { id, interval, group_code } }
    planCostsMap.value = {}
    if (selectedPlan.value > 0) {
      for (const pc of (planCosts || [])) {
        if (pc.plan_id === selectedPlan.value) {
          const key = `${pc.group_code}:${pc.interface}`
          planCostsMap.value[key] = pc
        }
      }
    }

    dataSources.value = (dashboard.sources || []).map(s => ({
      ...s,
      interfaces: (s.interfaces || []).map(i => {
        const pc = selectedPlan.value > 0 ? planCostsMap.value[`${s.source_code}:${i.name}`] : null
        const limit = resolveLimit(i, pc)

        return {
          ...i,
          key: `${s.source_code}:${i.name}`,
          name: i.name,
          source_code: s.source_code,
          globalInterval: i.interval ?? 0,
          globalLimitCount: i.limit_count ?? 0,
          globalWindowSec: i.window_sec ?? 0,
          editCost: i.cost,
          editInterval: limit.interval,
          editLimitCount: limit.count,
          editWindowSec: limit.window,
          editStatus: i.enabled ? 1 : 0,
          interval: limit.interval,
          limitCount: limit.count,
          windowSec: limit.window,
        }
      })
    }))
    selected.value.clear()
    updateFilter()
  } catch (e) {
    error.value = e.message
  }
  loading.value = false
  nextTick(revealObserve)
}

// 按当前套餐刷新各接口的生效限流：套餐覆盖有效时用套餐值，否则回退全局基线
const applyPlanLimits = () => {
  for (const ds of dataSources.value) {
    for (const iface of ds.interfaces) {
      const pc = selectedPlan.value > 0 ? planCostsMap.value[`${ds.source_code}:${iface.name}`] : null
      const limit = resolveLimit(
        {
          interval: iface.globalInterval,
          limit_count: iface.globalLimitCount,
          window_sec: iface.globalWindowSec,
        },
        pc,
      )
      iface.interval = limit.interval
      iface.limitCount = limit.count
      iface.windowSec = limit.window
      iface.editInterval = limit.interval
      iface.editLimitCount = limit.count
      iface.editWindowSec = limit.window
    }
  }
}

// 切换套餐时重新加载 planCosts
watch(selectedPlan, async (newVal) => {
  editing.value = null
  selected.value.clear()
  if (newVal > 0) {
    try {
      const planCosts = await adminApi.listPlanQuotaCosts()
      planCostsMap.value = {}
      for (const pc of (planCosts || [])) {
        if (pc.plan_id === newVal) {
          const key = `${pc.group_code}:${pc.interface}`
          planCostsMap.value[key] = pc
        }
      }
      applyPlanLimits()
    } catch (e) {
      toast('加载套餐配置失败: ' + e.message, 'error')
    }
  } else {
    planCostsMap.value = {}
    // 切回默认时，恢复全局基线值
    applyPlanLimits()
  }
})

watch(viewMode, () => {
  dsKeyword.value = ''
  if (viewMode.value === 'default') {
    selectedPlan.value = 0
  }
  editing.value = null
  selected.value.clear()
})

watch(dsKeyword, () => {
  updateFilter()
  // 过滤后重新出现的卡片是新建 DOM（keyed v-for 卸载后重建），不带 .in 类，
  // 必须重新触发 IntersectionObserver，否则 .reveal 保持 opacity: 0 不可见
  nextTick(revealObserve)
})

const selectedCount = (ds) => {
  return selected.value.get(ds.id)?.size || 0
}

const allSelected = (ds) => {
  return ds.interfaces.length > 0 && selected.value.get(ds.id)?.size === ds.interfaces.length
}

const toggleSelect = (ds, iface, checked) => {
  let sel = selected.value.get(ds.id)
  if (!sel) {
    sel = new Set()
    selected.value.set(ds.id, sel)
  }
  if (checked) {
    sel.add(iface.id)
  } else {
    sel.delete(iface.id)
  }
}

const selectAll = (ds, checked) => {
  if (checked) {
    selected.value.set(ds.id, new Set(ds.interfaces.map(i => i.id)))
  } else {
    selected.value.set(ds.id, new Set())
  }
}

const batchEditCost = (ds) => {
  const sel = selected.value.get(ds.id)
  if (!sel || sel.size === 0) {
    toast('请先选择要修改的接口', 'warning')
    return
  }
  batchCostInput.value = ''
  batchCostCount.value = sel.size
  batchTarget.value = { ds, ids: Array.from(sel) }
  loadPlanName()
  batchCostOpen.value = true
}

const onBatchCostInput = () => {
  const v = parseInt(batchCostInput.value, 10)
  if (isNaN(v) || v < 0) batchCostInput.value = ''
}

const confirmBatchCost = async () => {
  if (batchSubmitting.value) return
  if (!batchTarget.value) return
  const numCost = parseInt(batchCostInput.value, 10)
  if (isNaN(numCost) || numCost < 0) {
    toast('请输入有效的非负整数', 'error')
    return
  }
  batchSubmitting.value = true
  try {
    await batchUpdate(batchTarget.value.ds, batchTarget.value.ids, { cost: numCost })
    batchCostOpen.value = false
  } finally {
    batchSubmitting.value = false
    batchTarget.value = null
  }
}

const batchToggleStatus = (ds, status) => {
  const sel = selected.value.get(ds.id)
  if (!sel || sel.size === 0) {
    toast('请先选择要修改的接口', 'warning')
    return
  }
  batchUpdate(ds, Array.from(sel), { status })
}

const batchUpdate = async (ds, ifaceIds, payload) => {
  const toUpdate = ds.interfaces.filter(i => ifaceIds.includes(i.id))
  for (const iface of toUpdate) {
    try {
      // 消耗/状态为全局字段（无套餐级概念），批量更新一律写全局 QuotaCost
      await adminApi.updateQuotaCost(iface.id, {
        cost: payload.cost ?? iface.cost,
        status: payload.status ?? (iface.enabled ? 1 : 0),
      })
      if (payload.cost !== undefined) iface.cost = payload.cost
      if (payload.status !== undefined) iface.enabled = payload.status === 1
    } catch (e) {
      toast(`更新 ${iface.name} 失败: ${e.message}`, 'error')
      return
    }
  }
  selected.value.set(ds.id, new Set())
  toast(`批量更新 ${toUpdate.length} 项成功`, 'success')
}

const startEdit = (iface) => {
  iface.editCost = iface.cost
  iface.editInterval = iface.interval ?? 0
  iface.editLimitCount = iface.limitCount ?? 0
  iface.editWindowSec = iface.windowSec || DEFAULT_WINDOW_SEC
  iface.editStatus = iface.enabled ? 1 : 0
  editing.value = iface.key
}

// 编辑值 → 限流负载：窗口次数为 0 时窗口长度回落默认值（后端要求 window_sec > 0）
const limitPayload = (iface) => {
  const limitCount = Math.max(0, Math.trunc(iface.editLimitCount ?? 0))
  const editWindow = Math.trunc(iface.editWindowSec ?? 0)
  return {
    interval: Math.max(0, Math.trunc(iface.editInterval ?? 0)),
    limit_count: limitCount,
    window_sec: limitCount > 0 && editWindow > 0 ? editWindow : DEFAULT_WINDOW_SEC,
  }
}

const saveIface = async (iface) => {
  const limit = limitPayload(iface)
  try {
    if (viewMode.value === 'plan' && selectedPlan.value > 0) {
      // 套餐级：限流只写入 QuotaCostPlan（按套餐隔离，upsert 处理无覆盖行的情况），
      // 不触碰全局 QuotaCost 的限流字段；消耗/状态无套餐级概念，仍更新全局
      const pc = await adminApi.upsertPlanQuotaCost({
        plan_id: selectedPlan.value,
        group_code: iface.source_code,
        interface: iface.name,
        ...limit,
      })
      await adminApi.updateQuotaCost(iface.id, {
        cost: iface.editCost,
        status: iface.editStatus,
      })
      planCostsMap.value[`${iface.source_code}:${iface.name}`] = pc
      iface.cost = iface.editCost
      iface.enabled = iface.editStatus === 1
      applyPlanLimits()
      toast('已更新（仅当前套餐）', 'success')
    } else {
      // 默认模式：更新全局 QuotaCost
      await adminApi.updateQuotaCost(iface.id, {
        cost: iface.editCost,
        status: iface.editStatus,
        ...limit,
      })
      iface.cost = iface.editCost
      iface.enabled = iface.editStatus === 1
      iface.globalInterval = limit.interval
      iface.globalLimitCount = limit.limit_count
      iface.globalWindowSec = limit.window_sec
      applyPlanLimits()
      toast('已更新', 'success')
    }

    editing.value = null
  } catch (e) {
    toast(e.message, 'error')
  }
}

const resetPlanCosts = async () => {
  if (!confirm(`确定重置 ${dsName.value} 套餐的所有接口限流吗？重置后将回退为全局默认值。`)) return
  loading.value = true
  try {
    const planCosts = await adminApi.listPlanQuotaCosts()
    for (const pc of planCosts) {
      if (pc.plan_id === selectedPlan.value) {
        // 统一走 upsert：`PUT /quota-costs/plans/:id` 只能改已有行，重置时若某接口恰好没有套餐级行就会漏掉。
        // 面板只用一条端点（待办清单 P28·E1），PUT 仍留给脚本与部署用
        await adminApi.upsertPlanQuotaCost({ plan_id: pc.plan_id, group_code: pc.group_code, interface: pc.interface, interval: 0, limit_count: 0 })
        pc.interval = 0
        pc.limit_count = 0
      }
    }
    // 重建套餐级 Map 并按中间件语义回退显示（套餐无有效限流时回退全局基线）
    planCostsMap.value = {}
    for (const pc of planCosts) {
      if (pc.plan_id === selectedPlan.value) {
        planCostsMap.value[`${pc.group_code}:${pc.interface}`] = pc
      }
    }
    applyPlanLimits()
    toast('已重置，当前套餐回退为全局默认值', 'success')
  } catch (e) {
    toast(e.message, 'error')
  }
  loading.value = false
}

onMounted(() => { load() })
</script>
