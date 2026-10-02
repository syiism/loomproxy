<template>
  <div>
    <PageHeader title="系统设置" subtitle="站点运行时配置，按功能聚合为卡片；卡内保存只提交有改动的项。">
      <template #actions>
        <button class="btn-primary" @click="openCreate">新增配置</button>
      </template>
    </PageHeader>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <UiEmpty v-else-if="list.length === 0" title="暂无设置项" />
    <div v-else class="grid grid-cols-1 md:grid-cols-2 gap-4 md:gap-6 items-start">

      <!-- 功能卡片 -->
      <section v-for="g in GROUPS" :key="g.title" class="card reveal">
        <div class="mb-5">
          <div class="font-serif text-lg font-medium tracking-tight">{{ g.title }}</div>
          <div v-if="g.desc" class="text-xs text-text-muted mt-1">{{ g.desc }}</div>
        </div>
        <form @submit.prevent="saveGroup(g)" class="space-y-5">
          <template v-for="f in g.fields" :key="f.key">
            <!-- 数据库中不存在的受管 key（如被误删）：置灰提示，经「新增配置」恢复 -->
            <UiField v-if="!settingsMap[f.key]" :label="f.label">
              <input class="input font-mono" disabled :value="'未入库（用「新增配置」恢复 ' + f.key + '）'">
            </UiField>
            <UiField v-else :label="f.label" :hint="settingsMap[f.key].description">
              <UiSwitch v-if="f.widget === 'switch'" v-model="form[f.key]" />
              <select v-else-if="f.widget === 'select'" v-model="form[f.key]" class="input font-mono">
                <option v-for="opt in f.options" :key="opt" :value="opt">{{ opt }}</option>
              </select>
              <textarea
                v-else-if="f.widget === 'textarea'"
                v-model="form[f.key]"
                class="input font-mono leading-relaxed"
                :rows="f.rows || 3"
                :disabled="fieldDisabled(f)"
              ></textarea>
              <!-- 榜单可见性**只留一个编辑口**：数据源管理页的逐源开关。这里给只读摘要 + 跳转——
                   同页再放一份「整份名单一次提交」的复选组就会与逐源增量互相覆盖（拿旧快照点保存
                   会静默冲掉别处刚改的源），且候选只在打开那一刻取一次，新源不刷新就不出现（P17） -->
              <div v-else-if="f.widget === 'sources_ro'" class="flex flex-wrap items-center gap-x-3 gap-y-2">
                <span class="text-sm">
                  已放行 <span class="font-mono">{{ sourceList(f.key).length }}</span> 个源<span v-if="sourceList(f.key).length" class="text-text-muted">：{{ sourceNames(sourceList(f.key)) }}</span>
                </span>
                <button type="button" class="btn-ghost btn-sm" @click="$router.push('/admin/datasources')">到「数据源管理」逐源开关</button>
              </div>
              <template v-else-if="f.widget === 'json'">
                <textarea
                  v-model="form[f.key]"
                  class="input font-mono leading-relaxed"
                  :rows="f.rows || 5"
                  :disabled="fieldDisabled(f)"
                  spellcheck="false"
                  @blur="checkJson(f.key)"
                ></textarea>
                <div class="mt-2 flex items-center gap-2">
                  <button type="button" class="btn-ghost btn-sm" :disabled="fieldDisabled(f)" @click="checkJson(f.key)">校验</button>
                  <button type="button" class="btn-ghost btn-sm" :disabled="fieldDisabled(f)" @click="formatJson(f.key)">格式化</button>
                  <span v-if="jsonState[f.key]" class="min-w-0 flex-1 truncate text-xs"
                        :class="jsonState[f.key].ok ? 'text-pale-green-fg' : 'text-pale-red-fg'"
                        :title="jsonState[f.key].msg">{{ jsonState[f.key].msg }}</span>
                </div>
              </template>
              <input
                v-else
                v-model="form[f.key]"
                :type="f.widget === 'number' ? 'number' : 'text'"
                :min="f.min"
                class="input font-mono"
                :disabled="fieldDisabled(f)"
              >
            </UiField>
          </template>

          <div class="pt-4 border-t border-border flex items-center justify-between">
            <span class="text-xs text-text-muted">
              {{ dirtyKeys(g).length > 0 ? `待保存 ${dirtyKeys(g).length} 项` : '无改动' }}
            </span>
            <button
              type="submit"
              class="btn-primary btn-sm"
              :disabled="dirtyKeys(g).length === 0 || savingGroup === g.title"
            >{{ savingGroup === g.title ? '保存中' : '保存' }}</button>
          </div>
        </form>
      </section>

      <!-- 自定义配置兜底卡：不受卡片管理的 key -->
      <section v-if="customEntries.length > 0" class="card reveal md:col-span-2">
        <div class="mb-4">
          <div class="font-serif text-lg font-medium tracking-tight">自定义配置</div>
          <div class="text-xs text-text-muted mt-1">未被上方功能卡片管理的设置项（含后续版本新增、未识别的 key）。</div>
        </div>
        <div class="divide-y divide-border">
          <div v-for="s in customEntries" :key="s.key" class="flex flex-wrap items-center gap-3 md:gap-4 py-3 first:pt-0 last:pb-0">
            <div class="min-w-0 flex-1">
              <div class="font-mono text-xs break-all">{{ s.key }}</div>
              <div class="mt-1 max-w-[480px] truncate text-xs text-text-muted" :title="s.description">{{ s.description || '—' }}</div>
            </div>
            <div class="shrink-0 max-w-[200px] truncate font-mono text-xs" :title="s.value">
              <UiTag v-if="s.type === 'bool'" :tone="s.value === 'true' ? 'green' : 'red'" :label="s.value" />
              <template v-else>{{ s.value }}</template>
            </div>
            <div class="shrink-0 flex gap-3">
              <button @click="openEdit(s)" class="text-xs text-text-muted hover:text-text transition-colors">编辑</button>
              <button v-if="!isProtected(s)" @click="deleteTarget = s" class="text-xs text-pale-red-fg hover:opacity-70 transition-opacity">删除</button>
            </div>
          </div>
        </div>
      </section>
    </div>

    <!-- 新建配置 -->
    <UiModal :open="createOpen" title="新增配置" @close="createOpen = false" @confirm="saveCreate" :confirm-loading="submitting">
      <form @submit.prevent="saveCreate" class="space-y-5">
        <UiField label="键" hint="创建后不可修改">
          <input v-model="createForm.key" class="input font-mono" maxlength="64" required>
        </UiField>
        <UiField label="类型">
          <select v-model="createForm.type" class="input">
            <option value="string">string</option>
            <option value="bool">bool</option>
            <option value="number">number</option>
            <option value="json">json</option>
          </select>
        </UiField>
        <UiField label="值">
          <select v-if="createForm.type === 'bool'" v-model="createForm.value" class="input">
            <option value="true">true</option>
            <option value="false">false</option>
          </select>
          <textarea v-else-if="createForm.type === 'json'" v-model="createForm.value"
                    class="input font-mono leading-relaxed" rows="4" spellcheck="false"></textarea>
          <input v-else v-model="createForm.value" class="input font-mono" required>
        </UiField>
        <UiField label="说明">
          <input v-model="createForm.description" class="input" maxlength="255">
        </UiField>
      </form>
    </UiModal>

    <!-- 编辑值（自定义配置） -->
    <UiModal :open="!!editItem" :title="'编辑 ' + (editItem && editItem.key)" @close="editItem = null" @confirm="saveEdit" :confirm-loading="submitting">
      <form v-if="editItem" @submit.prevent="saveEdit" class="space-y-5">
        <UiField label="值">
          <select v-if="editItem.type === 'bool'" v-model="editForm.value" class="input">
            <option value="true">true</option>
            <option value="false">false</option>
          </select>
          <textarea v-else-if="editItem.type === 'json'" v-model="editForm.value"
                    class="input font-mono leading-relaxed" rows="5" spellcheck="false"></textarea>
          <input v-else v-model="editForm.value" class="input font-mono" required>
        </UiField>
        <div v-if="editItem.description" class="text-xs text-text-muted">{{ editItem.description }}</div>
      </form>
    </UiModal>

    <!-- 删除确认 -->
    <UiModal :open="!!deleteTarget" title="删除配置" @close="deleteTarget = null" @confirm="confirmDelete">
      <p class="text-text-muted text-sm">删除 <span class="font-mono">{{ deleteTarget && deleteTarget.key }}</span> 后，读取该配置的功能将回退到默认值。确定继续？</p>
    </UiModal>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, nextTick } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiModal from '../../components/UiModal.vue'
