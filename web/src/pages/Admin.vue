<template>
  <div>
    <PageHeader title="管理后台" subtitle="管理页面导航。读数不在这一页：额度看「概览」，账号看「用户管理」，调用看「接口监控」。" />

    <!-- 这一页只做导航，不放读数（待办清单 P28·D1 的落地形态）。
         以前这里有四张用户计数卡，而它们的家在「用户管理」页——同一份数字在两页各显示一遍只会各自漂移；
         合并成一个大页又要靠 isAdmin 条件渲染两套视图，那正是本轮清掉的那类隐患。
         下面的清单必须与 main.js 的 /admin/* 路由一一对应：号池那一行就是这样漏掉的，
         现在由 `make vet` 的 nav-check 拦。 -->
    <div class="reveal">
      <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">管理页面</h2>
      <div class="grid grid-cols-1 md:grid-cols-3 gap-4 md:gap-6">
        <div v-for="q in shortcuts" :key="q.to" class="card card-hover reveal cursor-pointer active:scale-[0.99]" @click="$router.push(q.to)">
          <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">{{ q.tag }}</div>
          <div class="font-serif text-xl font-medium tracking-tight">{{ q.title }}</div>
          <div class="mt-3 text-sm text-text-muted">{{ q.desc }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onMounted } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import { revealObserve } from '../utils.js'

// 11 个管理子页，顺序按「先看人与权限 → 额度与接口 → 源 → 运营 → 安全 → 系统」
const shortcuts = [
  { to: '/admin/users', tag: '用户管理', title: '用户与角色', desc: '新建用户、角色分配、套餐与重置密码、单用户额度覆盖' },
  { to: '/admin/roles', tag: '权限', title: '角色管理', desc: '角色的创建与维护' },
  { to: '/admin/quotas', tag: '额度', title: '额度 · 套餐', desc: '套餐、各维度限额与可访问的数据源（套餐↔源的关联只在这里改）' },
  { to: '/admin/interfaces', tag: '接口', title: '接口消耗与限流', desc: '每个接口消耗几点、按全局或按套餐限速' },
  { to: '/admin/datasources', tag: '数据源', title: '数据源管理', desc: '启用/禁用、分组、榜单可见与平台默认地址' },
  { to: '/admin/monitor', tag: '运营', title: '接口监控', desc: '调用统计、趋势、内容维度与最近/历史明细' },
  { to: '/admin/usage-logs', tag: '运营', title: '用量流水', desc: '额度消耗记录，可按用户与数据源筛' },
  { to: '/admin/pools', tag: '运营', title: '号池', desc: '凭证池的水位、状态与冷却原因（只读，号由源自己建）' },
  { to: '/admin/redeem-codes', tag: '运营', title: '卡密管理', desc: '兑换卡密的生成、作废与批次追踪' },
  { to: '/admin/blocked-ips', tag: '安全', title: 'IP 拉黑', desc: '黑名单 IP 的登录、注册与接口调用拦截' },
  { to: '/admin/settings', tag: '系统', title: '系统设置', desc: '注册开关、默认角色与套餐、站点参数' },
]

onMounted(() => { revealObserve() })
</script>
