<template>
  <div>
    <PageHeader title="个人中心" subtitle="账户资料、安全、API 密钥与接入。" />

    <!-- 身份概览：原来散在「基本信息」只读卡里的用户名/角色/套餐/到期/注册/最近登录，
         收成一张门面卡——进来第一眼就知道"我是谁、什么档、什么时候到期"，
         下面的卡片只放可编辑项与操作，不再重复展示只读值。 -->
    <div class="card reveal mb-8 md:mb-10 flex items-center gap-4 md:gap-6">
      <div class="w-14 h-14 md:w-16 md:h-16 rounded-full bg-surface-alt border border-border flex items-center justify-center font-serif text-2xl md:text-3xl shrink-0 select-none">{{ initial }}</div>
      <div class="min-w-0 flex-1">
        <div class="flex flex-wrap items-center gap-x-2 gap-y-1.5 mb-1.5">
          <span class="font-serif text-2xl md:text-3xl font-medium tracking-tight leading-tight">{{ displayName }}</span>
          <UiTag v-for="r in (me.roles || [])" :key="r.code" :tone="roleTone(r.code)" :label="r.display_name || r.name || r.code" />
          <UiTag v-if="me.plan" tone="blue" :label="me.plan.display_name || me.plan.name || me.plan.code" />
          <span v-else class="text-xs text-text-muted">免费版</span>
        </div>
        <div class="font-mono text-xs md:text-sm text-text-muted truncate">
          @{{ me.username }} · 注册于 {{ fmtDate(me.created_at) }} · 最近登录 {{ fmtDate(me.last_login_at) }}
        </div>
        <div v-if="me.plan_expire_at" class="mt-1.5 font-mono text-xs" :style="expireSoon ? 'color:#956400' : 'color:#787774'">
          套餐到期 {{ fmtDate(me.plan_expire_at) }}<template v-if="expireSoon">（即将到期）</template>
        </div>
      </div>
    </div>

    <!-- 账户资料 + 修改密码：两块都是短表单，并排高度对齐 -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-8 md:gap-10 items-start">
      <section class="reveal">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">账户资料</h2>
        <form @submit.prevent="onSaveProfile" class="space-y-5">
          <UiField label="用户名" :hint="usernameHint">
            <input v-model="profileForm.username" class="input font-mono" minlength="3" maxlength="64" :disabled="!!usernameNextAt" placeholder="3-64 个字符">
          </UiField>
          <UiField label="昵称">
            <input v-model="profileForm.nickname" class="input" maxlength="64" placeholder="未设置">
          </UiField>
          <UiField label="邮箱" hint="必须填写">
            <input v-model="profileForm.email" type="email" class="input" placeholder="未设置" required>
          </UiField>
          <UiField label="登录有效时长" hint="单位：小时；0 表示跟随系统默认，-1 表示永不过期，对下次及以后的登录生效">
            <input v-model.number="profileForm.token_expire_hours" type="number" min="-1" max="8760" class="input font-mono" placeholder="0 = 跟随系统默认，-1 = 永不过期">
          </UiField>
          <button type="submit" class="btn-primary" :disabled="savingProfile">{{ savingProfile ? '保存中' : '保存资料' }}</button>
        </form>
      </section>

      <!-- 修改密码 -->
      <section class="reveal">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">修改密码</h2>
        <form @submit.prevent="onChangePwd" class="space-y-5">
          <UiField label="当前密码">
            <input v-model="pwdForm.old_password" type="password" class="input" autocomplete="current-password" required>
          </UiField>
          <UiField label="新密码" hint="8–16 位，包含字母和数字">
            <input v-model="pwdForm.new_password" type="password" class="input" minlength="8" maxlength="16" autocomplete="new-password" required>
          </UiField>
          <button type="submit" class="btn-primary" :disabled="savingPwd">{{ savingPwd ? '提交中' : '更新密码' }}</button>
        </form>
      </section>
    </div>

    <!-- API 密钥 + 登录设备：两块都是列表型，并排便于对照"谁在用我的账号" -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-8 md:gap-10 items-start mt-12 md:mt-16">
      <!-- API 密钥 -->
      <section class="reveal">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">API 密钥</h2>
        <div v-if="keys.length" class="text-sm text-text-muted mb-4">用于书源或脚本以程序化方式调用数据源接口：请求头带 <code class="font-mono">X-API-Key</code> 或参数 <code class="font-mono">?api_key=</code>。调用按你的账号计费与限流。密钥可随时在列表中查看，泄露请立即撤销。</div>
        <div class="flex justify-end mb-4">
          <button class="btn-primary btn-sm" :disabled="keys.length >= 10" @click="createKeyOpen = true">{{ keys.length >= 10 ? '已达上限（10）' : '创建密钥' }}</button>
        </div>
        <UiSpinner v-if="keysLoading" />
        <UiEmpty v-else-if="keys.length === 0" title="还没有 API 密钥" />
        <div v-else class="card !p-0 divide-y divide-border">
          <div v-for="k in keys" :key="k.id" class="flex items-center justify-between gap-3 px-5 md:px-6 py-4">
            <div class="min-w-0">
              <div class="flex items-center gap-2 mb-1">
                <span class="font-medium text-sm">{{ k.name }}</span>
                <button class="text-xs text-text-muted hover:opacity-70 transition-opacity" @click="copyText(k.key)">复制</button>
              </div>
              <div class="font-mono text-xs break-all select-all">{{ k.key }}</div>
              <div class="font-mono text-xs text-text-muted mt-1">
                创建于 {{ fmtDate(k.created_at) }} · 最后使用 {{ k.last_used_at ? fmtDate(k.last_used_at) : '从未' }}
              </div>
            </div>
            <button class="text-xs text-pale-red-fg hover:opacity-70 transition-opacity whitespace-nowrap" @click="onRevokeKey(k)">撤销</button>
          </div>
        </div>
      </section>

      <!-- 登录设备 -->
      <section class="reveal">
        <div class="flex items-end justify-between mb-5 pb-3 border-b border-border">
          <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight">登录设备</h2>
          <button v-if="otherCount > 0" class="btn-danger btn-sm" :disabled="revokingOthers" @click="revokeOthersOpen = true">退出其他设备</button>
        </div>
        <UiSpinner v-if="sessionsLoading" />
        <UiEmpty v-else-if="sessions.length === 0" title="暂无登录设备" />
        <div v-else class="card !p-0 divide-y divide-border">
          <div v-for="s in sessions" :key="s.id" class="flex items-center justify-between gap-3 px-5 md:px-6 py-4">
            <div class="min-w-0">
              <div class="flex items-center gap-2 mb-1">
                <span class="font-medium text-sm">{{ s.device }}</span>
                <UiTag v-if="s.current" tone="green" label="当前设备" />
              </div>
              <div class="font-mono text-xs text-text-muted">
                {{ s.ip || '未知 IP' }} · 最后活跃 {{ fmtDate(s.last_active_at) }} · 登录于 {{ fmtDate(s.created_at) }}
              </div>
            </div>
            <button v-if="!s.current" class="text-xs text-pale-red-fg hover:opacity-70 transition-opacity whitespace-nowrap" @click="onRevoke(s)">退出</button>
          </div>
        </div>
      </section>
    </div>

    <!-- 偏好与接入：留存 / 别名(可选) / 书源 / 兑换 四张轻量卡共用一个两列网格自然流动。
         别名卡 v-if 不渲染时后面的卡自动补位，不会在页面中间留一个整列空白；
         无别名共 3 张卡，最后一张（兑换）跨整列，收尾不再留右列空洞。 -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-8 md:gap-10 items-start mt-12 md:mt-16">
      <!-- 隐私协议：阅读数据留存的同意位。唯一写入口 POST /auth/privacy（只认会话） -->
      <section class="reveal">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">阅读数据留存</h2>
        <p class="text-sm text-text-muted leading-relaxed mb-4">
          网关会记录你调用接口的<strong>搜索词</strong>与<strong>阅读记录</strong>（书名、章节、媒介），
          这些内容只用于生成公开排行榜。榜上只出现书名与搜索词本身，
          不会出现你的用户名、邮箱、IP 或任何其它身份信息。
        </p>
        <label class="flex items-start gap-2 mb-4">
          <input type="checkbox" v-model="privacyForm.content_consent" class="checkbox mt-0.5">
          <span class="text-sm">同意网关留存上述内容（默认同意，可随时关闭）</span>
        </label>
        <p class="text-xs text-text-muted leading-relaxed mb-4">
          关闭后，新的调用不再捕获搜索词与阅读记录，公开排行榜也不再计入你的阅读行为；
          已经记录的历史明细不会追溯删除。
        </p>
        <div class="flex items-center gap-3">
          <button type="button" class="btn-primary" :disabled="savingPrivacy" @click="onSavePrivacy">
            {{ savingPrivacy ? '保存中' : '保存设置' }}
          </button>
          <span v-if="privacySavedAt" class="text-xs text-text-muted">已保存</span>
        </div>
      </section>

      <!-- 显示别名（待办清单 P43）：只改「你看到的称呼」。默认名在 quota_plans/roles 两张全局表里，
           这里一概不动；管理员视图同时显示默认名与别名，所以别名不会把你的真实档位藏起来。 -->
      <section v-if="canAlias" class="reveal">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">显示名称</h2>
        <p class="text-sm text-text-muted leading-relaxed mb-4">
          给你的套餐或角色起一个只影响<strong>你自己界面</strong>的称呼。
          管理员看到的仍是默认名（并标注你的别名），公开排行榜与其它用户都不受影响。
        </p>
        <div class="space-y-3">
          <div v-for="t in aliasTargets" :key="t.kind + ':' + t.id" class="flex flex-col sm:flex-row sm:items-center gap-2">
            <span class="text-sm text-text-muted sm:w-16 shrink-0">{{ t.kind === 'plan' ? '套餐名' : '角色名' }}</span>
            <input v-model="t.draft" class="input flex-1" maxlength="32" :placeholder="'默认：' + t.name">
            <button type="button" class="btn-ghost btn-sm whitespace-nowrap" :disabled="savingAlias" @click="saveAlias(t)">保存</button>
            <button v-if="t.alias" type="button" class="btn-ghost btn-sm whitespace-nowrap" :disabled="savingAlias" @click="clearAlias(t)">清除</button>
          </div>
        </div>
        <p class="text-xs text-text-muted mt-3">最长 32 个字符；清除后回到默认名。换套餐时上一个套餐的称呼不会跟过来。</p>
      </section>
      <section class="reveal">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">阅读客户端</h2>
        <div class="card">
          <div class="text-sm text-text-muted mb-4">在安装了「阅读」App 的设备上，点击下方按钮将自动拉起 App 并导入书源。若未拉起，可复制直链到 App 内手动导入（网络导入/粘贴）。</div>
          <div class="flex gap-3">
            <button class="btn-primary" :disabled="!bookSourceReady" @click="onImportLegado">导入书源</button>
            <button class="btn-ghost" :disabled="!bookSourceReady" @click="copyImportUrl">复制直链</button>
          </div>
          <div v-if="!bookSourceReady" class="text-xs text-text-muted mt-3">书源文件尚未就位（由部署侧托管在 <span class="font-mono">/data/shuyuan/bookSource.json</span>）</div>
          <div v-else class="text-xs text-text-muted mt-3 font-mono break-all">{{ bookSourceUrl }}</div>
        </div>
      </section>

      <!-- 无别名时本格是 3 张卡里的最后一张，跨整列收尾；有别名时 4 张卡两两并排 -->
      <section class="reveal" :class="canAlias ? '' : 'lg:col-span-2'">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">套餐升级</h2>
        <div class="card">
          <div class="text-sm text-text-muted mb-4">输入卡密兑换套餐或续费。同套餐兑换自动叠加时长。</div>
          <form @submit.prevent="onRedeem" class="flex flex-col sm:flex-row gap-3">
            <input v-model="redeemCode" class="input flex-1 font-mono" placeholder="XXXX-XXXX-XXXX-XXXX" maxlength="19" required>
            <button type="submit" class="btn-primary whitespace-nowrap" :disabled="redeeming">{{ redeeming ? '兑换中' : '兑换' }}</button>
          </form>
        </div>
      </section>
    </div>

    <!-- 「额度清零时间」这一格按待办清单 P119 ① 从个人中心摘掉（面板不再给切换入口）。
         服务端能力原样留着：POST /auth/quota-cycle 与 /auth/me 的 quota_cycle_* 三个字段不动——
         **已经切到 subscription 的人继续按其注册钟点清零**，这是 A 档的定案理由（回落等于在
         用户不知情时改他的额度窗口）。要重新给入口时，本节的历史形状在 2026-10-10 之前的 git 里。 -->

    <!-- 数据源 BaseURL 配置：默认折叠（待办清单 P119 ②，维护者点名这一格挡视线）。
         折叠只是省滚动、不是藏信息——收起态那行摘要给的是读数（几个源、几个已自定义、有没有未保存改动），
         不是"暂无"这种形容词（判据正本在 UiCollapse.vue 顶部那两条设计约束）。
         保存按钮留在展开态里：dirty 只可能在展开之后发生，所以收起态不会有"改了却没处保存"。 -->
    <div class="mt-12 md:mt-16">
      <UiCollapse title="数据源地址" :summary="sourceConfigSummary" storage-key="profile-source-config">
        <div class="flex items-end justify-between mb-4 gap-3">
          <p class="text-xs text-text-muted leading-relaxed">留空即使用该源的平台默认地址；改动要按「保存配置」才写入。</p>
          <button v-if="sourceConfigDirty" class="btn-primary btn-sm shrink-0" :disabled="savingSourceConfig" @click="onSaveSourceConfig">保存配置</button>
        </div>
        <UiSpinner v-if="sourceConfigs === null" />
        <div v-else class="space-y-3">
          <div v-for="cfg in sourceConfigs" :key="cfg.source_name" class="card !p-4 flex flex-col sm:flex-row sm:items-center gap-3">
            <div class="font-mono text-sm min-w-0 flex-1 sm:flex-none sm:w-[140px] truncate">{{ cfg.source_display }}</div>
            <div class="font-mono text-xs text-text-muted sm:min-w-[100px]">{{ cfg.source_name }}</div>
            <input v-model="cfg.base_url" class="input min-w-0 flex-1 font-mono text-sm" placeholder="留空则使用平台默认地址" @input="sourceConfigDirty = true">
          </div>
        </div>
      </UiCollapse>
    </div>

    <!-- 退出其他设备确认 -->
    <UiModal :open="revokeOthersOpen" title="退出其他设备" @close="revokeOthersOpen = false" @confirm="onRevokeOthers" confirm-text="全部退出" :confirm-loading="revokingOthers">
      <p class="text-text-muted text-sm">将退出除当前设备外的全部 {{ otherCount }} 个登录会话，其他设备上的书源与控制台访问会立即失效。确定继续？</p>
    </UiModal>
    <!-- 创建 API 密钥 -->
    <UiModal :open="createKeyOpen" title="创建 API 密钥" confirm-text="创建" :confirm-loading="creatingKey" @close="createKeyOpen = false" @confirm="onCreateKey">
      <UiField label="备注名" hint="便于识别用途，如「书源-手机」">
        <input v-model="newKeyName" class="input" maxlength="64" placeholder="default" @keyup.enter="onCreateKey">
      </UiField>
    </UiModal>

    <!-- 密钥明文（仅一次） -->
    <UiModal :open="!!newKeyPlain" title="密钥已创建" confirm-text="我已保存" @close="newKeyPlain = ''" @confirm="newKeyPlain = ''">
      <p class="text-text-muted text-sm mb-3">密钥已创建（可随时在下方列表查看或复制）：</p>
      <div class="card !p-3 font-mono text-xs break-all select-all">{{ newKeyPlain }}</div>
      <button class="btn-ghost btn-sm mt-3" @click="copyText(newKeyPlain)">复制密钥</button>
    </UiModal>

    <!-- 撤销 API 密钥确认 -->
    <UiModal :open="!!revokeKeyTarget" title="撤销 API 密钥" @close="revokeKeyTarget = null" @confirm="onRevokeKeyConfirm" confirm-text="撤销" :confirm-loading="revokingKey">
      <p class="text-text-muted text-sm">撤销后使用该密钥的请求将立即失效（{{ revokeKeyTarget ? revokeKeyTarget.prefix + '……' : '' }}）。确定继续？</p>
    </UiModal>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, nextTick } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import UiField from '../components/UiField.vue'