import UiTag from '../../components/UiTag.vue'
import UiField from '../../components/UiField.vue'
import UiSwitch from '../../components/UiSwitch.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import { adminApi, miscApi } from '../../api/index.js'
import { toast, revealObserve } from '../../utils.js'

const PROTECTED = ['register_enabled', 'default_role', 'default_quota_plan']

// 受管字段分组：渲染顺序即卡片顺序；field 的中文说明沿用后端 description，不在前端重复维护
const GROUPS = [
  {
    title: '站点基础',
    desc: '站点身份与对用户可见的公告、书源入口。',
    fields: [
      { key: 'site_name', label: '站点名称', widget: 'text' },
      { key: 'maintenance_mode', label: '维护模式', widget: 'switch' },
      { key: 'announcement', label: '站内公告', widget: 'textarea', rows: 4 },
      { key: 'rank_public_sources', label: '可见榜单的数据源', widget: 'sources_ro' },
    ],
  },
  {
    title: '注册与登录',
    desc: '新用户注册开关、默认身份与登录 token 有效期。',
    fields: [
      { key: 'register_enabled', label: '允许注册', widget: 'switch' },
      { key: 'default_role', label: '新用户默认角色', widget: 'text' },
      { key: 'default_quota_plan', label: '新用户默认套餐', widget: 'text' },
      { key: 'jwt_expire_hours', label: 'Token 有效时长（小时）', widget: 'number' },
    ],
  },
  {
    title: '验证码',
    desc: '场景化验证码策略：默认全部场景关闭，启用后注册/找回密码需携带验证码。',
    fields: [
      { key: 'verify_code_scenes', label: '启用场景', widget: 'text' },
      { key: 'verify_send_interval_sec', label: '发送冷却（秒）', widget: 'number', min: 0 },
      { key: 'verify_daily_send_limit', label: '每日发送上限', widget: 'number', min: 0 },
      { key: 'verify_code_ttl_sec', label: '验证码有效期（秒）', widget: 'number', min: 0 },
      { key: 'verify_code_max_attempts', label: '单码最大失败次数', widget: 'number', min: 0 },
    ],
  },
  {
    title: '发码通道',
    desc: 'verify_provider 切换 mock / http；http 为通用模板适配器，绝大多数发码平台填配置即可对接。',
    fields: [
      { key: 'verify_provider', label: '通道', widget: 'select', options: ['mock', 'http'] },
      { key: 'verify_http_url', label: '请求地址', widget: 'textarea', rows: 2, gate: 'providerHttp' },
      { key: 'verify_http_method', label: '请求方法', widget: 'text', gate: 'providerHttp' },
      { key: 'verify_http_headers', label: '请求头（JSON）', widget: 'json', rows: 4, gate: 'providerHttp' },
      { key: 'verify_http_body', label: '请求体模板（JSON）', widget: 'json', rows: 6, gate: 'providerHttp' },
      { key: 'verify_http_success_keyword', label: '成功判定关键字', widget: 'text', gate: 'providerHttp' },
    ],
  },
  {
    title: '代理与 IP 防护',
    desc: '上游代理池的作用范围与 IP 自动拉黑阈值。',
    fields: [
      { key: 'proxy_enabled_sources', label: '走代理的数据源', widget: 'text' },
      { key: 'auto_block_enabled', label: 'IP 自动拉黑', widget: 'switch' },
      { key: 'auto_block_threshold', label: '拉黑阈值（次）', widget: 'number', min: 1 },
      { key: 'auto_block_window_sec', label: '统计窗口（秒）', widget: 'number', min: 1 },
    ],
  },
]

