<template>
  <div>
    <PageHeader title="额度套餐" subtitle="套餐与各维度调用限制。数据源的授权就是其中一行限制：加一行即授权，删一行即回收。">
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
              <span class="font-mono text-xs">{{ l.limit < 0 ? '不限' : l.limit }}<span class="text-text-muted"> / 日</span></span>
            </div>
          </div>
          <div v-else class="text-center py-3 text-text-muted text-sm">无限制项</div>
          <div class="flex items-center justify-between text-sm text-text-muted mt-4 pt-3 border-t border-border">
            <span>授权数据源</span>
            <span class="font-mono text-xs">{{ sourceLimitCount(p) }} 个</span>
          </div>
        </div>

        <div class="flex gap-2 mt-5 pt-4 border-t border-border">
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
      <p class="text-text-muted text-sm">删除套餐 <span class="font-medium text-text">{{ deletePlanTarget && deletePlanTarget.name }}</span> 将同时删除其全部限制项（也就回收它授权过的全部数据源）；仍被用户引用的套餐无法删除。确定继续？</p>
    </UiModal>

    <!-- 限制项管理 -->
    <UiModal :open="!!limitsPlan" :title="'限制项 — ' + (limitsPlan && limitsPlan.name)" wide @close="limitsPlan = null" @confirm="limitsPlan = null">
      <p class="text-xs text-text-muted mb-3">这里改的是<b>套餐轴</b>：维度为 <span class="font-mono">source</span> 的一行同时是这个源的<b>授权</b>和<b>限额</b>——
        加一行 = 该套餐可以用这个源，删一行 = 回收它（限额填 -1 就是「能用但不限」）。
        单个用户在其之上的增减（追加语义，优先级最高）在
        <router-link to="/admin/users" class="text-text hover:underline">用户页的「额度」</router-link> 里改。</p>
      <div v-if="limitsPlan">
        <div v-if="limits.length === 0" class="text-center py-6 text-text-muted text-sm border border-dashed border-border rounded-lg mb-5">
          暂无限制项——该套餐当前没有授权任何数据源，其用户对任何源都是 403。
        </div>
        <div v-else class="border border-border rounded-lg overflow-hidden mb-5">
          <!-- 「周期」不再是一列：P70② 之后额度只有当日一个窗口，而一列永远相同的值不是信息
               ——表格的形式在暗示"每行可能不同"，而它不可能（待办清单 P96③，2026-10-07 拍：并进一句说明） -->
          <div class="px-4 pt-3 text-xs text-text-muted font-mono">限额均按日（当日只有一个窗口，没有第二种口径）</div>
          <table class="table-base">
            <thead>
              <tr><th>维度</th><th>目标</th><th>限额</th><th>操作</th></tr>
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
                  <div class="flex gap-3">
                    <template v-if="editingLimit === l.id">
                      <button class="text-xs text-pale-green-fg hover:opacity-70" @click="saveLimit(l)">保存</button>
                      <button class="text-xs text-text-muted hover:text-text" @click="editingLimit = null">取消</button>
                    </template>
                    <template v-else>
                      <button class="text-xs text-text-muted hover:text-text" @click="startEditLimit(l)">编辑</button>
                      <button class="text-xs text-pale-red-fg hover:opacity-70" @click="deleteLimitTarget = l">删除</button>
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
              <select v-model="limitForm.scope" class="input" @change="limitForm.target = ''">
                <option value="global">global</option>
                <option value="source">source</option>
              </select>
            </UiField>
            <UiField v-if="limitForm.scope === 'source'" label="目标" :hint="targetHint">
              <select v-model="limitForm.target" class="input font-mono" required>
                <option value="" disabled>选择数据源</option>
                <option v-for="ds in grantableSources" :key="ds.id" :value="ds.name">
                  {{ ds.name }}（{{ ds.display_name }}）
                </option>
              </select>
            </UiField>
            <UiField v-else label="目标" hint="全局限额的目标名，如 api">
              <input v-model="limitForm.target" class="input font-mono" placeholder="api" maxlength="64" required>
            </UiField>
          </div>
          <div class="grid grid-cols-2 sm:grid-cols-2 gap-4 items-end">
            <!-- 「周期」这一格由 P96 收口时删掉：表单里它渲染的是常量，而 limitForm 根本没有 period 这一项，
                 后端也不收（P70②）——它不是"告诉你现在填的是按日的"，是一个填不动也没人读的输入框位。
                 口径那句话留在上方表格那一行（页面上只有一处说这件事）。 -->
            <UiField label="限额" label-hint="按日；-1 表示不限">
              <input v-model.number="limitForm.limit" type="number" class="input font-mono" required>
            </UiField>
            <button type="submit" class="btn-primary col-span-2 sm:col-span-1" :disabled="submitting">添加</button>
          </div>
        </form>
      </div>
    </UiModal>

    <!-- 删除限制项确认 -->
    <UiModal :open="!!deleteLimitTarget" :title="'删除限制项 #' + (deleteLimitTarget && deleteLimitTarget.id)" @close="deleteLimitTarget = null" @confirm="confirmDeleteLimit">
      <p class="text-text-muted text-sm">
        <template v-if="deleteLimitTarget && deleteLimitTarget.scope === 'source'">
          这一行就是套餐 <span class="font-medium text-text">{{ limitsPlan && limitsPlan.name }}</span> 对
          <span class="font-mono text-text">{{ deleteLimitTarget && deleteLimitTarget.target }}</span> 的<b>授权</b>——
          删除等于<b>回收该数据源</b>，其用户会立刻拿到 403。只想改成不限额请把限额填 <span class="font-mono text-text">-1</span>，别删行。确定继续？
        </template>
        <template v-else>
          删除 <span class="font-mono text-text">{{ deleteLimitTarget && deleteLimitTarget.scope }} / {{ deleteLimitTarget && deleteLimitTarget.target }}</span> 这条限额？确定继续？
        </template>
      </p>
    </UiModal>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, nextTick } from 'vue'
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
const limitForm = ref({ scope: 'source', target: '', limit: 100 })
const editingLimit = ref(null)
const limitEditForm = ref({ limit: 0 })
const deleteLimitTarget = ref(null)
const allDataSources = ref([])