import UiTag from '../components/UiTag.vue'
import UiModal from '../components/UiModal.vue'
import UiSpinner from '../components/UiSpinner.vue'
import UiEmpty from '../components/UiEmpty.vue'
import UiCollapse from '../components/UiCollapse.vue'
import { authApi, userConfigApi, apikeyApi, clearToken } from '../api/index.js'
import { session } from '../store.js'
import { fmtDate, roleTone, toast, revealObserve } from '../utils.js'

const me = ref({})
const profileForm = ref({ username: '', nickname: '', email: '', token_expire_hours: 0 })

// 身份概览卡的显示名与首字母头像：与顶栏 displayName 同一口径（昵称优先，否则用户名）
const displayName = computed(() => me.value.nickname || me.value.username || '')
const initial = computed(() => (displayName.value.trim() || '?').charAt(0).toUpperCase())

// 隐私协议的同意位：后端给的是**有效值**（NULL 已折算成同意），所以这里只管 true/false；
// 老会话缓存里没这个键时读成 undefined，`!== false` 按默认档（同意）走
const privacyForm = ref({ content_consent: true })
const savingPrivacy = ref(false)
// 显示别名（待办清单 P43）：资格判据与后端一致——绑定了非免费套餐才能改
const savingAlias = ref(false)
const aliasTargets = ref([])
const canAlias = computed(() => !!me.value.plan && me.value.plan.code !== 'free')
// targets 从 me 的 plan/roles 派生：别名按 (kind, target_id) 存，换套餐后上一份不会被带进来
const buildAliasTargets = () => {
  const out = []
  if (me.value.plan && me.value.plan.code !== 'free') {
    out.push({ kind: 'plan', id: me.value.plan.id, name: me.value.plan.name,
      alias: me.value.plan.alias || '', draft: me.value.plan.alias || '' })
  }
  for (const r of (me.value.roles || [])) {
    out.push({ kind: 'role', id: r.id, name: r.name, alias: r.alias || '', draft: r.alias || '' })
  }
  aliasTargets.value = out
}
const refreshMe = async () => {
  me.value = await authApi.me()
  session.user = me.value
  buildAliasTargets()
}
const saveAlias = async (t) => {
  if (savingAlias.value) return
  savingAlias.value = true
  try {
    await authApi.updateDisplayAlias(t.kind, t.id, t.draft.trim())
    await refreshMe()
    toast('显示名称已保存', 'success')
  } catch (e) { toast(e.message, 'error') } finally { savingAlias.value = false }
}
const clearAlias = async (t) => {
  if (savingAlias.value) return
  savingAlias.value = true
  try {
    await authApi.updateDisplayAlias(t.kind, t.id, '')
    await refreshMe()
    toast('已回到默认名称', 'success')
  } catch (e) { toast(e.message, 'error') } finally { savingAlias.value = false }
}
const privacySavedAt = ref('')
const pwdForm = ref({ old_password: '', new_password: '' })
const savingProfile = ref(false)
const savingPwd = ref(false)
const sessions = ref([])
const sessionsLoading = ref(true)
const revokingOthers = ref(false)
const revokeOthersOpen = ref(false)
const sourceConfigs = ref(null)
const sourceConfigDirty = ref(false)
// 收起态摘要：读数而不是形容词（0 个已自定义也要说出来——"一个都没配"本身就是信息）
const sourceConfigSummary = computed(() => {
  const list = sourceConfigs.value
  if (list === null) return '加载中'
  const custom = list.filter((c) => (c.base_url || '').trim() !== '').length
  const dirty = sourceConfigDirty.value ? '，有未保存改动' : ''
  return `共 ${list.length} 个源，已自定义 ${custom} 个${dirty}`
})
const savingSourceConfig = ref(false)
// 书源导入：绝对地址由当前站点拼（同源即用户真实到达的域），就绪与否由后端看文件
const bookSourceUrl = ref('')
const bookSourceReady = ref(false)
const redeemCode = ref('')
const redeeming = ref(false)
const keys = ref([])
const keysLoading = ref(true)
const createKeyOpen = ref(false)
const newKeyName = ref('')
const creatingKey = ref(false)
const newKeyPlain = ref('')
const revokeKeyTarget = ref(null)
const revokingKey = ref(false)

