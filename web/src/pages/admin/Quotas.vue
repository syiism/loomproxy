<template>
  <div>
    <PageHeader title="额度套餐" subtitle="套餐、各维度调用限制与可访问数据源。限额 -1 表示不限。">
      <template #actions>
        <button class="btn-primary" @click="openCreatePlan">新建套餐</button>
      </template>
    </PageHeader>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <UiEmpty v-else-if="plans.length === 0" title="暂无套餐" text="点击右上角新建第一个套餐。" />
    <div v-else class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 md:gap-6">
      <div v-for="p in plans" :key="p.id" class="card card-hover reveal flex flex-col">
        <div class="flex items-start justify-between mb-3">
          <div class="font-mono text-xs uppercase tracking-wider text-text-muted">{{ p.code }}</div>
          <div class="flex items-center gap-2">
            <UiTag tone="blue" :label="'Lv.' + (p.level ?? 0)" />
            <UiTag v-if="p.role" :tone="roleTone(p.role.code)" :label="p.role.name || p.role.code" />
            <UiTag :tone="statusTone(p.status)" :label="p.status === 1 ? '启用' : '禁用'" />
          </div>
        </div>
        <div class="font-serif text-xl font-medium tracking-tight mb-1">{{ p.name }}</div>
        <div class="text-sm text-text-muted mb-4">{{ p.description || '—' }}</div>

        <div class="flex-1">
          <div v-if="p.limits && p.limits.length" class="space-y-2">
            <div v-for="l in p.limits" :key="l.id" class="flex justify-between items-center text-sm">
              <span class="font-mono text-xs text-text-muted">{{ l.scope }} / {{ l.target }}</span>
              <span class="font-mono text-xs">{{ l.limit < 0 ? '不限' : l.limit }} / {{ l.period }}</span>
            </div>
          </div>
          <div v-else class="text-center py-3 text-text-muted text-sm">无限制项</div>
          <div class="flex items-center justify-between text-sm text-text-muted mt-4 pt-3 border-t border-border">
            <span>可用数据源</span>
            <span class="font-mono text-xs">{{ p.ds_count ?? '—' }} 个</span>
          </div>
        </div>

        <div class="flex gap-2 mt-5 pt-4 border-t border-border">
          <button class="btn-ghost btn-sm flex-1" @click="openDsPerms(p)">数据源</button>
          <button class="btn-ghost btn-sm flex-1" @click="openLimits(p)">限制项</button>
          <button class="btn-ghost btn-sm flex-1" @click="openEditPlan(p)">编辑</button>
          <button class="btn-danger btn-sm flex-1" @click="deletePlanTarget = p">删除</button>
        </div>
      </div>
    </div>

    <!-- 新建/编辑套餐 -->
    <UiModal :open="planModalOpen" :title="editingPlan ? '编辑套餐 #' + editingPlan.id : '新建套餐'" @close="planModalOpen = false" @confirm="savePlan">
      <form @submit.prevent="savePlan" class="space-y-5">
        <UiField label="代码" hint="创建后不可修改，如 free / vip">
          <input v-model="planForm.code" class="input font-mono" :disabled="!!editingPlan" maxlength="32" required>
        </UiField>
        <UiField label="名称">
          <input v-model="planForm.name" class="input" maxlength="64" required>
        </UiField>
        <UiField label="说明">
          <input v-model="planForm.description" class="input" maxlength="255">
        </UiField>
        <UiField label="等级" hint="用于卡密兑换时的升级/降级判定：0=免费，数值越大等级越高">
          <input v-model.number="planForm.level" type="number" min="0" class="input font-mono">
        </UiField>
        <UiField label="绑定角色" hint="用户套餐变更时自动切换为该角色；不绑定则不影响用户角色">
          <select v-model.number="planForm.role_id" class="input">
            <option :value="0">不绑定</option>
            <option v-for="r in allRoles" :key="r.id" :value="r.id">
              {{ r.name }}（{{ r.code }}）{{ r.status !== 1 ? ' — 已禁用' : '' }}
            </option>
          </select>
        </UiField>
        <UiField v-if="editingPlan" label="状态">
          <select v-model="planForm.status" class="input">
            <option :value="1">启用</option>
            <option :value="0">禁用</option>
          </select>
        </UiField>
      </form>
    </UiModal>

    <!-- 删除套餐确认 -->
    <UiModal :open="!!deletePlanTarget" :title="'删除套餐 #' + (deletePlanTarget && deletePlanTarget.id)" @close="deletePlanTarget = null" @confirm="confirmDeletePlan">
      <p class="text-text-muted text-sm">删除套餐 <span class="font-medium text-text">{{ deletePlanTarget && deletePlanTarget.name }}</span> 将同时删除其全部限制项；仍被用户引用的套餐无法删除。确定继续？</p>
    </UiModal>

    <!-- 数据源权限管理 -->
    <UiModal :open="dsOpen" :title="'数据源权限 — ' + (dsPlan && dsPlan.name)" wide @close="dsOpen = false" @confirm="saveDsPerms" :confirm-loading="dsSaving" confirm-text="保存">
      <UiSpinner v-if="dsLoading" />
      <div v-else class="space-y-3">
        <p class="text-text-muted text-sm">勾选套餐 <span class="text-text">{{ dsPlan && dsPlan.name }}</span> 可访问的数据源：</p>
        <div class="max-h-72 overflow-y-auto border border-border rounded-lg p-3 grid grid-cols-1 sm:grid-cols-2 gap-1.5">
          <label v-for="ds in allDataSources" :key="ds.id" class="flex items-center gap-2 text-sm cursor-pointer rounded px-1 py-0.5 hover:bg-surface-alt transition-colors">
            <input type="checkbox" :value="ds.id" v-model="dsSelected" class="checkbox">
            <span class="font-mono">{{ ds.display_name }}</span>
            <span class="text-text-muted text-xs font-mono">({{ ds.name }})</span>
            <UiTag v-if="ds.status !== 1" tone="gray" label="已禁用" />
          </label>
          <div v-if="allDataSources.length === 0" class="text-text-muted text-sm text-center py-4 col-span-full">暂无数据源</div>
        </div>
        <div class="text-sm text-text-muted">已选 {{ dsSelected.length }} / {{ allDataSources.length }} 项</div>
      </div>
    </UiModal>

    <!-- 限制项管理 -->
    <UiModal :open="!!limitsPlan" :title="'限制项 — ' + (limitsPlan && limitsPlan.name)" wide @close="limitsPlan = null" @confirm="limitsPlan = null">
      <p class="text-xs text-text-muted mb-3">这里改的是<b>套餐轴</b>的限额；单个用户在其之上的增减（追加语义，优先级最高）在
        <router-link to="/admin/users" class="text-text hover:underline">用户页的「额度」</router-link> 里改。两张表、一个生效链，别在两处找同一格。</p>
      <div v-if="limitsPlan">
        <div v-if="limits.length === 0" class="text-center py-6 text-text-muted text-sm border border-dashed border-border rounded-lg mb-5">暂无限制项</div>
        <div v-else class="border border-border rounded-lg overflow-hidden mb-5">
          <table class="table-base">
            <thead>
              <tr><th>维度</th><th>目标</th><th>限额</th><th>周期</th><th>操作</th></tr>
            </thead>
            <tbody>
              <tr v-for="l in limits" :key="l.id">
                <td class="font-mono text-xs">{{ l.scope }}</td>
                <td class="font-mono text-xs">{{ l.target }}</td>
                <td>
                  <input v-if="editingLimit === l.id" v-model.number="limitEditForm.limit" type="number" class="input input-sm w-24 font-mono">
                  <span v-else class="font-mono text-xs">{{ l.limit < 0 ? '不限' : l.limit }}</span>
                </td>
                <td>
                  <select v-if="editingLimit === l.id" v-model="limitEditForm.period" class="input input-sm w-24">
                    <option value="day">day</option>
                    <option value="week">week</option>
                    <option value="month">month</option>
                  </select>
                  <span v-else class="font-mono text-xs">{{ l.period }}</span>
                </td>
                <td>
                  <div class="flex gap-3">
                    <template v-if="editingLimit === l.id">
                      <button class="text-xs text-pale-green-fg hover:opacity-70" @click="saveLimit(l)">保存</button>
                      <button class="text-xs text-text-muted hover:text-text" @click="editingLimit = null">取消</button>
                    </template>
                    <template v-else>
                      <button class="text-xs text-text-muted hover:text-text" @click="startEditLimit(l)">编辑</button>
                      <button class="text-xs text-pale-red-fg hover:opacity-70" @click="removeLimit(l)">删除</button>
                    </template>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">新增限制项</div>
        <form @submit.prevent="addLimit" class="space-y-4">
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <UiField label="维度" hint="global 全局 / source 数据源">
              <select v-model="limitForm.scope" class="input">
                <option value="global">global</option>
                <option value="source">source</option>
              </select>
            </UiField>
            <UiField label="目标" hint="如 api 或数据源码（如 novel_a）">
              <input v-model="limitForm.target" class="input font-mono" placeholder="api / novel_a" required>
            </UiField>
          </div>
          <div class="grid grid-cols-2 sm:grid-cols-3 gap-4 items-end">
            <UiField label="限额" label-hint="-1 表示不限">
              <input v-model.number="limitForm.limit" type="number" class="input font-mono" required>
            </UiField>
            <UiField label="周期">
              <select v-model="limitForm.period" class="input">
                <option value="day">day</option>
                <option value="week">week</option>
                <option value="month">month</option>
              </select>
            </UiField>
            <button type="submit" class="btn-primary col-span-2 sm:col-span-1" :disabled="submitting">添加</button>
          </div>
        </form>
      </div>
    </UiModal>
  </div>
