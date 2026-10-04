<template>
  <div>
    <PageHeader title="号池" subtitle="数据源凭证池运行状态。墙钟燃烧型按冷热续领维持活跃号，用量摊薄型把请求轮询到全部可用号上、被风控的号临时冷却而非判死。底座项目未接入数据源时列表为空。" />

    <div class="reveal flex items-center gap-4 mb-6">
      <button @click="load" class="btn-ghost btn-sm">刷新</button>
      <label class="text-sm text-text-muted flex items-center gap-1.5 cursor-pointer">
        <input type="checkbox" v-model="autoRefresh"> 每 10 秒自动刷新
      </label>
    </div>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <UiEmpty v-else-if="pools.length === 0" title="暂无号池" text="接入需要凭证池的数据源后，其号池状态会显示在这里。" />

    <section v-for="p in pools" :key="p.name" class="card reveal mb-6 !p-0 overflow-hidden">
      <div class="flex flex-wrap items-center gap-3 px-5 py-4 border-b border-border">
        <h2 class="font-serif text-lg">{{ p.name }}</h2>
        <UiTag :tone="p.running ? 'green' : 'gray'" :label="p.running ? '运行中' : '未启动'" />
        <UiTag tone="blue" :label="kindLabel(p.config && p.config.kind)" />
        <div class="text-xs text-text-muted font-mono ml-auto">
          <template v-if="isSpread(p)">{{ spreadSummary(p) }}</template>
          <template v-else>{{ burnSummary(p) }}</template>
        </div>
      </div>

      <div class="grid grid-cols-2 md:grid-cols-5 gap-3 px-5 py-4">
        <div v-for="k in statusKeys" :key="k">
          <div class="text-xs text-text-muted mb-1">{{ statusLabels[k] }}</div>
          <div class="font-mono text-xl font-medium">{{ p.counts[k] || 0 }}</div>
        </div>
      </div>

      <!-- 窄屏回退（待办清单 P33）：这 6 列在手机上要横滚将近两屏，而在这页真正要回答的是
           "哪台不对劲"——标识 + 状态 + 额度 + 有效期四格就够，凭证字段名与备注降成第三行。
           刻意不套 .card：外层 section 已是卡片，卡片套卡片会把正文宽度再吃掉一层。 -->
      <div v-if="p.devices && p.devices.length" class="sm:hidden divide-y divide-border border-t border-border">
        <div v-for="d in p.devices" :key="d.ident" class="px-5 py-3">
          <div class="flex items-start justify-between gap-2">
            <div class="font-mono text-xs break-all">{{ d.ident }}</div>
            <UiTag :tone="statusTones[d.status] || 'gray'" :label="statusLabels[d.status] || d.status" />
          </div>
          <div class="mt-1.5 text-xs text-text-muted font-mono">
            {{ d.used_quota }} / {{ d.total_quota }} · {{ expiryText(d) }}
          </div>
          <div v-if="credFields(d).length" class="mt-1 text-xs text-text-muted font-mono">{{ credFields(d).join(' · ') }}</div>
          <div v-if="d.note" class="mt-1 text-xs text-text-muted">{{ d.note }}</div>
        </div>
      </div>
      <div v-else class="sm:hidden px-5 py-3 text-sm text-text-muted border-t border-border">暂无号记录</div>

      <div class="table-wrap border-t border-border !rounded-none !border-0 hidden sm:block">
        <table class="table-base">
          <thead>
            <tr><th>标识</th><th>凭证字段</th><th>状态</th><th>周期额度</th><th>有效期至</th><th>备注</th></tr>
          </thead>
          <tbody>
            <tr v-for="d in p.devices" :key="d.ident">
              <td class="font-mono text-xs">{{ d.ident }}</td>
              <td class="font-mono text-xs text-text-muted">{{ credFields(d).join(' · ') || '—' }}</td>
              <td><UiTag :tone="statusTones[d.status] || 'gray'" :label="statusLabels[d.status] || d.status" /></td>
              <td class="font-mono text-xs">{{ d.used_quota }} / {{ d.total_quota }}</td>
              <td class="font-mono text-xs text-text-muted whitespace-nowrap">{{ expiryText(d) }}</td>
              <td class="text-xs text-text-muted">{{ d.note || '—' }}</td>
            </tr>
            <tr v-if="!p.devices || p.devices.length === 0">
              <td colspan="6" class="text-sm text-text-muted">暂无号记录</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted, nextTick, watch } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiTag from '../../components/UiTag.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import { adminApi } from '../../api/index.js'