const otherCount = computed(() => sessions.value.filter(s => !s.current).length)

// 套餐临期（3 天内）提示
const expireSoon = computed(() => {
  if (!me.value.plan_expire_at) return false
  return new Date(me.value.plan_expire_at) - Date.now() < 3 * 24 * 3600 * 1000
})

// 用户名修改冷却：每 30 天限改一次，返回下次可修改时间（null 表示当前可改）
const usernameNextAt = computed(() => {
  if (!me.value.username_changed_at) return null
  const next = new Date(me.value.username_changed_at).getTime() + 30 * 24 * 3600 * 1000
  return next > Date.now() ? new Date(next) : null
})
const usernameHint = computed(() => {
  return usernameNextAt.value
    ? '每 30 天只能修改一次，下次可修改：' + fmtDate(usernameNextAt.value)
    : '每 30 天只能修改一次，修改后立即生效'
})

const onRedeem = async () => {
  if (redeeming.value) return
  redeeming.value = true
  try {
    const data = await userConfigApi.redeem(redeemCode.value.trim())
    toast('兑换成功：' + data.plan_name + (data.expire_at ? '，' + fmtDate(data.expire_at) + ' 到期' : '，永久有效'), 'success')
    redeemCode.value = ''
    // 刷新会话信息以更新套餐展示
    try { me.value = await authApi.me(); session.user = me.value } catch (e) { /* 忽略 */ }
  } catch (err) {
    toast(err.message, 'error')
  } finally {
    redeeming.value = false
  }
}