</template>

<script setup>
import { ref, onMounted, nextTick } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiModal from '../../components/UiModal.vue'
import UiTag from '../../components/UiTag.vue'
import UiField from '../../components/UiField.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import { adminApi } from '../../api/index.js'
import { statusTone, roleTone, toast, revealObserve } from '../../utils.js'

const loading = ref(true)
const error = ref('')
const plans = ref([])
const submitting = ref(false)
const allRoles = ref([])
const planModalOpen = ref(false)
const editingPlan = ref(null)
const planForm = ref({ code: '', name: '', description: '', status: 1, level: 0, role_id: 0 })
const deletePlanTarget = ref(null)
const limitsPlan = ref(null)
const limits = ref([])
const limitForm = ref({ scope: 'source', target: '', limit: 100, period: 'day' })
const editingLimit = ref(null)
const limitEditForm = ref({ limit: 0, period: 'day' })
// 数据源权限
const allDataSources = ref([])
const dsOpen = ref(false)
const dsPlan = ref(null)
const dsSelected = ref([])
const dsCurrentIds = ref([])
const dsLoading = ref(false)
const dsSaving = ref(false)

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    const plansData = (await adminApi.listPlans()) || []
    plans.value = await Promise.all(plansData.map(async p => ({
      ...p,
      limits: await adminApi.listLimits(p.id).catch(() => []),
      ds_count: await adminApi.listPlanDataSources(p.id).then(r => (r.data_sources || []).length).catch(() => null),
    })))
  } catch (e) {
    error.value = e.message
  }
  loading.value = false
  nextTick(revealObserve)
}

