<template>
  <div>
    <PageHeader title="数据源管理" subtitle="管理数据源的启用/禁用与基础信息；可通过每个数据源的「套餐权限」快捷调整各套餐的访问权限（完整管理见额度套餐页）。" />

    <!-- 数据源列表 -->
    <section class="mb-12 md:mb-16 reveal">
      <div class="flex items-end justify-between mb-5 pb-3 border-b border-border">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight">数据源列表</h2>
        <button class="btn-primary btn-sm" @click="showCreateDsModal = true">新增数据源</button>
      </div>
      <p v-if="dataSources.length" class="text-xs text-text-muted mb-5">「榜单可见」控制该源是否出现在普通用户的公开排行榜，存的仍是设置项 <span class="font-mono">rank_public_sources</span> 的名单——**这一页是唯一的编辑口**，系统设置页只显示这份名单的摘要。</p>
      <UiSpinner v-if="dsLoading" />
      <UiEmpty v-else-if="dsError" title="加载失败" :text="dsError" />
      <div v-else class="space-y-3">
        <div v-for="ds in dataSources" :key="ds.id" class="card !p-0 overflow-hidden">
          <div class="flex flex-wrap items-center justify-between gap-3 px-5 md:px-6 py-4 border-b border-border bg-surface-alt">
            <div>
              <div class="font-serif text-lg md:text-xl font-medium tracking-tight">{{ ds.display_name }}</div>
              <div class="flex items-center gap-2 font-mono text-xs text-text-muted mt-0.5">
                <span class="px-2 py-0.5 bg-surface border border-border rounded">{{ ds.name }}</span>
                <span class="px-2 py-0.5 bg-surface border border-border rounded">{{ ds.category }}</span>
                <span v-if="groupNameOf(ds.group_id)" class="px-2 py-0.5 bg-surface border border-border rounded">{{ groupNameOf(ds.group_id) }}</span>
                <span class="text-text-muted">排序: {{ ds.sort_order }}</span>
              </div>
            </div>
            <div class="flex items-center gap-3">
              <UiTag :tone="ds.status === 1 ? 'green' : 'gray'" :label="ds.status === 1 ? '启用' : '禁用'" />
              <button @click="toggleRankPublic(ds)" :disabled="rankBusy === ds.id" class="text-xs transition-colors"
                :class="ds.rank_public ? 'text-pale-green-fg' : 'text-text-muted hover:text-text'">{{ ds.rank_public ? '榜单可见' : '榜单隐藏' }}</button>
              <button @click="openPlanPerms(ds)" class="text-xs text-text-muted hover:text-text transition-colors">套餐权限</button>
              <button @click="editDataSource(ds)" class="text-xs text-text-muted hover:text-text transition-colors">编辑</button>
              <button @click="deleteDataSource(ds)" class="text-xs text-pale-red-fg hover:opacity-70 transition-opacity">删除</button>
            </div>
          </div>
        </div>
      </div>
    </section>

    <!-- 分组管理 -->
    <section class="mb-12 md:mb-16 reveal">
      <div class="flex items-end justify-between mb-3 pb-3 border-b border-border">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight">分组管理</h2>
        <button class="btn-primary btn-sm" @click="openCreateGroup">新增分组</button>
      </div>
      <p class="text-xs text-text-muted mb-5">分组是给数据源打的归类标签，仅用于筛选与批量操作，不参与额度、计费、限速的解析；「按组套用限额」只是把成员的限额一次性写入（每源一行）。</p>
      <UiSpinner v-if="groupsLoading" />
      <UiEmpty v-else-if="groupsError" title="加载失败" :text="groupsError" />
      <UiEmpty v-else-if="sourceGroups.length === 0" title="暂无分组" text="删除分组不会删除其中的数据源，成员会回落到未分组。" />
      <div v-else class="space-y-3">
        <div v-for="g in sourceGroups" :key="g.id" class="card !p-0 overflow-hidden">
          <div class="flex flex-wrap items-center justify-between gap-3 px-5 md:px-6 py-4 border-b border-border bg-surface-alt">
            <div>
              <div class="font-serif text-lg md:text-xl font-medium tracking-tight">{{ g.name }}</div>
              <div class="flex items-center gap-2 font-mono text-xs text-text-muted mt-0.5">
                <span class="px-2 py-0.5 bg-surface border border-border rounded">成员: {{ g.member_count }}</span>
                <span class="text-text-muted">排序: {{ g.sort_order }}</span>
                <span v-if="g.description" class="truncate max-w-[16rem]" :title="g.description">{{ g.description }}</span>
              </div>
            </div>
            <div class="flex items-center gap-3">
              <UiTag :tone="g.status === 1 ? 'green' : 'gray'" :label="g.status === 1 ? '启用' : '停用'" />
              <button @click="openMembers(g)" class="text-xs text-text-muted hover:text-text transition-colors">成员</button>
              <button @click="openApplyLimits(g)" class="text-xs text-text-muted hover:text-text transition-colors">套用限额</button>
              <button @click="editGroup(g)" class="text-xs text-text-muted hover:text-text transition-colors">编辑</button>
              <button @click="deleteGroup(g)" class="text-xs text-pale-red-fg hover:opacity-70 transition-opacity">删除</button>
            </div>
          </div>
        </div>
      </div>
      <div class="mt-3 font-mono text-xs text-text-muted">未分组数据源: {{ ungroupedCount }}</div>
    </section>

    <!-- 数据源默认地址 -->
    <section class="mt-12 md:mt-16 reveal">
      <div class="flex items-end justify-between mb-5 pb-3 border-b border-border">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight">默认地址</h2>
        <button v-if="sourceConfigDirty" class="btn-primary btn-sm" :disabled="savingConfig" @click="saveSourceConfigs">保存</button>
      </div>
      <UiSpinner v-if="sourceConfigs === null" />
      <div v-else class="space-y-3">
        <div v-for="cfg in sourceConfigs" :key="cfg.source_name" class="flex items-center gap-3">
          <div class="font-mono text-sm min-w-[130px]">{{ cfg.source_display }}</div>
          <div class="font-mono text-xs text-text-muted min-w-[90px]">{{ cfg.source_name }}</div>
          <input v-model="cfg.base_url" class="input flex-1 font-mono text-sm" placeholder="留空则需用户传参" @input="sourceConfigDirty = true">
        </div>
      </div>
    </section>

    <!-- 新增数据源弹窗 -->
    <UiModal v-model:open="showCreateDsModal" title="新增数据源" wide @confirm="createDataSource" :confirm-loading="creatingDs" confirm-text="创建">
      <div class="space-y-4">
        <div>
          <label class="label">标识名 *</label>
          <input v-model="createDsForm.name" class="input font-mono" placeholder="如: my_novel" maxlength="64">
          <p class="text-xs text-text-muted mt-1">唯一标识，创建后不可修改</p>
        </div>
        <div>
          <label class="label">显示名称 *</label>
          <input v-model="createDsForm.display_name" class="input" placeholder="如: 我的小说源" maxlength="128">
        </div>
        <div>
          <label class="label">分类 *</label>
          <input v-model="createDsForm.category" class="input" placeholder="如: novel" maxlength="32">
        </div>
        <div>
          <label class="label">描述</label>
          <textarea v-model="createDsForm.description" class="input" rows="2" maxlength="255" placeholder="数据源描述"></textarea>
        </div>
        <div>
          <label class="label">排序</label>
          <input v-model.number="createDsForm.sort_order" type="number" class="input w-24" min="0">
        </div>
        <div class="flex items-center gap-2">
          <input type="checkbox" id="createDsStatus" v-model="createDsForm.status" class="checkbox">
          <label for="createDsStatus" class="text-sm">启用</label>
        </div>
      </div>
    </UiModal>

    <!-- 编辑数据源弹窗 -->
    <UiModal v-model:open="showEditDsModal" title="编辑数据源" wide @confirm="updateDataSource" :confirm-loading="updatingDs" confirm-text="保存">
      <div class="space-y-4">
        <div>
          <label class="label">标识名</label>
          <input v-model="editDsForm.name" class="input font-mono" disabled>
        </div>
        <div>
          <label class="label">显示名称 *</label>
          <input v-model="editDsForm.display_name" class="input" maxlength="128">
        </div>
        <div>
          <label class="label">分类 *</label>
          <input v-model="editDsForm.category" class="input" placeholder="如: novel" maxlength="32">
        </div>
        <div>
          <label class="label">描述</label>
          <textarea v-model="editDsForm.description" class="input" rows="2" maxlength="255"></textarea>
        </div>
        <div>
          <label class="label">排序</label>
          <input v-model.number="editDsForm.sort_order" type="number" class="input w-24" min="0">
        </div>
        <div>
          <label class="label">分组</label>
          <select v-model="editDsForm.group_id" class="input">
            <option :value="0">未分组</option>
            <option v-for="g in sourceGroups" :key="g.id" :value="g.id">{{ g.name }}</option>
          </select>
          <p class="text-xs text-text-muted mt-1">分组仅是归类标签，不影响访问与计费；选「未分组」会把该源移出所属组</p>
        </div>
        <div class="flex items-center gap-2">
          <input type="checkbox" id="editDsStatus" v-model="editDsForm.status" class="checkbox">
          <label for="editDsStatus" class="text-sm">启用</label>
        </div>
      </div>
    </UiModal>

    <!-- 套餐权限快捷操作弹窗 -->
    <UiModal :open="permOpen" :title="'套餐权限 — ' + (permDs && permDs.display_name)" @close="permOpen = false" @confirm="savePlanPerms" :confirm-loading="permSaving" confirm-text="保存">
      <UiSpinner v-if="permLoading" />
      <div v-else class="space-y-2">
        <p class="text-text-muted text-sm">勾选允许访问「{{ permDs && permDs.display_name }}」的套餐：</p>
        <div class="max-h-64 overflow-y-auto border border-border rounded-lg p-3 space-y-1.5">
          <label v-for="p in plans" :key="p.id" class="flex items-center gap-2 text-sm cursor-pointer rounded px-1 py-0.5 hover:bg-surface-alt transition-colors">
            <input type="checkbox" v-model="permChecked[p.id]" class="checkbox">
            <span>{{ p.name }}</span>
            <span class="font-mono text-xs text-text-muted">{{ p.code }}</span>
          </label>
          <div v-if="plans.length === 0" class="text-text-muted text-sm text-center py-4">暂无套餐</div>
        </div>
      </div>
    </UiModal>

    <!-- 新增/编辑分组弹窗 -->
    <UiModal v-model:open="showGroupModal" :title="groupModalMode === 'create' ? '新增分组' : '编辑分组'" @confirm="saveGroup" :confirm-loading="savingGroup" :confirm-text="groupModalMode === 'create' ? '创建' : '保存'">
      <div class="space-y-4">
        <div>
          <label class="label">组名 *</label>
          <input v-model="groupForm.name" class="input" placeholder="如: 网文精选" maxlength="64">
          <p class="text-xs text-text-muted mt-1">组名唯一，创建后可改名</p>
        </div>
        <div>
          <label class="label">描述</label>
          <textarea v-model="groupForm.description" class="input" rows="2" maxlength="255" placeholder="分组说明"></textarea>
        </div>
        <div>
          <label class="label">排序</label>
          <input v-model.number="groupForm.sort_order" type="number" class="input w-24" min="0">
        </div>
        <div class="flex items-center gap-2">
          <input type="checkbox" id="groupFormStatus" v-model="groupForm.status" class="checkbox">
          <label for="groupFormStatus" class="text-sm">启用</label>
        </div>
      </div>
    </UiModal>

    <!-- 分组成员弹窗（整盘提交：勾选列表即最终成员） -->
    <UiModal v-model:open="showMembersModal" :title="'成员 — ' + (membersGroup && membersGroup.name)" @confirm="saveMembers" :confirm-loading="savingMembers" confirm-text="保存">
      <div class="space-y-2">
        <p class="text-text-muted text-sm">勾选属于「{{ membersGroup && membersGroup.name }}」的数据源：取消勾选即移出本组，已在他组的会被移到本组（一源至多一组）。</p>
        <div class="max-h-64 overflow-y-auto border border-border rounded-lg p-3 space-y-1.5">
          <label v-for="ds in dataSources" :key="ds.name" class="flex items-center gap-2 text-sm cursor-pointer rounded px-1 py-0.5 hover:bg-surface-alt transition-colors">
            <input type="checkbox" v-model="memberChecked[ds.name]" class="checkbox">
            <span>{{ ds.display_name }}</span>
            <span class="font-mono text-xs text-text-muted">{{ ds.name }}</span>
          </label>
          <div v-if="dataSources.length === 0" class="text-text-muted text-sm text-center py-4">暂无数据源</div>
        </div>
      </div>
    </UiModal>

    <!-- 按组套用限额弹窗 -->
    <UiModal v-model:open="showLimitsModal" :title="'按组套用限额 — ' + (limitsGroup && limitsGroup.name)" @confirm="submitApplyLimits" :confirm-loading="savingLimits" confirm-text="套用">
      <div class="space-y-4">
        <p class="text-text-muted text-sm">为该分组当前 {{ limitsGroup && limitsGroup.member_count }} 个成员批量写入限额，落库仍是每源一行；分组本身不是计费对象。</p>
        <div>
          <label class="label">套餐 *</label>
          <select v-model="limitsForm.plan_code" class="input">
            <option value="" disabled>请选择套餐</option>
            <option v-for="p in plans" :key="p.id" :value="p.code">{{ p.name }}（{{ p.code }}）</option>
          </select>
        </div>
        <div>
          <label class="label">限额</label>
          <input v-model.number="limitsForm.limit" type="number" class="input font-mono" min="-1">
          <p class="text-xs text-text-muted mt-1">-1 = 不限，0 = 当日不可用，大于 0 = 每日次数</p>
        </div>
        <div>
          <label class="label">周期</label>
          <select v-model="limitsForm.period" class="input w-32">
            <option value="day">day</option>
            <option value="week">week</option>
            <option value="month">month</option>
          </select>
        </div>
        <div v-if="applyResult" class="text-xs text-text-muted border-t border-border pt-3">
          <div class="mb-1">已为 {{ applyResult.applied }} 个数据源套用「{{ applyResult.plan_code }}」限额 {{ applyResult.limit < 0 ? '不限' : applyResult.limit }}。</div>
          <div class="font-mono break-words">{{ (applyResult.sources || []).join(', ') }}</div>
        </div>
      </div>
    </UiModal>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, nextTick } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiTag from '../../components/UiTag.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import UiModal from '../../components/UiModal.vue'