onMounted(() => { revealObserve() })

const load = async () => {
  try {
    me.value = session.user || {}
    buildAliasTargets()
    profileForm.value = { username: me.value.username || '', nickname: me.value.nickname || '', email: me.value.email || '', token_expire_hours: me.value.token_expire_hours || 0 }
    privacyForm.value.content_consent = me.value.content_consent !== false
  } catch (e) { /* 401 已由客户端处理 */ }
  nextTick(revealObserve)
}


const onSaveProfile = async () => {
  if (savingProfile.value) return
  if (!profileForm.value.email || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(profileForm.value.email)) {
    toast('请输入有效的邮箱地址', 'error')
    return
  }
  savingProfile.value = true
  try {
    const oldUsername = (me.value.username || '').toLowerCase()
    const data = await authApi.updateMe({
      username: profileForm.value.username.trim(),
      nickname: profileForm.value.nickname.trim(),
      email: profileForm.value.email.trim(),
      token_expire_hours: Number(profileForm.value.token_expire_hours) || 0,
    })
    // 用户名即登录凭证：变更后后端已吊销全部会话，本地登出并引导重新登录
    if (data.username && data.username.toLowerCase() !== oldUsername) {
      toast('用户名已修改，请使用新用户名重新登录', 'success')
      clearToken()
      session.user = null
      setTimeout(() => { location.href = '/panel/login' }, 800)
      return
    }
    me.value = data
    session.user = me.value
    toast('资料已保存', 'success')
  } catch (err) {
    toast(err.message, 'error')
  } finally {
    savingProfile.value = false
  }
}

