<template>
  <div>
    <PageHeader title="用户管理" subtitle="所有注册用户及其角色与套餐。">
      <template #actions>
        <button class="btn-primary" @click="openCreate">新建用户</button>
      </template>
    </PageHeader>

    <!-- 筛选级计数：这一页只报「这次筛选有多宽」，站点级数字在「管理后台」那四张看板 -->
    <div class="reveal font-mono text-xs text-text-muted mb-4">
      当前列表命中 {{ total }} 条<span v-if="filterCount">（{{ filterCount }} 个筛选条件）</span><span v-else>（未筛选）</span>
    </div>

    <div class="reveal flex flex-col sm:flex-row gap-3 mb-3">
      <input v-model="keyword" placeholder="搜索用户名 / 邮箱 / 昵称" class="input flex-1" @keydown.enter="apply">
      <button @click="apply" class="btn-ghost whitespace-nowrap">搜索</button>
      <button @click="resetFilters" class="btn-ghost whitespace-nowrap" :disabled="!hasAnyFilter">清空筛选</button>
    </div>

    <!-- 九个筛选条件收在折叠块里（P30 的窄屏纪律：收起态给读数摘要，不是藏信息）。
         条件是即时生效的：下拉改完还要再点一次「搜索」，那是把「没生效」做成常态。 -->
    <UiCollapse title="筛选条件" :summary="filterSummary" storage-key="admin-users-filters">
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
        <UiField v-for="f in STATIC_FILTERS" :key="f.key" :label="f.label" :hint="f.hint">
          <select v-model="filters[f.key]" class="input" @change="apply">
            <option v-for="o in f.options" :key="o.value" :value="o.value">{{ o.label }}</option>
          </select>
        </UiField>
        <UiField label="套餐" hint="按当前在册的套餐列；「已下架」那一档捞的是绑着看不见套餐的人">
          <select v-model="filters.plan" class="input" @change="apply">
            <option value="">全部</option>
            <option v-for="p in allPlans" :key="p.id" :value="String(p.id)">{{ p.name }}（{{ p.code }}）</option>
            <option value="unavailable">已下架的套餐</option>
          </select>
        </UiField>
        <UiField label="角色">
          <select v-model="filters.role" class="input" @change="apply">
            <option value="">全部</option>
            <option v-for="r in allRoles" :key="r.code" :value="r.code">{{ r.name }}（{{ r.code }}）</option>
          </select>
        </UiField>
        <UiField v-if="filters.expire === 'expiring'" label="到期窗口（天）" hint="默认 7，超出 1–365 会被夹住">
          <input v-model="filters.expireDays" type="number" min="1" max="365" class="input" @change="apply">
        </UiField>
        <UiField label="会话数超上限">
          <label class="flex items-center gap-1.5 text-sm cursor-pointer select-none pt-1.5">
            <input type="checkbox" v-model="filters.overCap" @change="apply" class="w-4 h-4 rounded border-border text-text focus:ring-text">
            只看活跃会话超过上限的人
          </label>
        </UiField>
      </div>
    </UiCollapse>

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
              <UiTag v-if="u.plan" tone="blue" :label="u.plan.name || u.plan.code" /><span v-if="u.plan && u.plan.alias" class="text-xs text-text-muted ml-1">（本人别名：{{ u.plan.alias }}）</span><span v-else-if="!u.plan" class="text-text-muted">免费版</span>
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
                <span v-if="u.plan && u.plan.alias" class="text-xs text-text-muted">（本人别名：{{ u.plan.alias }}）</span>
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
          套餐：{{ quotaUser.plan_name || '免费版' }}（{{ quotaUser.plan_code || '—' }}）<span v-if="quotaUser.plan_alias" class="ml-1">— 本人别名：{{ quotaUser.plan_alias }}</span>
          <span class="ml-2 text-xs">覆盖值在计划额度上增减（可为负），留空或 0 表示不调整；
            标「无权限」的行是该套餐没授权的源（授权=限额行，P34），改它不会让用户能用上这个源</span>
          <router-link to="/admin/quotas" class="ml-2 text-xs text-text-muted hover:text-text transition-colors">计划额度本身在「额度 · 套餐」页改 ↗</router-link>
          <!-- 单日额度刷新：动的是用量**起算点**，不是流水也不是限额（待办清单 P41）。
               起算点必须显示出来，否则「已用 0」会被读成「今天还没用」；管理员一眼看到它是几点。 -->
          <div class="mt-3 flex flex-col sm:flex-row sm:items-center gap-2">
            <span class="text-xs text-text-muted font-mono">
              当日已用自 {{ fmtDate(quotaUser.usage_since) }} 起算
              <span v-if="quotaUser.quota_reset_at">（{{ fmtDate(quotaUser.quota_reset_at) }} 刷新过）</span>
              <span v-else>（零点口径，未刷新过）</span>
            </span>
            <button @click="refreshQuota" class="btn-ghost btn-sm self-start sm:self-auto whitespace-nowrap" :disabled="submitting">刷新额度</button>
          </div>
        </div>
        <table class="table-base w-full">
          <thead>
            <tr><th>数据源</th><th>计划额度（剩余）</th><th>用户覆盖</th><th>生效额度</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="(item, i) in quotaItems" :key="item.source_code" :class="item.granted ? '' : 'opacity-60'">
              <td class="text-xs whitespace-nowrap">
                {{ item.source_name }}
                <UiTag v-if="!item.granted" tone="gray" label="无权限" />
              </td>
              <td class="font-mono text-xs">
                <span v-if="!item.granted" class="text-text-muted">—</span>
                <template v-else-if="item.plan_limit != null && item.plan_limit >= 0">
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
              <td class="font-mono text-xs font-medium">{{ item.granted ? fmtQuota(item.effective) : '—' }}</td>
              <td>
                <template v-if="!item.granted">
                  <button v-if="item.user_limit != null" class="text-xs text-pale-red-fg hover:opacity-70" @click="clearQuota(item)">清除覆盖</button>
                  <span v-else class="text-text-muted text-xs">套餐未授权该源，覆盖不参与生效</span>
                </template>
                <template v-else-if="editingQuota === item.source_code">
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
import { useRoute, useRouter } from 'vue-router'
import PageHeader from '../../components/PageHeader.vue'
import UiModal from '../../components/UiModal.vue'
import UiTag from '../../components/UiTag.vue'
import UiField from '../../components/UiField.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import UiPagination from '../../components/UiPagination.vue'
import UiCollapse from '../../components/UiCollapse.vue'
import { adminApi } from '../../api/index.js'
import { fmtDate, roleTone, statusTone, toast, revealObserve } from '../../utils.js'