import { adminApi, quotaApi } from '../../api/index.js'
import { toast, revealObserve } from '../../utils.js'

const dsLoading = ref(true)
const dsError = ref('')
const dataSources = ref([])
// 套餐权限快捷操作
const plans = ref([])
const plansLoaded = ref(false)
const permOpen = ref(false)
const permDs = ref(null)
const permChecked = ref({})
const permLoading = ref(false)
const permSaving = ref(false)
const sourceConfigs = ref(null)
const sourceConfigDirty = ref(false)
const savingConfig = ref(false)
const showCreateDsModal = ref(false)
const creatingDs = ref(false)
const createDsForm = ref({
  name: '',
  display_name: '',
  category: '',
  description: '',
  sort_order: 0,
  status: true,
})
const showEditDsModal = ref(false)
const updatingDs = ref(false)
const editDsForm = ref({})
const editingDsId = ref(null)

// 分组管理
const sourceGroups = ref([])
const ungroupedCount = ref(0)
const groupsLoading = ref(true)
const groupsError = ref('')
const showGroupModal = ref(false)
const groupModalMode = ref('create')
const editingGroupId = ref(null)
const savingGroup = ref(false)
const groupForm = ref({ name: '', description: '', sort_order: 0, status: true })
const showMembersModal = ref(false)
const membersGroup = ref(null)
const memberChecked = ref({})
const savingMembers = ref(false)
const showLimitsModal = ref(false)
const limitsGroup = ref(null)
const limitsForm = ref({ plan_code: '', limit: 100, period: 'day' })
const savingLimits = ref(false)
const applyResult = ref(null)

