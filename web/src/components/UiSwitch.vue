<template>
  <button
    type="button"
    role="switch"
    :aria-checked="modelValue"
    :disabled="disabled"
    class="inline-flex items-center gap-2.5 group"
    @click="toggle"
  >
    <span
      class="w-9 h-5 p-0.5 rounded-full border transition-colors inline-flex items-center shrink-0"
      :class="modelValue ? 'bg-accent border-accent' : 'bg-surface-alt border-border'"
    >
      <span
        class="w-4 h-4 rounded-full bg-on-accent transition-transform"
        :class="modelValue ? 'translate-x-4' : 'translate-x-0'"
      />
    </span>
    <span v-if="label" class="text-sm" :class="modelValue ? 'text-text' : 'text-text-muted'">{{ label }}</span>
  </button>
</template>

<script setup>
const props = defineProps({
  modelValue: { type: Boolean, default: false },
  label: { type: String, default: '' },
  disabled: { type: Boolean, default: false },
})

const emit = defineEmits(['update:modelValue'])
const toggle = () => {
  if (!props.disabled) emit('update:modelValue', !props.modelValue)
}
</script>
