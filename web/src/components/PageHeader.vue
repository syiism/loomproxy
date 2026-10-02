<template>
  <div class="reveal flex flex-wrap items-end justify-between gap-4 mb-8 md:mb-10">
    <div>
      <h1 class="font-serif text-3xl md:text-4xl lg:text-5xl font-medium tracking-tighter leading-[1.1] mb-2">{{ title }}</h1>
      <p v-if="subtitle" class="text-text-muted text-sm md:text-base">{{ subtitle }}</p>
    </div>
    <div v-if="$slots.actions" class="flex flex-wrap items-center gap-3 justify-end">
      <slot name="actions" />
    </div>
  </div>
</template>

<script setup>
import { useSlots } from 'vue'

defineProps({
  title: { type: String, required: true },
  subtitle: { type: String, default: '' },
})

const slots = useSlots()
</script>