const MANAGED_KEYS = new Set(GROUPS.flatMap(g => g.fields.map(f => f.key)))

const loading = ref(true)
const error = ref('')
const list = ref([])
const submitting = ref(false)
const createOpen = ref(false)
const createForm = ref({ key: '', type: 'string', value: '', description: '' })
const editItem = ref(null)
const editForm = ref({ value: '' })
const deleteTarget = ref(null)

// 卡片级编辑态：form 为当前值（switch 存布尔、其余存字符串），snapshot 为加载时的字符串原值
const settingsMap = ref({})
const form = ref({})
const snapshot = ref({})
const savingGroup = ref('')

const isProtected = (s) => PROTECTED.includes(s.key)

// 不受卡片管理的 key 进自定义配置卡（含未来 seed 新增而前端未识别的）
const customEntries = computed(() => list.value.filter(s => !MANAGED_KEYS.has(s.key)))

// gate: 'providerHttp' 表示仅 http 通道可编辑（mock 下置灰）
const fieldDisabled = (f) => f.gate === 'providerHttp' && form.value.verify_provider === 'mock'

// sources_ro widget：库里存的是逗号分隔名单，这里只读展示（编辑在数据源管理页，见 P17）
const sourceChoices = ref([])
const sourceList = (key) => String(form.value[key] ?? '').split(',').map(x => x.trim()).filter(Boolean)
// 源码翻展示名；认不出的（停用/已删的源）原样带出源码而不是藏掉
const sourceNames = (codes, max = 8) => {
  const names = codes.map(c => {
    const hit = sourceChoices.value.find(o => o.code === c)
    return hit && hit.name && hit.name !== c ? hit.name : c
  })
  return names.length > max ? names.slice(0, max).join('、') + ` 等 ${names.length} 个` : names.join('、')
}

