<template>
  <button type="button" class="inline-flex items-center gap-1 transition-colors"
          :class="active ? 'text-text font-medium' : 'hover:text-text'"
          :aria-label="'按' + label + '排序' + (active ? (dir === 'desc' ? '（当前降序）' : '（当前升序）') : '')"
          @click="$emit('pick')">
    {{ label }}<span class="text-xs">{{ active ? (dir === 'desc' ? '▼' : '▲') : '' }}</span>
  </button>
</template>

<script setup>
// 表头排序按钮：点击同一列切方向、点别的列换列（默认降序）。
// 只做「发信号」，排序本身在服务端做——这一页的数列全是跨表聚合出来的，
// 在浏览器里只排当前这一页 20 行会排出一张假表（P45 要的正是「谁最不像一个人用」这种全站次序）。
defineProps({
  label: { type: String, required: true },
  active: { type: Boolean, default: false },
  dir: { type: String, default: 'desc' },
})
defineEmits(['pick'])
</script>