// 同意位单独一条端点：它不是「资料」的一部分，语义是本人对网关的授权，
// 而且 /auth/me 必须带邮箱——把两件事捆在一起，改一个开关要顺带重写邮箱
const onSavePrivacy = async () => {
  if (savingPrivacy.value) return
  savingPrivacy.value = true
  try {
    const data = await authApi.updatePrivacy(privacyForm.value.content_consent)
    privacyForm.value.content_consent = data.content_consent !== false
    me.value = { ...me.value, content_consent: privacyForm.value.content_consent }
    session.user = me.value
    privacySavedAt.value = new Date().toISOString()
    toast(privacyForm.value.content_consent ? '已同意留存阅读数据' : '已关闭留存，新的调用不再记录搜索词与阅读记录', 'success')
  } catch (err) {
    // 失败要把开关拨回服务端的事实，不能让用户以为已经生效
    privacyForm.value.content_consent = me.value.content_consent !== false
    toast(err.message, 'error')
  } finally {
    savingPrivacy.value = false
  }
}

const loadSessions = async () => {
  sessionsLoading.value = true
  try {
    sessions.value = (await authApi.listSessions()) || []
  } catch (e) { /* 加载失败不阻断页面 */ }
  sessionsLoading.value = false
  nextTick(revealObserve)
}

