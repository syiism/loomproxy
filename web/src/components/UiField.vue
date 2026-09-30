<template>
  <div>
    <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">
      {{ label }}<span v-if="labelHint" class="normal-case tracking-normal">（{{ labelHint }}）</span>
    </label>
    <slot />
    <div v-if="hint" class="mt-1.5 text-xs text-text-muted">{{ hint }}</div>
  </div>
</template>

<script setup>
defineProps({
  label: { type: String, required: true },
  labelHint: { type: String, default: '' },
  hint: { type: String, default: '' },
})
</script>
