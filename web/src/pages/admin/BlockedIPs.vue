<template>
  <div>
    <PageHeader title="IP 拉黑" subtitle="被拉黑的 IP 无法登录、注册，也无法调用任何接口；下方另列当前的登录限频锁定（进程内存，与黑名单是两件事）。" />

    <div class="reveal flex flex-col sm:flex-row gap-3 mb-6">
      <input v-model="form.ip" placeholder="IP 地址（IPv4 / IPv6）" class="input sm:w-64 font-mono" @keydown.enter="add">
      <input v-model="form.note" placeholder="备注（可选）" class="input flex-1" @keydown.enter="add">
      <button @click="add" class="btn-primary whitespace-nowrap" :disabled="submitting">拉黑</button>
    </div>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <UiEmpty v-else-if="list.length === 0" title="黑名单为空" text="添加 IP 后，该地址的所有请求将被拒绝（403）。" />
    <template v-else>
      <div class="table-wrap reveal overflow-x-auto">
        <table class="table-base">
          <thead>
            <tr><th>ID</th><th>IP</th><th>来源</th><th>备注</th><th>拉黑时间</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="b in list" :key="b.id">
              <td class="font-mono text-xs">{{ b.id }}</td>
              <td class="font-mono text-sm">{{ b.ip }}</td>
              <td><UiTag :tone="b.source === 'auto' ? 'yellow' : 'gray'" :label="b.source === 'auto' ? '自动' : '手动'" /></td>
              <td class="text-text-muted max-w-[240px] truncate">{{ b.note || '—' }}</td>
              <td class="font-mono text-xs text-text-muted whitespace-nowrap">{{ fmtDate(b.created_at) }}</td>
              <td>
                <button @click="remove(b)" class="text-xs text-pale-red-fg hover:opacity-70 transition-opacity" :disabled="submitting">解除</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- 登录限频锁定（待办清单 P40）：与上面那张表是两件事。
         上面是库里的黑名单（拦所有请求、删行才放行），这里是进程内存里的限频锁（重启即清零）。
         所以「解锁」不解黑名单，文案必须把这句写在按钮旁边而不是藏在说明里。 -->
    <div class="mt-10">
      <div class="flex items-baseline justify-between gap-3 mb-2">
        <h2 class="text-sm font-semibold text-text">登录限频锁定</h2>
        <button @click="loadAttempts" class="text-xs text-pale-blue-fg hover:opacity-70 transition-opacity" :disabled="attemptsLoading">刷新</button>
      </div>
      <p v-if="attemptsError" class="text-sm text-pale-red-fg">{{ attemptsError }}</p>
      <p v-else-if="attempts.length === 0" class="text-sm text-text-muted">
        当前没有锁定记录。读数只反映这个进程：重启即清零，也不代表「最近没人试过」。
      </p>
      <div v-else class="table-wrap overflow-x-auto">
        <table class="table-base">
          <thead>
            <tr><th>通道</th><th>IP</th><th>窗口内尝试</th><th class="hidden md:table-cell">失败连击</th><th>状态</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="a in attempts" :key="a.kind + a.ip">
              <td class="text-sm">{{ a.kind === 'forgot' ? '找回密码' : '登录' }}</td>
              <td class="font-mono text-sm">{{ a.ip }}</td>
              <td class="font-mono text-xs">{{ a.window_count }} / {{ a.max_per_minute }}</td>
              <td class="hidden md:table-cell font-mono text-xs">{{ a.fail_streak }}</td>
              <td>
                <UiTag v-if="a.locked" tone="red" :label="'锁到 ' + fmtClock(a.locked_until)" />
                <span v-else class="text-xs text-text-muted">未锁（计数中）</span>
              </td>
              <td>
                <button @click="unlock(a)" class="text-xs text-pale-red-fg hover:opacity-70 transition-opacity" :disabled="submitting">解锁</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 账号侧登录尝试（待办清单 P40① 要的那份材料）：**只读观察，一行处置都不做**。
         它存在的意义是把「一个账号被多少个不同出口试过」说出来——上面那道按 IP 的闸看不见这个形状
         （现网 14 天：536 次失败来自 222 个 IP，单 IP 最多 14 次，全部低于阈值）。
         刻意不写"疑似攻击"：一个用户连错三次与一个脚本各错一次，在这一列里长得一样，判定是人做的。 -->
    <div class="mt-10">
      <div class="flex items-baseline justify-between gap-3 mb-2">
        <h2 class="text-sm font-semibold text-text">账号侧登录尝试（只读观察）</h2>
        <button @click="loadAttempts" class="text-xs text-pale-blue-fg hover:opacity-70 transition-opacity" :disabled="attemptsLoading">刷新</button>
      </div>
      <p v-if="accounts.length === 0" class="text-sm text-text-muted">
        当前没有登录尝试的记录。读数只反映这个进程（重启即清零），保留窗口是一小时。
      </p>
      <div v-else class="table-wrap overflow-x-auto">
        <table class="table-base">
          <thead>
            <tr><th>登录标识</th><th>尝试</th><th>失败</th><th>不同 IP 数</th><th class="hidden md:table-cell">来源样本</th></tr>
          </thead>
          <tbody>
            <tr v-for="a in accounts" :key="a.principal">
              <td class="font-mono text-sm">{{ a.principal }}</td>
              <td class="font-mono text-xs">{{ a.attempts }}</td>
              <td class="font-mono text-xs">{{ a.fails }}</td>
              <td>
                <UiTag v-if="isMultiIp(a)" tone="yellow" :label="'多出口 ' + a.distinct_ips" />
                <span v-else class="font-mono text-xs">{{ a.distinct_ips }}</span>
              </td>
              <td class="hidden md:table-cell font-mono text-xs text-text-muted">{{ (a.sample_ips || []).join(' ') }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="text-xs text-text-muted mt-2">
        这一列不做任何处置：账号侧要不要独立闸门还没拍（待办清单 P40①），这里只把判定要用的材料摆出来。
        「不同 IP 数」标黄只说"这个标识来自多个出口"，不说"有人在爆破"。
      </p>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, nextTick } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiTag from '../../components/UiTag.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import { adminApi } from '../../api/index.js'