const loadSourceChoices = async () => {
  try {
    const list = await miscApi.datasources()
    sourceChoices.value = (list || []).map(d => ({ code: d.id, name: d.name || d.id }))
  } catch (e) { /* 拉不到候选时仍可手改库值，不阻塞设置页 */ }
}

// json widget：本地即时校验（后端同样会校验并压缩，这里只是让坏值当场可见）
const jsonState = ref({})

const checkJson = (key) => {
  const raw = String(form.value[key] ?? '').trim()
  if (!raw) { jsonState.value = { ...jsonState.value, [key]: { ok: true, msg: '空值合法（该能力未启用）' } }; return true }
  try {
    const parsed = JSON.parse(raw)
    const compact = JSON.stringify(parsed)
    jsonState.value = { ...jsonState.value, [key]: { ok: true, msg: `合法 JSON（保存时压缩为单行 ${compact.length} 字节）` } }
    return true
  } catch (e) {
    jsonState.value = { ...jsonState.value, [key]: { ok: false, msg: '非法 JSON：' + e.message } }
    return false
  }
}

const formatJson = (key) => {
  const raw = String(form.value[key] ?? '').trim()
  if (!raw) return
  try {
    form.value[key] = JSON.stringify(JSON.parse(raw), null, 2)
    checkJson(key)
  } catch (e) {
    jsonState.value = { ...jsonState.value, [key]: { ok: false, msg: '无法格式化：' + e.message } }
  }
}

// 表单值 → 落库字符串（bool 转 'true'/'false'，文本 trim）
const serialize = (v) => (typeof v === 'boolean' ? String(v) : String(v ?? '').trim())

const dirtyKeys = (g) => g.fields.filter(f => settingsMap.value[f.key] && serialize(form.value[f.key]) !== snapshot.value[f.key]).map(f => f.key)

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    list.value = (await adminApi.listSettings()) || []
    const map = {}
    const f = {}
    const snap = {}
    for (const s of list.value) {
      map[s.key] = s
      if (MANAGED_KEYS.has(s.key)) {
        f[s.key] = s.type === 'bool' ? s.value === 'true' : s.value
        snap[s.key] = s.value
      }
    }
    settingsMap.value = map
    form.value = f
    snapshot.value = snap
    jsonState.value = {}
  } catch (e) {
    error.value = e.message
  }
  loading.value = false
  nextTick(revealObserve)
}

// 卡片级保存：只提交该卡有改动的 key（后端逐 key PUT，空值合法）
const saveGroup = async (g) => {
  if (savingGroup.value) return
  const changes = g.fields.filter(f => settingsMap.value[f.key] && serialize(form.value[f.key]) !== snapshot.value[f.key])
  if (changes.length === 0) return
  // JSON 型先本地过一遍：坏值不发请求，免得整卡只有这一项被后端拒掉后界面与库对不上
  for (const f of changes.filter(f => f.widget === 'json')) {
    if (!checkJson(f.key)) { toast(`「${f.label}」不是合法 JSON，已取消保存`, 'error'); return }
  }
  savingGroup.value = g.title
  try {
    for (const f of changes) {
      const res = await adminApi.updateSetting(f.key, serialize(form.value[f.key]))
      // 后端对 JSON 型会压缩落库：回填压缩值，界面显示的即为库里的实际内容
      if (f.widget === 'json' && res && typeof res.value === 'string') form.value[f.key] = res.value
    }
    for (const f of changes) {
      snapshot.value[f.key] = serialize(form.value[f.key])
    }
    toast(`已保存 ${changes.length} 项配置`, 'success')
  } catch (e) {
    toast(e.message, 'error')
    // 逐 key 提交中途失败可能部分已保存：重新拉取对齐界面与库
    load()
  } finally {
    savingGroup.value = ''
  }
}

const openCreate = () => {
  createForm.value = { key: '', type: 'string', value: '', description: '' }
  createOpen.value = true
}

const saveCreate = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.createSetting({
      key: createForm.value.key.trim(),
      value: createForm.value.value,
      type: createForm.value.type,
      description: createForm.value.description.trim(),
    })
    toast('配置已创建', 'success')
    createOpen.value = false
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const openEdit = (s) => {
  editItem.value = s
  editForm.value = { value: s.value }
}

const saveEdit = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.updateSetting(editItem.value.key, editForm.value.value)
    toast('已更新', 'success')
    editItem.value = null
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const confirmDelete = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.deleteSetting(deleteTarget.value.key)
    toast('已删除', 'success')
    deleteTarget.value = null
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

onMounted(() => { load(); loadSourceChoices(); revealObserve() })
load()
</script>