import { fmtDate, revealObserve } from '../../utils.js'

const statusKeys = ['hot', 'cold', 'cooldown', 'spent', 'dead']
const statusLabels = { hot: '可用/活跃', cold: '冷备', cooldown: '冷却中', spent: '周期用尽', dead: '失效' }
const statusTones = { hot: 'green', cold: 'blue', cooldown: 'yellow', spent: 'gray', dead: 'red' }

// 池形态：Kind 是声明位，框架按它分叉调度节奏（P5）。两种形态的配置项语义不同，
// 一套摘要通用不了——墙钟型关心「保持几个活跃、临期多久续领」，摊薄型关心「摊到几台、冷却多久」。
const KIND_LABELS = { burn_wall_clock: '墙钟燃烧型', spread: '用量摊薄型' }
const kindLabel = (kind) => {
  const k = kind || 'burn_wall_clock'
  return KIND_LABELS[k] || k
}
const isSpread = (p) => p.config && p.config.kind === 'spread'
// 软删留下的行要说出来（待办清单 P35② 的可见性那一半）：框架自己不产生软删行，
// 这些行只可能来自手工 SQL，而它们在按状态的计数里**全部隐形**——占库、可能撞唯一索引，
// 却不占名额也没人再看见。0 时不挂这句（空态不写长说明，P24）；-1 是"查不到"，
// 绝不能显示成 0，那等于报一个让人放心的假清白。
const softDeletedNote = (p) => {
  const n = p.soft_deleted
  if (n == null || n === 0) return ''
  if (n < 0) return ' · 软删行读数不可用'
  return ` · 手工软删留下 ${n} 行（不计入任何状态，也不会被自动清理）`
}
// 死号保留被总号上限压下来时要说出来：运维看到「死号只留 2 条」而配置写 10，
// 不该去翻代码才知道是谁压的（待办清单 P35）
const deadNote = (c) => (
  c.max_dead != null && c.dead_retention != null && c.dead_retention !== c.max_dead
    ? ` · 死号只留 ${c.dead_retention}（配置 ${c.max_dead}，被总号上限压到）` : ''
)
const burnSummary = (p) => {
  const c = p.config || {}
  return `冷备 ${c.cold_spares} · 活跃上限 ${c.max_hot} · 总号上限 ${c.max_devices > 0 ? c.max_devices : '不限'} · 临期续领 ${c.renew_before_sec}s · 巡检 ${c.maintain_sec}s${deadNote(c)}${softDeletedNote(p)}`
}
const spreadSummary = (p) => {
  const c = p.config || {}
  const target = c.target_devices > 0 ? c.target_devices : (c.max_devices > 0 ? c.max_devices : 1)
  return `轮询目标 ${target} 台 · 总号上限 ${c.max_devices > 0 ? c.max_devices : '不限'} · 兜底冷却 ${c.cooldown_default_sec}s · 巡检 ${c.maintain_sec}s${deadNote(c)}${softDeletedNote(p)}`
}

// 凭证只列键名：摊薄型的嵌套凭证走 Payload，其第一层键名同样只出键名
const credFields = (d) => [...(d.creds || []), ...(d.payload_keys || [])]
const expiryText = (d) => {
  if (!d.expire_at) return '—'
  // 冷却中的号，expire_at 承载的是「冷却到什么时候」，不是资源到期时间
  return (d.status === 'cooldown' ? '冷却至 ' : '') + fmtDate(d.expire_at)
}

const loading = ref(true)
const error = ref('')
const pools = ref([])
const autoRefresh = ref(true)

const load = async () => {
  error.value = ''
  try {
    pools.value = await adminApi.listPools() || []
    await nextTick()
    revealObserve()
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

let timer = null
const applyTimer = () => {
  if (timer) clearInterval(timer)
  timer = autoRefresh.value ? setInterval(load, 10000) : null
}
watch(autoRefresh, applyTimer)

onMounted(async () => {
  await load()
  applyTimer()
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>
