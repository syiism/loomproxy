<template>
  <div class="reveal flex flex-wrap items-end justify-between gap-4 mb-8 md:mb-10">
    <div>
      <h1 class="font-serif text-3xl md:text-4xl lg:text-5xl font-medium tracking-tighter leading-[1.1] mb-2">{{ title }}</h1>
      <p v-if="subtitle" class="text-text-muted text-sm md:text-base">{{ subtitle }}</p>
    </div>
    <!-- min-w-0/max-w-full：flex 子项默认 min-width:auto，一个不肯缩的 actions 组会把整页顶出视口
         （实测 Ranking 在 390 视口上 scrollWidth=414，待办清单 P65）。这里兜一层，
         让"要么自己换行、要么在我的宽度里排"成为插槽内容必须满足的约束。 -->
    <div v-if="$slots.actions" class="flex flex-wrap items-center gap-3 justify-end min-w-0 max-w-full">
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