const openCreatePlan = async () => {
  if (!allRoles.value.length) {
    try { allRoles.value = (await adminApi.listRoles()) || [] } catch (e) { toast(e.message, 'error') }
  }
  editingPlan.value = null
  planForm.value = { code: '', name: '', description: '', status: 1, level: 0, role_id: 0 }
  planModalOpen.value = true
}

const openEditPlan = async (p) => {
  if (!allRoles.value.length) {
    try { allRoles.value = (await adminApi.listRoles()) || [] } catch (e) { toast(e.message, 'error') }
  }
  editingPlan.value = p
  planForm.value = { code: p.code, name: p.name, description: p.description || '', status: p.status, level: p.level ?? 0, role_id: p.role_id || 0 }
  planModalOpen.value = true
}

const savePlan = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    const roleId = planForm.value.role_id || 0
    if (editingPlan.value) {
      await adminApi.updatePlan(editingPlan.value.id, {
        name: planForm.value.name.trim(),
        description: planForm.value.description.trim(),
        status: planForm.value.status,
        level: planForm.value.level ?? 0,
        role_id: roleId,
      })
      toast('套餐已更新', 'success')
    } else {
      await adminApi.createPlan({
        code: planForm.value.code.trim(),
        name: planForm.value.name.trim(),
        description: planForm.value.description.trim(),
        level: planForm.value.level ?? 0,
        role_id: roleId,
      })
      toast('套餐已创建', 'success')
    }
    planModalOpen.value = false
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const confirmDeletePlan = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.deletePlan(deletePlanTarget.value.id)
    toast('套餐已删除', 'success')
    deletePlanTarget.value = null
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const openDsPerms = async (p) => {
  dsPlan.value = p
  dsOpen.value = true
  dsLoading.value = true
  try {
    if (!allDataSources.value.length) allDataSources.value = (await adminApi.listDataSources()) || []
    const res = await adminApi.listPlanDataSources(p.id)
    dsCurrentIds.value = (res.data_sources || []).map(d => d.id)
    dsSelected.value = [...dsCurrentIds.value]
  } catch (e) { toast(e.message, 'error') }
  dsLoading.value = false
}