const route = useRoute()
const router = useRouter()

const loading = ref(true)
const error = ref('')
const list = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const keyword = ref(typeof route.query.keyword === 'string' ? route.query.keyword : '')
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

// 静态筛选项的选项表。文案只在这里写一遍：折叠块的收起态摘要复用同一份 label，
// 摘要另抄一份就会长成第二份事实来源（P28 那两条看板各说一套的同一件事）。
const manyApiKeys = ref(0) // 「偏多」的档位由 /admin/users 的 filters_meta 下发，面板不自己写数字
const STATIC_FILTERS = computed(() => [
  { key: 'status', label: '账号状态', options: [
    { value: '', label: '全部' }, { value: 'normal', label: '启用' },
    { value: 'disabled', label: '禁用' }, { value: 'deleted', label: '已删除' },
  ] },
  { key: 'expire', label: '套餐到期', options: [
    { value: '', label: '全部' }, { value: 'permanent', label: '永久（未设到期）' },
    { value: 'expiring', label: '即将到期' }, { value: 'active', label: '在有效期内' },
    { value: 'expired', label: '已过期' },
  ] },
  { key: 'activity', label: '活跃度', hint: '「从未登录」单独一档，不并进「不活跃」', options: [
    { value: '', label: '全部' }, { value: 'never', label: '从未登录' },
    { value: '7d', label: '7 日内登录过' }, { value: '30d', label: '30 日内登录过' },
    { value: '90d', label: '90 日内登录过' }, { value: 'stale', label: '90 天以上未登录' },
  ] },
  { key: 'consent', label: '阅读数据留存', hint: '未表态 = 从未点过开关，默认按同意处理', options: [
    { value: '', label: '全部' }, { value: 'unset', label: '未表态' },
    { value: 'on', label: '明确同意' }, { value: 'off', label: '明确关闭' },
  ] },
  { key: 'quotaReset', label: '单日额度刷新', hint: '刷过之后「当日限额」对这个人当天不再构成约束', options: [
    { value: '', label: '全部' }, { value: 'any', label: '刷新过（不限时间）' },
    { value: 'recent', label: '统计窗口内刷新过' },
  ] },
  { key: 'keys', label: 'API 密钥数', options: [
    { value: '', label: '全部' }, { value: 'none', label: '没有密钥' },
    { value: 'any', label: '至少一把' },
    { value: 'many', label: manyApiKeys.value ? '≥ ' + manyApiKeys.value + ' 把（批量发 key 的形态）' : '偏多（档位随读数下发）' },
  ] },
])

// query 参数名与后端一致，页面字段名是 camelCase——两张表对起来只在这一个地方翻
const QUERY_KEYS = {
  status: 'status', plan: 'plan', role: 'role', expire: 'expire', expireDays: 'expire_days',
  activity: 'activity', consent: 'consent', quotaReset: 'quota_reset', keys: 'keys',
}

const emptyFilters = () => ({
  status: '', plan: '', role: '', expire: '', expireDays: '',
  activity: '', consent: '', quotaReset: '', keys: '', overCap: false,
})

