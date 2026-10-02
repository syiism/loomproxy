<template>
  <div>
    <PageHeader title="用户管理" subtitle="所有注册用户及其角色与套餐。">
      <template #actions>
        <button class="btn-primary" @click="openCreate">新建用户</button>
      </template>
    </PageHeader>

    <div class="reveal flex flex-col sm:flex-row gap-3 mb-6">
      <input v-model="keyword" placeholder="搜索用户名 / 邮箱 / 昵称" class="input flex-1" @keydown.enter="doSearch">
      <button @click="doSearch" class="btn-ghost whitespace-nowrap">搜索</button>
      <label class="flex items-center gap-1.5 text-sm text-text-muted cursor-pointer whitespace-nowrap select-none">
        <input type="checkbox" v-model="withDeleted" @change="doSearch" class="w-4 h-4 rounded border-border text-text focus:ring-text">显示已删除
      </label>
    </div>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <UiEmpty v-else-if="list.length === 0" title="暂无用户" text="没有匹配的用户记录。" />
    <template v-else>
      <!-- 移动端卡片 -->
      <div class="sm:hidden">
        <div v-for="u in list" :key="u.id" class="card mb-3 reveal">
          <div class="flex justify-between items-start mb-3">
            <div>
              <div class="font-medium text-base">{{ u.username }}</div>
              <div class="font-mono text-xs text-text-muted">#{{ u.id }}</div>
            </div>
            <UiTag v-if="u.deleted_at" tone="gray" label="已删除" />
            <UiTag v-else :tone="statusTone(u.status)" :label="u.status === 1 ? '启用' : '禁用'" />
          </div>
          <div class="space-y-2 text-sm">
            <div v-if="u.email"><span class="text-text-muted">邮箱：</span>{{ u.email }}</div>
            <div class="flex items-center gap-1.5"><span class="text-text-muted">角色：</span>
              <UiTag v-for="r in (u.roles || [])" :key="r.code" :tone="roleTone(r.code)" :label="r.name || r.code" />
            </div>
            <div class="flex items-center gap-1.5"><span class="text-text-muted">套餐：</span>
              <UiTag v-if="u.plan" tone="blue" :label="u.plan.name || u.plan.code" /><span v-else class="text-text-muted">免费版</span>
            </div>
            <div><span class="text-text-muted">最近登录：</span>{{ fmtDate(u.last_login_at) }}</div>
          </div>
          <div class="flex flex-wrap gap-2 mt-4 pt-3 border-t border-border">
            <template v-if="u.deleted_at">
              <button @click="confirmRestore(u)" class="btn-ghost btn-sm flex-1 min-w-0">恢复</button>
            </template>
            <template v-else>
              <button @click="openEdit(u)" class="btn-ghost btn-sm flex-[2] min-w-0">编辑</button>
              <button @click="openRoles(u)" class="btn-ghost btn-sm flex-1 min-w-0">角色</button>
              <button @click="openQuota(u)" class="btn-ghost btn-sm flex-1 min-w-0">额度</button>
              <button @click="openResetPwd(u)" class="btn-ghost btn-sm flex-1 min-w-0">重置密码</button>
              <button @click="deleteTarget = u; deleteOpen = true" class="btn-danger btn-sm flex-1 min-w-0">删除</button>
            </template>
          </div>
        </div>
      </div>

      <!-- 桌面端表格 -->
      <div class="hidden sm:block table-wrap reveal overflow-x-auto">
        <table class="table-base">
          <thead>
            <tr>
              <th>ID</th><th>用户名</th><th>邮箱</th><th>角色</th><th>套餐</th><th>状态</th>
              <th class="hidden lg:table-cell">最近登录</th><th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in list" :key="u.id">
              <td class="font-mono text-xs">{{ u.id }}</td>
              <td>
                <div class="font-medium whitespace-nowrap">{{ u.username }}</div>
                <div v-if="u.nickname" class="text-xs text-text-muted">{{ u.nickname }}</div>
              </td>
              <td class="text-text-muted max-w-[200px] truncate">{{ u.email || '—' }}</td>
              <td>
                <span class="flex flex-wrap gap-1">
                  <UiTag v-for="r in (u.roles || [])" :key="r.code" :tone="roleTone(r.code)" :label="r.name || r.code" />
                  <span v-if="!(u.roles || []).length" class="text-text-muted">—</span>
                </span>
              </td>
              <td class="whitespace-nowrap">
                <UiTag v-if="u.plan" tone="blue" :label="u.plan.name || u.plan.code" />
                <span v-else class="text-text-muted">免费版</span>
              </td>
              <td class="whitespace-nowrap">
                <UiTag v-if="u.deleted_at" tone="gray" label="已删除" />
                <UiTag v-else :tone="statusTone(u.status)" :label="u.status === 1 ? '启用' : '禁用'" />
              </td>
              <td class="font-mono text-xs text-text-muted whitespace-nowrap hidden lg:table-cell">{{ fmtDate(u.last_login_at) }}</td>
              <td class="whitespace-nowrap">
                <div v-if="u.deleted_at" class="flex items-center gap-1.5 text-xs" style="white-space: nowrap">
                  <button @click="confirmRestore(u)" class="text-pale-green-fg hover:opacity-70 transition-opacity">恢复</button>
                </div>
                <div v-else class="flex items-center gap-1.5 text-xs" style="white-space: nowrap">
                  <button @click="openEdit(u)" class="text-text-muted hover:text-text transition-colors">编辑</button>
                  <span class="text-border select-none">|</span>
                  <button @click="openRoles(u)" class="text-text-muted hover:text-text transition-colors">角色</button>
                  <span class="text-border select-none">|</span>
                  <button @click="openQuota(u)" class="text-text-muted hover:text-text transition-colors">额度</button>
                  <span class="text-border select-none">|</span>
                  <button @click="openResetPwd(u)" class="text-text-muted hover:text-text transition-colors">重置密码</button>
                  <span class="text-border select-none">|</span>
                  <button @click="deleteTarget = u; deleteOpen = true" class="text-pale-red-fg hover:opacity-70 transition-opacity">删除</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <UiPagination :page="page" :total="total" :page-size="pageSize" @change="goPage" />
    </template>

    <!-- 新建用户 -->
    <UiModal v-model:open="createOpen" title="新建用户" @confirm="saveCreate" :confirm-loading="submitting" confirm-text="创建">
      <form @submit.prevent="saveCreate" class="space-y-5">
        <UiField label="用户名" hint="3–64 个字符">
          <input v-model="createForm.username" class="input" minlength="3" maxlength="64" required>
        </UiField>
        <UiField label="初始密码" hint="8–16 位，包含字母和数字">
          <input v-model="createForm.password" type="password" class="input" minlength="8" maxlength="16" required>
        </UiField>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <UiField label="昵称（可选）">
            <input v-model="createForm.nickname" class="input" maxlength="64">
          </UiField>
          <UiField label="邮箱">
            <input v-model="createForm.email" type="email" class="input" required>
          </UiField>
        </div>
        <UiField label="角色" :hint="planBoundRole ? '已按所选套餐自动绑定角色，如需调整请先更换套餐或修改套餐绑定' : '角色为单选'">
          <select v-model="createForm.role_code" class="input" :disabled="!!planBoundRole">
            <option value="">（无角色）</option>
            <option v-for="r in allRoles" :key="r.code" :value="r.code">
              {{ r.name }}（{{ r.code }}）{{ r.status !== 1 ? ' — 已禁用' : '' }}
            </option>
          </select>
        </UiField>
        <UiField label="套餐">
          <select v-model="createForm.plan_id" class="input">
            <option v-for="p in allPlans" :key="p.id" :value="p.id">{{ planLabel(p) }}</option>
          </select>
        </UiField>
      </form>
    </UiModal>

    <!-- 编辑用户 -->
    <UiModal v-model:open="editOpen" :title="'编辑用户 #' + (editUser && editUser.id)" @confirm="saveEdit" :confirm-loading="submitting">
      <form @submit.prevent="saveEdit" class="space-y-5">
        <UiField label="用户名">
          <input :value="editUser.username" disabled class="input">
        </UiField>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <UiField label="昵称">
            <input v-model="editForm.nickname" class="input">
          </UiField>
          <UiField label="邮箱">
            <input v-model="editForm.email" class="input">
          </UiField>
        </div>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <UiField label="状态">
            <select v-model="editForm.status" class="input">
              <option :value="1">启用</option>
              <option :value="0">禁用</option>
            </select>
          </UiField>
          <UiField label="套餐" hint="更换套餐时自动切换为套餐绑定的角色">
            <select v-model="editForm.plan_id" class="input">
              <option v-for="p in allPlans" :key="p.id" :value="p.id">{{ planLabel(p) }}</option>
            </select>
          </UiField>
        </div>
      </form>
    </UiModal>

    <!-- 角色 -->
    <UiModal v-model:open="rolesOpen" :title="'修改角色 — ' + (rolesUser && rolesUser.username)" @confirm="saveRoles" :confirm-loading="submitting">
      <form @submit.prevent="saveRoles" class="space-y-4">
        <UiField label="角色" hint="角色为单选；套餐绑定了角色时，更换套餐会自动覆盖此处设置">
          <select v-model="rolesForm.code" class="input">
            <option value="">（无角色）</option>
            <option v-for="r in allRoles" :key="r.code" :value="r.code">
              {{ r.name }}（{{ r.code }}）{{ r.status !== 1 ? ' — 已禁用' : '' }}
            </option>
          </select>
        </UiField>
        <div class="flex items-center gap-2">
          <span class="text-text-muted text-sm">当前角色：</span>
          <UiTag v-if="rolesUser && (rolesUser.roles || []).length" :tone="roleTone(rolesForm.code)" :label="currentRoleLabel" />
          <span v-else class="text-text-muted text-sm">无</span>
        </div>
      </form>
    </UiModal>

    <!-- 重置密码 -->
    <UiModal v-model:open="resetPwdOpen" :title="'重置密码 #' + (resetPwdUser && resetPwdUser.id)" @confirm="saveResetPwd" :confirm-loading="submitting" confirm-text="重置">
      <form @submit.prevent="saveResetPwd" class="space-y-5">
        <UiField label="新密码" hint="8–16 位，包含字母和数字">
          <input v-model="resetPwdForm.new_password" type="password" minlength="8" maxlength="16" required class="input">
        </UiField>
      </form>
    </UiModal>

    <!-- 额度覆盖 -->
    <UiModal v-model:open="quotaOpen" :title="'额度覆盖 — ' + (quotaUser && quotaUser.username)" wide @confirm="quotaOpen = false">
      <div v-if="quotaUser" class="overflow-x-auto">
        <div class="text-sm text-text-muted mb-4">
          套餐：{{ quotaUser.plan_name || '免费版' }}（{{ quotaUser.plan_code || '—' }}）
          <span class="ml-2 text-xs">覆盖值在计划额度上增减（可为负），留空或 0 表示不调整</span>
          <router-link to="/admin/quotas" class="ml-2 text-xs text-text-muted hover:text-text transition-colors">计划额度本身在「额度 · 套餐」页改 ↗</router-link>
        </div>
        <table class="table-base w-full">
          <thead>
            <tr><th>数据源</th><th>计划额度（剩余）</th><th>用户覆盖</th><th>生效额度</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="(item, i) in quotaItems" :key="item.source_code">
              <td class="text-xs whitespace-nowrap">{{ item.source_name }}</td>
              <td class="font-mono text-xs">
                <template v-if="item.plan_limit != null && item.plan_limit >= 0">
                  <span class="font-medium">{{ fmtQuota(Math.max(item.plan_limit - (item.used || 0), 0)) }}</span>
                  <span class="text-text-muted"> / {{ fmtQuota(item.plan_limit) }}</span>
                </template>
                <span v-else>不限</span>
              </td>
              <td>
                <div class="flex items-center gap-1">
                  <input v-if="editingQuota === item.source_code"
                    v-model.number="quotaEditForm.limit" type="number" class="input input-sm w-20 font-mono"
                    placeholder="0">
                  <span v-else class="font-mono text-xs">{{ item.user_limit != null ? fmtQuota(item.user_limit) : '0' }}</span>
                </div>
              </td>
              <td class="font-mono text-xs font-medium">{{ fmtQuota(item.effective) }}</td>
              <td>
                <template v-if="editingQuota === item.source_code">
                  <button class="text-xs text-pale-green-fg hover:opacity-70 mr-2" @click="saveQuota(item)">保存</button>
                  <button class="text-xs text-text-muted hover:text-text" @click="editingQuota = null">取消</button>
                </template>
                <template v-else>
                  <button class="text-xs text-text-muted hover:text-text" @click="startEditQuota(item)">编辑</button>
                  <button v-if="item.user_limit != null" class="text-xs text-pale-red-fg hover:opacity-70 ml-2" @click="clearQuota(item)">清除覆盖</button>
                </template>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </UiModal>

    <!-- 删除确认 -->
    <UiModal v-model:open="deleteOpen" :title="'删除用户 #' + (deleteTarget && deleteTarget.id)" @confirm="confirmDelete" :confirm-loading="submitting" confirm-text="删除">
      <p class="text-text-muted text-sm">此操作将软删除用户 <span class="font-mono text-text">{{ deleteTarget && deleteTarget.username }}</span>，该账号将无法再登录，其用户名与邮箱不可再注册。确定继续？</p>
    </UiModal>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, nextTick } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiModal from '../../components/UiModal.vue'