const saveDsPerms = async () => {
  if (dsSaving.value || !dsPlan.value) return
  dsSaving.value = true
  try {
    const planId = dsPlan.value.id
    const toAdd = dsSelected.value.filter(id => !dsCurrentIds.value.includes(id))
    const toRemove = dsCurrentIds.value.filter(id => !dsSelected.value.includes(id))
    await Promise.all([
      toAdd.length ? adminApi.batchAddPlanDataSources(planId, toAdd) : null,
      ...toRemove.map(id => adminApi.removePlanDataSource(planId, id)),
    ].filter(Boolean))
    toast('数据源权限已保存', 'success')
    dsOpen.value = false
    dsPlan.value = null
    load()
  } catch (e) { toast(e.message, 'error') } finally { dsSaving.value = false }
}

const openLimits = async (p) => {
  limitsPlan.value = p
  limitForm.value = { scope: 'source', target: '', limit: 100, period: 'day' }
  editingLimit.value = null
  await loadLimits()
}

const loadLimits = async () => {
  try {
    limits.value = (await adminApi.listLimits(limitsPlan.value.id)) || []
  } catch (e) { toast(e.message, 'error') }
}

const addLimit = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.createLimit({
      plan_id: limitsPlan.value.id,
      scope: limitForm.value.scope,
      target: limitForm.value.target.trim(),
      limit: limitForm.value.limit,
      period: limitForm.value.period,
    })
    toast('限制项已添加', 'success')
    limitForm.value.target = ''
    await loadLimits()
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const startEditLimit = (l) => {
  editingLimit.value = l.id
  limitEditForm.value = { limit: l.limit, period: l.period }
}

const saveLimit = async (l) => {
  try {
    await adminApi.updateLimit(l.id, { limit: limitEditForm.value.limit, period: limitEditForm.value.period })
    toast('已更新', 'success')
    editingLimit.value = null
    await loadLimits()
    load()
  } catch (e) { toast(e.message, 'error') }
}

const removeLimit = async (l) => {
  try {
    await adminApi.deleteLimit(l.id)
    toast('已删除', 'success')
    await loadLimits()
    load()
  } catch (e) { toast(e.message, 'error') }
}

onMounted(() => { revealObserve() })
load()
</script>