const onRevoke = async (s) => {
  try {
    await authApi.revokeSession(s.id)
    toast('已退出该设备', 'success')
    loadSessions()
  } catch (err) {
    toast(err.message, 'error')
  }
}

const onRevokeOthers = async () => {
  if (revokingOthers.value) return
  revokingOthers.value = true
  try {
    const data = await authApi.revokeOtherSessions()
    toast('已退出 ' + (data.count || 0) + ' 个设备', 'success')
    revokeOthersOpen.value = false
    loadSessions()
  } catch (err) {
    toast(err.message, 'error')
  } finally {
    revokingOthers.value = false
  }
}

const loadKeys = async () => {
  keysLoading.value = true
  try {
    keys.value = (await apikeyApi.list()) || []
  } catch (e) { /* 加载失败不阻断页面 */ }
  keysLoading.value = false
  nextTick(revealObserve)
}

const onCreateKey = async () => {
  if (creatingKey.value) return
  creatingKey.value = true
  try {
    const data = await apikeyApi.create(newKeyName.value.trim())
    newKeyPlain.value = data.key
    createKeyOpen.value = false
    newKeyName.value = ''
    loadKeys()
  } catch (err) {
    toast(err.message, 'error')
  } finally {
    creatingKey.value = false
  }
}

const copyText = async (text) => {
  try {
    await navigator.clipboard.writeText(text)
    toast('已复制', 'success')
  } catch (e) {
    toast('复制失败，请手动选择复制', 'error')
  }
}

