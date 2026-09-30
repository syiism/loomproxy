<template>
  <teleport to="body">
    <div v-if="open" class="fixed inset-0 z-50 flex items-end sm:items-center justify-center bg-black/40 animate-fade" @click.self="close">
      <div class="bg-surface border-t sm:border border-border rounded-t-xl sm:rounded-xl w-full max-h-[90vh] overflow-y-auto p-5 sm:p-6 md:p-8 animate-rise" :class="wide ? 'sm:max-w-3xl' : 'sm:max-w-md'">
        <div class="flex items-start justify-between mb-5">
          <div class="font-serif text-xl font-medium tracking-tight">{{ title }}</div>
          <button type="button" class="text-text-muted hover:text-text transition-colors p-1 -m-1" @click="close" aria-label="关闭">
            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M6 18L18 6M6 6l12 12"></path></svg>
          </button>
        </div>
        <div ref="slotRef"><slot /></div>
        <div class="flex justify-end gap-3 mt-6 pt-4 border-t border-border">
          <slot name="footer">
            <button type="button" class="btn-ghost btn-sm" @click="close">取消</button>
            <button type="button" class="btn-primary btn-sm" :disabled="confirmLoading" @click="handleConfirm">
              <UiSpinner v-if="confirmLoading" class="w-4 h-4" />
              <span v-else>{{ confirmText || '确定' }}</span>
            </button>
          </slot>
        </div>
      </div>
    </div>
  </teleport>
</template>

<script setup>
import { ref, useSlots } from 'vue'
import UiSpinner from './UiSpinner.vue'

const props = defineProps({
  open: { type: Boolean, default: false },
  title: { type: String, default: '' },
  wide: { type: Boolean, default: false },
  confirmLoading: { type: Boolean, default: false },
  confirmText: { type: String, default: '' },
})

const emit = defineEmits(['close', 'confirm', 'update:open'])

const slotRef = ref(null)
const slots = useSlots()

const close = () => {
  emit('update:open', false)
  emit('close')
}

const handleConfirm = () => {
  const slotEl = slotRef.value
  if (slotEl) {
    const form = slotEl.querySelector(':scope > form')
    if (form) {
      form.requestSubmit()
      return
    }
  }
  emit('confirm')
}
</script>