const loadDataSources = async () => {
  dsLoading.value = true
  dsError.value = ''
  try {
    dataSources.value = await adminApi.listDataSources()
  } catch (e) {
    dsError.value = e.message
  }
  dsLoading.value = false
  nextTick(revealObserve)
}

const rankBusy = ref(null)

// 榜单可见性：后端把它写回设置项 rank_public_sources 的名单，这里只翻开关
const toggleRankPublic = async (ds) => {
  if (rankBusy.value === ds.id) return
  rankBusy.value = ds.id
  const next = !ds.rank_public
  try {
    await adminApi.updateDataSource(ds.id, { rank_public: next })
    ds.rank_public = next
    toast(next ? `已开放「${ds.display_name}」的公开榜单` : `已关闭「${ds.display_name}」的公开榜单`, 'success')
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    rankBusy.value = null
  }
}

// ===== 分组管理 =====
const groupNameOf = (id) => {
  if (!id) return ''
  const hit = sourceGroups.value.find(g => Number(g.id) === Number(id))
  return hit ? hit.name : ''
}

const loadSourceGroups = async () => {
  groupsLoading.value = true
  groupsError.value = ''
  try {
    const res = await adminApi.listSourceGroups()
    sourceGroups.value = (res && res.groups) || []
    ungroupedCount.value = (res && res.ungrouped_count) || 0
  } catch (e) {
    groupsError.value = e.message
  }
  groupsLoading.value = false
  nextTick(revealObserve)
}

