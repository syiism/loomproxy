<template>
  <div class="flex items-center justify-center gap-3 py-12 text-text-muted text-sm">
    <span class="animate-spin-custom inline-block w-3.5 h-3.5 border-2 border-border border-t-text rounded-full"></span>
    <span>{{ text }}</span>
  </div>
</template>

<script setup>
defineProps({ text: { type: String, default: '加载中' } })
</script>