import UiTag from '../../components/UiTag.vue'
import UiField from '../../components/UiField.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import UiPagination from '../../components/UiPagination.vue'
import { adminApi } from '../../api/index.js'
import { fmtDate, roleTone, statusTone, toast, revealObserve } from '../../utils.js'

const loading = ref(true)
const error = ref('')
const list = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const keyword = ref('')
const withDeleted = ref(false)
const submitting = ref(false)
const allRoles = ref([])
const allPlans = ref([])
const createOpen = ref(false)
const createForm = ref({ username: '', password: '', nickname: '', email: '', role_code: 'user', plan_id: null })
const editOpen = ref(false)
const editUser = ref(null)
const editForm = ref({ nickname: '', email: '', status: 1, plan_id: null })
const rolesOpen = ref(false)
const rolesUser = ref(null)
const rolesForm = ref({ code: '' })
const resetPwdOpen = ref(false)
const resetPwdUser = ref(null)
const resetPwdForm = ref({ new_password: '' })
const deleteOpen = ref(false)
const deleteTarget = ref(null)
const quotaOpen = ref(false)
const quotaUser = ref(null)
const quotaItems = ref([])
const editingQuota = ref(null)
const quotaEditForm = ref({ limit: 0 })

const doSearch = () => { page.value = 1; load() }
const goPage = (p) => { page.value = p; load() }

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    const data = await adminApi.listUsers({ page: page.value, keyword: keyword.value.trim(), withDeleted: withDeleted.value })
    list.value = data.list || []
    total.value = data.total || 0
    pageSize.value = data.page_size || 20
  } catch (e) {
    error.value = e.message
  }
  loading.value = false
  nextTick(revealObserve)
}