const openCreateGroup = () => {
  groupModalMode.value = 'create'
  editingGroupId.value = null
  groupForm.value = { name: '', description: '', sort_order: 0, status: true }
  showGroupModal.value = true
}

const editGroup = (g) => {
  groupModalMode.value = 'edit'
  editingGroupId.value = g.id
  groupForm.value = {
    name: g.name,
    description: g.description || '',
    sort_order: Number(g.sort_order) || 0,
    status: g.status === 1,
  }
  showGroupModal.value = true
}

const saveGroup = async () => {
  if (savingGroup.value) return
  const name = (groupForm.value.name || '').trim()
  if (!name) { toast('组名不能为空', 'error'); return }
  const payload = {
    name,
    description: groupForm.value.description || '',
    status: groupForm.value.status ? 1 : 0,
    sort_order: Number(groupForm.value.sort_order) || 0,
  }
  savingGroup.value = true
  try {
    if (groupModalMode.value === 'create') await adminApi.createSourceGroup(payload)
    else await adminApi.updateSourceGroup(editingGroupId.value, payload)
    toast('保存成功', 'success')
    showGroupModal.value = false
    await loadSourceGroups()
  } catch (e) { toast(e.message, 'error') } finally { savingGroup.value = false }
}

const deleteGroup = (g) => {
  adminApi.deleteSourceGroup(g.id).then(() => {
    toast('分组已删除，成员回落未分组', 'success')
    loadSourceGroups()
    loadDataSources()
  }).catch(e => toast(e.message, 'error'))
}

