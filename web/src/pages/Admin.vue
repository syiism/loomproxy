<template>
  <div>
    <PageHeader title="管理后台" subtitle="站点运营数据与快捷入口。" />

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <template v-else>
      <div class="grid grid-cols-2 lg:grid-cols-4 gap-4 md:gap-6">
        <div v-for="c in cards" :key="c.label" class="card reveal">
          <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">{{ c.label }}</div>
          <div class="font-serif text-3xl md:text-4xl font-medium tracking-tighter">{{ c.value }}</div>
        </div>
      </div>

      <div class="mt-10 md:mt-12 reveal">
        <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">快捷入口</h2>
        <div class="grid grid-cols-1 md:grid-cols-3 gap-4 md:gap-6">
          <div v-for="q in shortcuts" :key="q.to" class="card card-hover reveal cursor-pointer active:scale-[0.99]" @click="$router.push(q.to)">
            <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">{{ q.tag }}</div>
            <div class="font-serif text-xl font-medium tracking-tight">{{ q.title }}</div>
            <div class="mt-3 text-sm text-text-muted">{{ q.desc }}</div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import PageHeader from '../components/PageHeader.vue'
import UiSpinner from '../components/UiSpinner.vue'
import UiEmpty from '../components/UiEmpty.vue'
import { adminApi } from '../api/index.js'
import { revealObserve } from '../utils.js'

const router = useRouter()

const loading = ref(true)
const error = ref('')
const stats = ref({})

const shortcuts = [
  { to: '/admin/users', tag: '用户管理', title: '用户与角色', desc: '新建用户、角色分配、套餐与重置密码' },
  { to: '/admin/quotas', tag: '额度', title: '套餐配置', desc: '套餐与各维度调用限制' },
  { to: '/admin/datasources', tag: '数据源', title: '数据源管理', desc: '数据源启用/禁用、分类与排序' },
  { to: '/admin/interfaces', tag: '接口', title: '接口管理', desc: '各数据源组的接口消耗与状态配置' },
  { to: '/admin/usage-logs', tag: '运营', title: '用量流水', desc: '额度消耗记录查询' },
  { to: '/admin/redeem-codes', tag: '运营', title: '卡密管理', desc: '套餐兑换卡密的生成、作废与批次追踪' },
  { to: '/admin/monitor', tag: '运营', title: '接口监控', desc: '数据源接口调用统计、最近与历史调用明细' },
  { to: '/admin/blocked-ips', tag: '安全', title: 'IP 拉黑', desc: '黑名单 IP 的登录、注册与接口调用拦截' },
  { to: '/admin/settings', tag: '系统', title: '系统设置', desc: '注册开关、默认角色与套餐' },
  { to: '/admin/roles', tag: '权限', title: '角色管理', desc: '角色的创建与维护' },
]

const cards = computed(() => {
  const s = stats.value
  return [
    { label: '总用户数', value: s.total_users ?? 0 },
    { label: '今日新增', value: s.today_new ?? 0 },
    { label: '管理员', value: s.admin_count ?? 0 },
    { label: 'VIP 用户', value: s.vip_count ?? 0 },
  ]
})

onMounted(() => { revealObserve() })

const loadStats = async () => {
  try {
    stats.value = await adminApi.stats()
  } catch (e) {
    error.value = e.message
  }
  loading.value = false
  nextTick(revealObserve)
}

loadStats()
</script>