const ensureMeta = async () => {
  const tasks = []
  if (!allRoles.value.length) tasks.push(adminApi.listRoles().then(r => { allRoles.value = r || [] }))
  if (!allPlans.value.length) tasks.push(adminApi.listPlans().then(p => { allPlans.value = p || [] }))
  try { await Promise.all(tasks) } catch (e) { toast(e.message, 'error') }
}

// 所选套餐绑定的角色（新建用户时锁定角色选择，与后端「套餐绑定优先」语义一致）
const planBoundRole = computed(() => {
  const p = allPlans.value.find(p => p.id === createForm.value.plan_id)
  if (!p || !p.role_id) return null
  return allRoles.value.find(r => r.id === p.role_id) || null
})

watch(() => createForm.value.plan_id, () => {
  if (planBoundRole.value) createForm.value.role_code = planBoundRole.value.code
})

// 套餐下拉选项文案（含绑定角色提示）
const planLabel = (p) => {
  const role = p.role_id ? allRoles.value.find(r => r.id === p.role_id) : null
  return p.name + '（' + p.code + '）' + (role ? ' · 角色 ' + (role.name || role.code) : '')
}

const currentRoleLabel = computed(() => {
  const r = allRoles.value.find(r => r.code === rolesForm.value.code)
  return r ? (r.name || r.code) : (rolesForm.value.code || '无')
})