const onRevokeKey = (k) => { revokeKeyTarget.value = k }

const onRevokeKeyConfirm = async () => {
  if (revokingKey.value || !revokeKeyTarget.value) return
  revokingKey.value = true
  try {
    await apikeyApi.revoke(revokeKeyTarget.value.id)
    toast('密钥已撤销，相关请求立即失效', 'success')
    revokeKeyTarget.value = null
    loadKeys()
  } catch (err) {
    toast(err.message, 'error')
  } finally {
    revokingKey.value = false
  }
}

const loadSourceConfigs = async () => {
  try {
    sourceConfigs.value = await userConfigApi.listSourceConfigs()
    sourceConfigDirty.value = false
  } catch (e) {
    sourceConfigs.value = []
  }
}

const onSaveSourceConfig = async () => {
  if (savingSourceConfig.value) return
  savingSourceConfig.value = true
  try {
    await userConfigApi.updateSourceConfigs(sourceConfigs.value)
    toast('数据源地址已保存', 'success')
    sourceConfigDirty.value = false
  } catch (err) {
    toast(err.message, 'error')
  } finally {
    savingSourceConfig.value = false
  }
}

const onChangePwd = async () => {
  if (savingPwd.value) return
  savingPwd.value = true
  try {
    await authApi.changePassword(pwdForm.value.old_password, pwdForm.value.new_password)
    toast('密码已更新', 'success')
    pwdForm.value = { old_password: '', new_password: '' }
  } catch (err) {
    toast(err.message, 'error')
  } finally {
    savingPwd.value = false
  }
}

const loadImportConfig = async () => {
  try {
    const data = await userConfigApi.getImportConfig()
    const path = data.book_source_path || ''
    bookSourceUrl.value = path ? window.location.origin + path : ''
    bookSourceReady.value = !!data.ready && !!bookSourceUrl.value
  } catch (e) { /* 取不到就按未就位处理 */ }
}

const onImportLegado = () => {
  if (!bookSourceReady.value) return
  window.location.href = 'legado://import/auto?src=' + encodeURIComponent(bookSourceUrl.value)
}

const copyImportUrl = async () => {
  if (!bookSourceReady.value) return
  try {
    await navigator.clipboard.writeText(bookSourceUrl.value)
    toast('直链已复制，可到阅读 App 内手动导入', 'success')
  } catch (e) {
    toast('复制失败，请长按链接手动复制', 'error')
  }
}

load()
loadSessions()
loadKeys()
loadSourceConfigs()
loadImportConfig()
</script>