// 授权=限额行，所以「还能授权谁」= 全部源减去本套餐已有 source 行的那些名字
const grantableSources = computed(() => {
  const taken = new Set(limits.value.filter(l => l.scope === 'source').map(l => l.target))
  return allDataSources.value.filter(ds => !taken.has(ds.name))
})
const targetHint = computed(() =>
  grantableSources.value.length
    ? `未授权的 ${grantableSources.value.length} 个源；已授权的请在上表「编辑」改限额`
    : '全部数据源都已授权，去上表改限额即可')

const sourceLimitCount = (p) => (p.limits || []).filter(l => l.scope === 'source').length

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    const plansData = (await adminApi.listPlans()) || []
    plans.value = await Promise.all(plansData.map(async p => ({
      ...p,
      limits: await adminApi.listLimits(p.id).catch(() => []),
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

const openLimits = async (p) => {
  limitsPlan.value = p
  limitForm.value = { scope: 'source', target: '', limit: -1 }
  editingLimit.value = null
  deleteLimitTarget.value = null
  await loadLimits()
  if (!allDataSources.value.length) {
    try { allDataSources.value = (await adminApi.listDataSources()) || [] } catch (e) { toast(e.message, 'error') }
  }
}

const loadLimits = async () => {
  try {
    limits.value = (await adminApi.listLimits(limitsPlan.value.id)) || []
  } catch (e) { toast(e.message, 'error') }
}

const addLimit = async () => {
  if (submitting.value) return
  const target = limitForm.value.target.trim()
  if (!target) {
    toast(limitForm.value.scope === 'source' ? '请选择数据源' : '请填写目标', 'error')
    return
  }
  submitting.value = true
  try {
    await adminApi.createLimit({
      plan_id: limitsPlan.value.id,
      scope: limitForm.value.scope,
      target,
      limit: limitForm.value.limit,
    })
    toast(limitForm.value.scope === 'source' ? `已授权并限额 ${target}` : '限制项已添加', 'success')
    limitForm.value.target = ''
    await loadLimits()
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const startEditLimit = (l) => {
  editingLimit.value = l.id
  limitEditForm.value = { limit: l.limit }
}

const saveLimit = async (l) => {
  try {
    await adminApi.updateLimit(l.id, { limit: limitEditForm.value.limit })
    toast('已更新', 'success')
    editingLimit.value = null
    await loadLimits()
    load()
  } catch (e) { toast(e.message, 'error') }
}

const confirmDeleteLimit = async () => {
  const l = deleteLimitTarget.value
  if (!l) return
  try {
    await adminApi.deleteLimit(l.id)
    toast(l.scope === 'source' ? `已回收 ${l.target}` : '已删除', 'success')
    deleteLimitTarget.value = null
    await loadLimits()
    load()
  } catch (e) { toast(e.message, 'error') }
}

onMounted(() => { revealObserve() })

load()
</script>