const openCreate = async () => {
  await ensureMeta()
  createForm.value = { username: '', password: '', nickname: '', email: '', role_code: 'user', plan_id: null }
  createOpen.value = true
}

const saveCreate = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    const f = createForm.value
    await adminApi.createUser({
      username: f.username.trim(),
      password: f.password,
      nickname: f.nickname.trim() || undefined,
      email: f.email.trim() || undefined,
      role_code: f.role_code,
      plan_id: f.plan_id || undefined,
    })
    toast('用户已创建', 'success')
    createOpen.value = false
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const openEdit = async (u) => {
  await ensureMeta()
  try {
    editUser.value = await adminApi.getUser(u.id)
    editForm.value = {
      nickname: editUser.value.nickname || '',
      email: editUser.value.email || '',
      status: editUser.value.status,
      plan_id: editUser.value.plan_id || null,
    }
    editOpen.value = true
  } catch (e) { toast(e.message, 'error') }
}

const saveEdit = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    const id = editUser.value.id
    const oldPlan = editUser.value.plan_id || null
    // PATCH 语义：只提交改过的字段——历史行邮箱为空时，整体提交会带上空串被后端拒掉
    const payload = {}
    if (editForm.value.nickname !== (editUser.value.nickname || '')) payload.nickname = editForm.value.nickname
    if (editForm.value.email !== (editUser.value.email || '')) payload.email = editForm.value.email
    if (editForm.value.status !== editUser.value.status) payload.status = editForm.value.status
    if (Object.keys(payload).length) await adminApi.updateUser(id, payload)
    if (editForm.value.plan_id !== oldPlan) {
      await adminApi.updateUserPlan(id, editForm.value.plan_id)
    }
    toast('已更新', 'success')
    editOpen.value = false
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const openRoles = async (u) => {
  await ensureMeta()
  try {
    const user = await adminApi.getUser(u.id)
    rolesUser.value = user
    rolesForm.value.code = (user.roles || [])[0]?.code || ''
    rolesOpen.value = true
  } catch (e) { toast(e.message, 'error') }
}

