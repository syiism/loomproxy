<template>
  <div>
    <PageHeader title="数据源管理" subtitle="管理数据源的启用/禁用与基础信息；可通过每个数据源的「套餐权限」快捷调整各套餐的访问权限（完整管理见额度套餐页）。" />

    <!-- 数据源列表 -->
    <section class="mb-12 md:mb-16 reveal">
      <div class="flex items-end justify-between mb-5 pb-3 border-b border-border">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight">数据源列表</h2>
        <button class="btn-primary btn-sm" @click="showCreateDsModal = true">新增数据源</button>
      </div>
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
                <span class="text-text-muted">排序: {{ ds.sort_order }}</span>
              </div>
            </div>
            <div class="flex items-center gap-3">
              <UiTag :tone="ds.status === 1 ? 'green' : 'gray'" :label="ds.status === 1 ? '启用' : '禁用'" />
              <button @click="openPlanPerms(ds)" class="text-xs text-text-muted hover:text-text transition-colors">套餐权限</button>
              <button @click="editDataSource(ds)" class="text-xs text-text-muted hover:text-text transition-colors">编辑</button>
              <button @click="deleteDataSource(ds)" class="text-xs text-pale-red-fg hover:opacity-70 transition-opacity">删除</button>
            </div>
          </div>
        </div>
      </div>
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

const createDataSource = async () => {
  creatingDs.value = true
  try {
    await adminApi.createDataSource(createDsForm.value)
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
    sort_order: Number(ds.sort_order) || 0 
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
    await adminApi.updateDataSource(editingDsId.value, payload)
    toast('更新成功', 'success')
    showEditDsModal.value = false
    loadDataSources()
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
</script>