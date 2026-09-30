<template>
  <span class="tag" :class="toneClass"><slot>{{ label }}</slot></span>
</template>

<script setup>
const TONES = {
  red: 'bg-pale-red-bg text-pale-red-fg',
  blue: 'bg-pale-blue-bg text-pale-blue-fg',
  green: 'bg-pale-green-bg text-pale-green-fg',
  yellow: 'bg-pale-yellow-bg text-pale-yellow-fg',
  gray: 'bg-pale-gray-bg text-pale-gray-fg',
}

import { computed } from 'vue'

const props = defineProps({
  tone: { type: String, default: 'gray' },
  label: { type: String, default: '' },
})

const toneClass = computed(() => TONES[props.tone] || TONES.gray)
</script>