const openMembers = (g) => {
  membersGroup.value = g
  const map = {}
  for (const ds of dataSources.value) map[ds.name] = Number(ds.group_id) === g.id
  memberChecked.value = map
  showMembersModal.value = true
}

const saveMembers = async () => {
  if (savingMembers.value) return
  const names = Object.keys(memberChecked.value).filter(k => memberChecked.value[k])
  savingMembers.value = true
  try {
    // 整盘提交：列表即最终成员；未知源码后端会 404 并在 msg 里点名，直接透出
    const res = await adminApi.updateSourceGroupMembers(membersGroup.value.id, names)
    toast('成员已保存，共 ' + ((res && res.member_count) ?? names.length) + ' 个', 'success')
    showMembersModal.value = false
    await Promise.all([loadSourceGroups(), loadDataSources()])
  } catch (e) { toast(e.message, 'error') } finally { savingMembers.value = false }
}

const openApplyLimits = async (g) => {
  limitsGroup.value = g
  limitsForm.value = { plan_code: '', limit: 100, period: 'day' }
  applyResult.value = null
  showLimitsModal.value = true
  if (!plansLoaded.value) {
    try { await loadPlansDataSources() } catch (e) { toast(e.message, 'error') }
  }
}

const submitApplyLimits = async () => {
  if (savingLimits.value) return
  const planCode = (limitsForm.value.plan_code || '').trim()
  if (!planCode) { toast('请选择套餐', 'error'); return }
  const limit = Number(limitsForm.value.limit)
  if (!Number.isFinite(limit)) { toast('限额需为数字', 'error'); return }
  savingLimits.value = true
  try {
    const res = await adminApi.applySourceGroupLimits(limitsGroup.value.id, {
      plan_code: planCode, limit, period: limitsForm.value.period,
    })
    applyResult.value = res
    toast('已套用 ' + res.applied + ' 个数据源', 'success')
  } catch (e) { toast(e.message, 'error') } finally { savingLimits.value = false }
}