import { fmtDate, toast, revealObserve } from '../../utils.js'

const loading = ref(true)
const error = ref('')
const list = ref([])
const submitting = ref(false)
const form = ref({ ip: '', note: '' })

const attempts = ref([])
const attemptsLoading = ref(false)
const attemptsError = ref('')
// 账号侧观察与上面那份同一次请求带回（同一端点、同一个刷新按钮），但它**没有操作列**：只读
const accounts = ref([])
// 标黄阈值由后端下发（它复用「设备与密钥」那一页同一条 `suspect_distinct_ips` 定义）。
// 这里不写死数字：改了设置而面板文案还指着旧数，就是自己长出第二份事实（待办清单 P46 的判据）。
// 取不到时不标黄而不是猜一个默认值——宁可不标，也不标错。
const accountsMeta = ref({})
const isMultiIp = (a) => {
  const n = Number(accountsMeta.value?.multi_ip_yellow)
  return Number.isFinite(n) && n > 0 && Number(a.distinct_ips) >= n
}

// 锁定到期时间只到分钟：面板上要的是「还要等多久」，不是秒
const fmtClock = (iso) => {
  const d = new Date(iso)
  if (isNaN(d)) return '—'
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

const loadAttempts = async () => {
  attemptsLoading.value = true
  attemptsError.value = ''
  try {
    const data = await adminApi.listSecurityAttempts()
    attempts.value = (data && data.items) || []
    accounts.value = (data && data.accounts) || []
    accountsMeta.value = (data && data.accounts_meta) || {}
  } catch (e) {
    attemptsError.value = e.message
  }
  attemptsLoading.value = false
}

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    list.value = await adminApi.listBlockedIPs() || []
  } catch (e) {
    error.value = e.message
  }
  loading.value = false
  nextTick(revealObserve)
}

const add = async () => {
  if (submitting.value) return
  const ip = form.value.ip.trim()
  if (!ip) { toast('请输入 IP 地址', 'error'); return }
  submitting.value = true
  try {
    await adminApi.addBlockedIP({ ip, note: form.value.note.trim() || undefined })
    toast('已拉黑', 'success')
    form.value = { ip: '', note: '' }
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const remove = async (b) => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.removeBlockedIP(b.id)
    toast('已解除', 'success')
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

// 解锁限频锁：cleared=0 不是失败，但也不能报「已解锁」——那个 IP 当前压根没有记录
const unlock = async (a) => {
  if (submitting.value) return
  submitting.value = true
  try {
    const r = await adminApi.resetSecurityAttempts({ ip: a.ip })
    if (r && r.cleared > 0) toast('已解除该 IP 的限频锁（不影响上方黑名单）', 'success')
    else toast('该 IP 当前没有锁定记录', 'info')
    loadAttempts()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

onMounted(() => { revealObserve() })
load()
loadAttempts()
</script>
