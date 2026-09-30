<template>
  <div v-if="totalPages > 1" class="flex items-center justify-between mt-4">
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

const totalPages = computed(() => Math.max(1, Math.ceil(props.total / props.pageSize)))

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
