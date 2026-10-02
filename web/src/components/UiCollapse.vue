<template>
  <section class="card mb-6 !p-0 overflow-hidden">
    <button type="button" class="w-full flex items-center gap-3 px-4 md:px-6 py-3.5 text-left hover:bg-surface-alt/60 transition-colors"
            :aria-expanded="open ? 'true' : 'false'" @click="toggle">
      <span class="font-serif text-base md:text-lg font-medium tracking-tight shrink-0">{{ title }}</span>
      <span class="min-w-0 flex-1 truncate font-mono text-xs text-text-muted">{{ open ? '' : summary }}</span>
      <span class="shrink-0 font-mono text-xs text-text-muted">{{ open ? '收起 ▲' : '展开 ▼' }}</span>
    </button>
    <div v-show="open" class="px-4 md:px-6 pb-1 pt-4 border-t border-border">
      <slot />
    </div>
  </section>
</template>

<script setup>
// 折叠块。两条设计约束，都不是美学：
//  1. **收起态必须给一行读数摘要**（`summary`）——折叠是省滚动，不是藏信息。
//     摘要写成形容词（「暂无异常」）就等于没有。
//  2. 展开后要重跑 reveal 观察（`utils.js` 的 `revealObserve` 只观察调用时**可见**的 `.reveal:not(.in)`）。
//     段内若含 `reveal` 元素，收起时它拿不到 `.in`，展开后会永远停在 opacity:0——
//     这就是 §10 那条「reveal 依赖观察时机」的坑，组件里一次性兜住，别让每个页面各踩一遍。
import { nextTick, ref, watchEffect } from 'vue'
import { revealObserve } from '../utils.js'

const props = defineProps({
  title: { type: String, required: true },
  summary: { type: String, default: '' },
  storageKey: { type: String, default: '' },
  defaultOpen: { type: Boolean, default: false },
})
const emit = defineEmits(['toggle'])

const storeName = () => (props.storageKey ? 'loomproxy.collapse.' + props.storageKey : '')

const readSaved = () => {
  const n = storeName()
  if (!n) return null
  try { return localStorage.getItem(n) } catch (e) { return null } // 隐私模式下 localStorage 会抛，折叠退成默认态而不是白屏
}

const saved = readSaved()
const open = ref(saved === null ? props.defaultOpen : saved === '1')

watchEffect(() => {
  const n = storeName()
  if (!n) return
  try { localStorage.setItem(n, open.value ? '1' : '0') } catch (e) { /* 存不下就算了 */ }
})

const toggle = () => {
  open.value = !open.value
  if (open.value) nextTick(revealObserve)
  emit('toggle', open.value)
}
</script>
