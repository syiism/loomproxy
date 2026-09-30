<template>
  <div class="text-center py-12">
    <div class="font-serif text-lg font-medium tracking-tight mb-1">{{ title }}</div>
    <div class="text-sm text-text-muted"><slot>{{ text }}</slot></div>
  </div>
</template>

<script setup>
defineProps({
  title: { type: String, default: '暂无数据' },
  text: { type: String, default: '' },
})
</script>
