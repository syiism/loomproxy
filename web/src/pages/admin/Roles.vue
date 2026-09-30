<template>
  <div>
    <PageHeader title="角色管理" subtitle="系统角色及其说明。内置角色不可删除。">
      <template #actions>
        <button class="btn-primary" @click="openCreate">新建角色</button>
      </template>
    </PageHeader>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <UiEmpty v-else-if="list.length === 0" title="暂无角色" />
    <template v-else>
      <!-- 移动端卡片 -->
      <div class="sm:hidden">
        <div v-for="r in list" :key="r.id" class="card mb-3 reveal">
          <div class="flex justify-between items-start mb-3">
            <div>
              <div class="font-medium">{{ r.name }}</div>
              <div class="font-mono text-xs text-text-muted">#{{ r.id }}</div>
            </div>
            <UiTag :tone="statusTone(r.status)" :label="r.status === 1 ? '启用' : '禁用'" />
          </div>
          <div class="space-y-2 text-sm mb-4">
            <div><UiTag :tone="roleTone(r.code)" :label="r.code" /></div>
            <div><span class="text-text-muted">说明：</span>{{ r.description || '—' }}</div>
          </div>
          <div class="flex gap-2 pt-3 border-t border-border">
            <button class="btn-ghost btn-sm flex-1" @click="openEdit(r)">编辑</button>
            <button v-if="!isBuiltin(r)" class="btn-danger btn-sm flex-1" @click="deleteTarget = r">删除</button>
          </div>
        </div>
      </div>

      <!-- 桌面端表格 -->
      <div class="hidden sm:block table-wrap reveal">
        <table class="table-base">
          <thead>
            <tr><th>ID</th><th>代码</th><th>名称</th><th>说明</th><th>状态</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="r in list" :key="r.id">
              <td class="font-mono text-xs">{{ r.id }}</td>
              <td><UiTag :tone="roleTone(r.code)" :label="r.code" /></td>
              <td class="font-medium">{{ r.name }}</td>
              <td class="text-text-muted">{{ r.description || '—' }}</td>
              <td><UiTag :tone="statusTone(r.status)" :label="r.status === 1 ? '启用' : '禁用'" /></td>
              <td>
                <div class="flex gap-3">
                  <button @click="openEdit(r)" class="text-xs text-text-muted hover:text-text transition-colors">编辑</button>
                  <button v-if="!isBuiltin(r)" @click="deleteTarget = r" class="text-xs text-pale-red-fg hover:opacity-70 transition-opacity">删除</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- 新建/编辑角色 -->
    <UiModal :open="modalOpen" :title="editing ? '编辑角色 #' + editing.id : '新建角色'" @close="modalOpen = false" @confirm="save">
      <form @submit.prevent="save" class="space-y-5">
        <UiField label="代码" hint="小写字母、数字与下划线，创建后不可修改">
          <input v-model="form.code" class="input font-mono" :disabled="!!editing" maxlength="32" pattern="[a-z0-9_]+" required>
        </UiField>
        <UiField label="名称">
          <input v-model="form.name" class="input" maxlength="64" required>
        </UiField>
        <UiField label="说明">
          <input v-model="form.description" class="input" maxlength="255">
        </UiField>
        <UiField v-if="editing" label="状态">
          <select v-model="form.status" class="input" :disabled="editing.code === 'admin'">
            <option :value="1">启用</option>
            <option :value="0">禁用</option>
          </select>
        </UiField>
      </form>
    </UiModal>

    <!-- 删除确认 -->
    <UiModal :open="!!deleteTarget" :title="'删除角色 #' + (deleteTarget && deleteTarget.id)" @close="deleteTarget = null" @confirm="confirmDelete">
      <p class="text-text-muted text-sm">确定删除角色 <span class="font-mono text-text">{{ deleteTarget && deleteTarget.code }}</span> 吗？仍被用户引用的角色无法删除。</p>
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
import { roleTone, statusTone, toast, revealObserve } from '../../utils.js'

const BUILTIN = ['admin', 'user', 'vip']

const loading = ref(true)
const error = ref('')
const list = ref([])
const submitting = ref(false)
const modalOpen = ref(false)
const editing = ref(null)
const form = ref({ code: '', name: '', description: '', status: 1 })
const deleteTarget = ref(null)

const isBuiltin = (r) => BUILTIN.includes(r.code)

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    list.value = (await adminApi.listRoles()) || []
  } catch (e) {
    error.value = e.message
  }
  loading.value = false
  nextTick(revealObserve)
}

const openCreate = () => {
  editing.value = null
  form.value = { code: '', name: '', description: '', status: 1 }
  modalOpen.value = true
}

const openEdit = (r) => {
  editing.value = r
  form.value = { code: r.code, name: r.name, description: r.description || '', status: r.status }
  modalOpen.value = true
}

const save = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    if (editing.value) {
      await adminApi.updateRole(editing.value.id, {
        name: form.value.name.trim(),
        description: form.value.description.trim(),
        status: form.value.status,
      })
      toast('角色已更新', 'success')
    } else {
      await adminApi.createRole({
        code: form.value.code.trim(),
        name: form.value.name.trim(),
        description: form.value.description.trim(),
      })
      toast('角色已创建', 'success')
    }
    modalOpen.value = false
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const confirmDelete = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.deleteRole(deleteTarget.value.id)
    toast('已删除', 'success')
    deleteTarget.value = null
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

onMounted(() => { revealObserve() })
load()
</script>