// 初始值直接读 URL：这一页要能被「复制链接给同事」分享，而首屏 load() 在 setup 末尾就跑，
// 等 onMounted 再回填会先拉一次未筛选的列表
const filtersFromUrl = () => {
  const f = emptyFilters()
  for (const [k, qk] of Object.entries(QUERY_KEYS)) {
    const v = route.query[qk]
    if (typeof v === 'string' && v) f[k] = v
  }
  f.overCap = route.query.over_cap === '1'
  return f
}
const filters = ref(filtersFromUrl())
if (typeof route.query.page === 'string' && /^\d+$/.test(route.query.page)) {
  page.value = Math.max(1, parseInt(route.query.page, 10) || 1)
}

const queryOf = () => {
  const q = {}
  const kw = keyword.value.trim()
  if (kw) q.keyword = kw
  for (const [k, qk] of Object.entries(QUERY_KEYS)) {
    const v = filters.value[k]
    // 到期窗口是「即将到期」的修饰参数，切走这一档就不留在 URL 里
    if (v && !(k === 'expireDays' && filters.value.expire !== 'expiring')) q[qk] = v
  }
  if (filters.value.overCap) q.over_cap = '1'
  if (page.value > 1) q.page = String(page.value)
  return q
}

// 同一份 query 重复 replace 会被 vue-router 判成「冗余导航」而 reject；这一页每次 load 都同步一次，
// 首屏尤其容易原样不动——不让它变成控制台里的一条红
const syncUrl = () => { router.replace({ query: queryOf() }).catch(() => {}) }

const filterCount = computed(() => {
  let n = 0
  if (keyword.value.trim()) n++
  for (const f of STATIC_FILTERS.value) if (filters.value[f.key]) n++
  if (filters.value.plan) n++
  if (filters.value.role) n++
  if (filters.value.overCap) n++
  return n
})
const hasAnyFilter = computed(() => filterCount.value > 0)

const filterSummary = computed(() => {
  const parts = []
  const kw = keyword.value.trim()
  if (kw) parts.push('关键词 ' + kw)
  for (const f of STATIC_FILTERS.value) {
    const v = filters.value[f.key]
    if (!v) continue
    const hit = f.options.find(o => o.value === v)
    parts.push(f.label + ' ' + (hit ? hit.label : v))
  }
  if (filters.value.plan) {
    const p = allPlans.value.find(x => String(x.id) === filters.value.plan)
    parts.push('套餐 ' + (p ? p.name : '已下架的套餐'))
  }
  if (filters.value.role) {
    const r = allRoles.value.find(x => x.code === filters.value.role)
    parts.push('角色 ' + (r ? (r.name || r.code) : filters.value.role))
  }
  if (filters.value.overCap) parts.push('会话数超上限')
  return parts.length ? parts.join(' · ') : '未筛选（全部用户）'
})

const apply = () => { page.value = 1; load() }
const goPage = (p) => { page.value = p; load() }
const resetFilters = () => {
  keyword.value = ''
  filters.value = emptyFilters()
  page.value = 1
  load()
}

const load = async () => {
  loading.value = true
  error.value = ''
  syncUrl()
  try {
    const f = filters.value
    const data = await adminApi.listUsers({
      page: page.value, keyword: keyword.value.trim(),
      status: f.status, plan: f.plan, role: f.role,
      expire: f.expire, expireDays: f.expireDays, activity: f.activity,
      consent: f.consent, quotaReset: f.quotaReset, keys: f.keys, overCap: f.overCap,
    })
    list.value = data.list || []
    total.value = data.total || 0
    pageSize.value = data.page_size || 20
    const meta = (data.filters_meta || {})
    if (meta.many_api_keys) manyApiKeys.value = meta.many_api_keys
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

// 刷新单日额度：确认后重开弹窗，让「已用」列与起算点一起跟着新读数变。
// 提示里带上「刷掉了多少」——只说「已刷新」不说明刚才那些消耗去哪了。
const refreshQuota = async () => {
  if (submitting.value || !quotaUser.value) return
  const uid = quotaUser.value.id
  if (!uid) { toast('拿不到用户 ID，请关闭后重开额度弹窗', 'error'); return }
  submitting.value = true
  try {
    const r = await adminApi.refreshUserQuota(uid)
    toast('已刷新：' + (r && r.used_before != null ? r.used_before : '?') + ' 点当日消耗不再计入，起算点改为 '
      + fmtDate(r && r.usage_since) + '（流水未删）', 'success')
    submitting.value = false
    await openQuota(quotaUser.value)
  } catch (e) {
    submitting.value = false
    toast(e.message, 'error')
  }
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

onMounted(() => {
  revealObserve()
  // 筛选下拉要一屏就能选：套餐/角色清单不能在点开新建弹窗时才拉
  ensureMeta()
})
load()
</script>
