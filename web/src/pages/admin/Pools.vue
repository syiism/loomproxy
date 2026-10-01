<template>
  <div>
    <PageHeader title="号池" subtitle="数据源凭证池运行状态（冷热分离）。号池由需要凭证池的数据源注册，底座项目未接入数据源时列表为空。" />

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
          冷备 {{ p.config.cold_spares }} · 活跃上限 {{ p.config.max_hot }} · 总号上限 {{ p.config.max_devices > 0 ? p.config.max_devices : '不限' }} · 临期续领 {{ p.config.renew_before_sec }}s · 巡检 {{ p.config.maintain_sec }}s
        </div>
      </div>

      <div class="grid grid-cols-2 md:grid-cols-4 gap-3 px-5 py-4">
        <div v-for="k in statusKeys" :key="k">
          <div class="text-xs text-text-muted mb-1">{{ statusLabels[k] }}</div>
          <div class="font-mono text-xl font-medium">{{ p.counts[k] || 0 }}</div>
        </div>
      </div>

      <div class="table-wrap border-t border-border !rounded-none !border-0">
        <table class="table-base">
          <thead>
            <tr><th>标识</th><th>凭证字段</th><th>状态</th><th>周期额度</th><th>有效期至</th></tr>
          </thead>
          <tbody>
            <tr v-for="d in p.devices" :key="d.ident">
              <td class="font-mono text-xs">{{ d.ident }}</td>
              <td class="font-mono text-xs text-text-muted">{{ (d.creds || []).join(' · ') || '—' }}</td>
              <td><UiTag :tone="statusTones[d.status] || 'gray'" :label="statusLabels[d.status] || d.status" /></td>
              <td class="font-mono text-xs">{{ d.used_quota }} / {{ d.total_quota }}</td>
              <td class="font-mono text-xs text-text-muted whitespace-nowrap">{{ d.expire_at ? fmtDate(d.expire_at) : '—' }}</td>
            </tr>
            <tr v-if="!p.devices || p.devices.length === 0">
              <td colspan="5" class="text-sm text-text-muted">暂无号记录</td>
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

const statusKeys = ['hot', 'cold', 'spent', 'dead']
const statusLabels = { hot: '活跃', cold: '冷备', spent: '周期用尽', dead: '失效' }
const statusTones = { hot: 'green', cold: 'blue', spent: 'yellow', dead: 'red' }

// 池形态：Kind 是声明位，框架不据它分叉行为。空值就是墙钟燃烧型（目前唯一的形态）；
// 遇到没见过的值原样显示，别替管理员猜——这一列存在的意义正是「一排 cold 号未必是池没工作」
const KIND_LABELS = { burn_wall_clock: '墙钟燃烧型' }
const kindLabel = (kind) => {
  const k = kind || 'burn_wall_clock'
  return KIND_LABELS[k] || k
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