const createDataSource = async () => {
  creatingDs.value = true
  try {
    // status 后端声明是 *int（1/0），勾选框是布尔——原样提交会被 binding 打成 400
    await adminApi.createDataSource({ ...createDsForm.value, status: createDsForm.value.status ? 1 : 0 })
    toast('创建成功', 'success')
    showCreateDsModal.value = false
    createDsForm.value = { name: '', display_name: '', category: '', description: '', sort_order: 0, status: true }
    loadDataSources()
    loadPlansDataSources()
  } catch (e) { toast(e.message, 'error') } finally { creatingDs.value = false }
}

const editDataSource = (ds) => {
  editingDsId.value = ds.id
  editDsForm.value = { 
    ...ds, 
    status: ds.status === 1 || ds.status === true,
    sort_order: Number(ds.sort_order) || 0,
    group_id: ds.group_id ? Number(ds.group_id) : 0
  }
  showEditDsModal.value = true
}

const updateDataSource = async () => {
  updatingDs.value = true
  try {
    const payload = {
      ...editDsForm.value,
      status: editDsForm.value.status ? 1 : 0,
      sort_order: Number(editDsForm.value.sort_order) || 0
    }
    // 弹窗不编辑榜单可见性：带着它提交会把设置项白写一遍（行上的开关才是入口）
    delete payload.rank_public
    // 分组：选具体组走 group_id；未分组走 clear_group（二者互斥，group_id=0 会被后端当成非法组 id）
    const gid = Number(payload.group_id)
    if (gid > 0) {
      payload.group_id = gid
    } else {
      delete payload.group_id
      payload.clear_group = true
    }
    await adminApi.updateDataSource(editingDsId.value, payload)
    toast('更新成功', 'success')
    showEditDsModal.value = false
    loadDataSources()
    loadSourceGroups()
  } catch (e) { toast(e.message, 'error') } finally { updatingDs.value = false }
}

const deleteDataSource = (ds) => {
  adminApi.deleteDataSource(ds.id).then(() => {
    toast('已删除', 'success')
    loadDataSources()
    loadPlansDataSources()
  }).catch(e => toast(e.message, 'error'))
}

const loadPlansDataSources = async () => {
  const plansData = await adminApi.listPlans()
  for (const plan of plansData) {
    const res = await adminApi.listPlanDataSources(plan.id)
    plan.data_source_ids = (res.data_sources || []).map(d => d.id)
  }
  plans.value = plansData
  plansLoaded.value = true
}

const openPlanPerms = async (ds) => {
  permDs.value = ds
  permOpen.value = true
  permLoading.value = true
  try {
    if (!plansLoaded.value) await loadPlansDataSources()
    const map = {}
    for (const p of plans.value) map[p.id] = (p.data_source_ids || []).includes(ds.id)
    permChecked.value = map
  } catch (e) { toast(e.message, 'error') }
  permLoading.value = false
}

const savePlanPerms = async () => {
  if (permSaving.value || !permDs.value) return
  permSaving.value = true
  try {
    const dsId = permDs.value.id
    const ops = []
    for (const p of plans.value) {
      const was = (p.data_source_ids || []).includes(dsId)
      const now = !!permChecked.value[p.id]
      if (was === now) continue
      ops.push(now
        ? adminApi.addPlanDataSource(p.id, dsId)
        : adminApi.removePlanDataSource(p.id, dsId))
    }
    await Promise.all(ops)
    await loadPlansDataSources()
    toast('套餐权限已保存', 'success')
    permOpen.value = false
    permDs.value = null
  } catch (e) { toast(e.message, 'error') } finally { permSaving.value = false }
}

const loadSourceConfigs = async () => {
  try {
    sourceConfigs.value = await adminApi.listSourceConfigs()
    sourceConfigDirty.value = false
  } catch (e) {
    sourceConfigs.value = []
  }
}

const saveSourceConfigs = async () => {
  if (savingConfig.value) return
  savingConfig.value = true
  try {
    await adminApi.updateSourceConfigs(sourceConfigs.value)
    toast('默认地址已保存', 'success')
    sourceConfigDirty.value = false
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    savingConfig.value = false
  }
}

onMounted(() => { revealObserve() })
loadDataSources()
loadSourceConfigs()
loadSourceGroups()
</script>