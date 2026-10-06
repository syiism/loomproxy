<template>
  <!-- 第三态：`total < 0` 是"这次没读到"，不是"没有行"。以前失败与空列表渲染成同一件事
       （分页整块隐身 + 页面顶着一句「命中 0 条」），待办清单 P99 列表页那一格治的就是它。
       后端列表失败已直接 500，所以这个哨兵由各页的 catch 写进 total（同一个形状也留给
       将来后端真的下发 -1 的时候——两种来源在面板只有一处判法）。 -->
  <div v-if="unavailable" class="mt-3 text-sm text-text-muted">条数不可用：本次读取失败</div>
  <div v-else-if="totalPages > 1" class="flex items-center justify-between mt-4">
    <div class="text-sm text-text-muted">共 {{ total }} 条</div>
    <div class="flex items-center gap-1.5">
      <button class="pg-btn" :disabled="page <= 1" @click="go(page - 1)">上一页</button>
      <button v-for="p in pages" :key="p" class="pg-btn font-mono" :class="p === page ? 'bg-text text-white border-text' : ''" @click="go(p)">{{ p }}</button>
      <button class="pg-btn" :disabled="page >= totalPages" @click="go(page + 1)">下一页</button>
    </div>
  </div>
  <div v-else-if="total > 0" class="mt-3 text-sm text-text-muted">共 {{ total }} 条</div>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  page: { type: Number, required: true },
  total: { type: Number, required: true },
  pageSize: { type: Number, default: 20 },
})

const emit = defineEmits(['change'])

const unavailable = computed(() => props.total < 0)
const totalPages = computed(() => Math.max(1, Math.ceil(Math.max(0, props.total) / props.pageSize)))

const pages = computed(() => {
  const t = totalPages.value
  let start = Math.max(1, Math.min(props.page - 2, t - 4))
  const end = Math.min(t, start + 4)
  start = Math.max(1, end - 4)
  const arr = []
  for (let i = start; i <= end; i++) arr.push(i)
  return arr
})

const go = (p) => {
  if (p >= 1 && p <= totalPages.value && p !== props.page) emit('change', p)
}
</script>

<style scoped>
.pg-btn {
  @apply px-3 py-1.5 border border-border rounded text-xs hover:bg-surface-alt transition-colors disabled:opacity-40 disabled:hover:bg-transparent;
}
</style>