const saveRoles = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.updateUserRoles(rolesUser.value.id, rolesForm.value.code)
    toast('角色已更新', 'success')
    rolesOpen.value = false
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const openResetPwd = (u) => {
  resetPwdUser.value = u
  resetPwdForm.value = { new_password: '' }
  resetPwdOpen.value = true
}

const saveResetPwd = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.resetUserPassword(resetPwdUser.value.id, resetPwdForm.value.new_password)
    toast('密码已重置', 'success')
    resetPwdOpen.value = false
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const confirmDelete = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.deleteUser(deleteTarget.value.id)
    toast('已删除', 'success')
    deleteOpen.value = false
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const confirmRestore = async (u) => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.restoreUser(u.id)
    toast('已恢复', 'success')
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const fmtQuota = (n) => {
  if (n < 0) return '不限'
  return String(n)
}

const openQuota = async (u) => {
  // u 可能是用户行（字段 id）或上次的额度响应（字段 user_id），统一取 id，
  // 且合并保留到 quotaUser，避免保存/清除后刷新时拼出 /admin/users/undefined/quota
  const uid = u.id || u.user_id
  editingQuota.value = null
  try {
    const data = await adminApi.getUserQuota(uid)
    quotaUser.value = { ...data, id: uid }
    quotaItems.value = data.items || []
    quotaOpen.value = true
  } catch (e) { toast(e.message, 'error') }
}

const startEditQuota = (item) => {
  editingQuota.value = item.source_code
  // 覆盖为空默认为 0（不调整）
  quotaEditForm.value.limit = item.user_limit != null ? item.user_limit : 0
}

const saveQuota = async (item) => {
  if (submitting.value) return
  submitting.value = true
  try {
    const raw = quotaEditForm.value.limit
    const limit = (raw === '' || raw == null || isNaN(raw)) ? 0 : raw
    await adminApi.updateUserQuota(quotaUser.value.user_id, [{ group_code: item.source_code, limit }])
    toast('已更新', 'success')
    editingQuota.value = null
    await openQuota(quotaUser.value)
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const clearQuota = async (item) => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.updateUserQuota(quotaUser.value.user_id, [{ group_code: item.source_code, limit: 0 }])
    toast('覆盖已清除', 'success')
    await openQuota(quotaUser.value)
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

onMounted(() => { revealObserve() })
load()
</script>
