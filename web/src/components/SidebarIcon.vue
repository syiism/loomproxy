<script setup>
const props = defineProps({
  name: { type: String, required: true },
  size: { type: Number, default: 16 },
  strokeWidth: { type: Number, default: 1.5 },
})

const iconMap = {
  'layout-dashboard': [['rect', { width: '7', height: '9', x: '3', y: '3' }], ['rect', { width: '7', height: '5', x: '14', y: '3' }], ['rect', { width: '7', height: '9', x: '14', y: '12' }], ['rect', { width: '7', height: '5', x: '3', y: '16' }]],
  // 双人图标：两人左右分离排布（原 Lucide users 数据第二人几乎被第一人完全遮挡）
  'users': [['path', { d: 'M15.5 21a7.5 7.5 0 0 0-15 0' }], ['circle', { cx: '8', cy: '7', r: '3.5' }], ['circle', { cx: '17', cy: '8.5', r: '3' }], ['path', { d: 'M23 21a6.5 6.5 0 0 0-6-6.5' }]],
  'shield': [['path', { d: 'M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10' }]],
  'credit-card': [['rect', { width: '20', height: '14', x: '2', y: '5', rx: '2' }], ['line', { x1: '2', x2: '22', y1: '10', y2: '10' }]],
  'database': [['ellipse', { cx: '12', cy: '5', rx: '9', ry: '3' }], ['path', { d: 'M3 5V19a9 3 0 0 0 18 0V5' }], ['path', { d: 'M3 12a9 3 0 0 0 18 0' }]],
  'code': [['polyline', { points: '16 18 22 12 16 6' }], ['polyline', { points: '8 6 2 12 8 18' }]],
  'list': [['line', { x1: '8', x2: '21', y1: '6', y2: '6' }], ['line', { x1: '8', x2: '21', y1: '12', y2: '12' }], ['line', { x1: '8', x2: '21', y1: '18', y2: '18' }], ['line', { x1: '3', x2: '3.01', y1: '6', y2: '6' }], ['line', { x1: '3', x2: '3.01', y1: '12', y2: '12' }], ['line', { x1: '3', x2: '3.01', y1: '18', y2: '18' }]],
  'settings': [['circle', { cx: '12', cy: '12', r: '3' }], ['path', { d: 'M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z' }]],
  'panel-left': [['rect', { width: '18', height: '12', x: '3', y: '6', rx: '2' }], ['line', { x1: '9', x2: '9', y1: '6', y2: '18' }]],
  'panel-left-close': [['rect', { width: '18', height: '12', x: '3', y: '6', rx: '2' }], ['line', { x1: '9', x2: '9', y1: '6', y2: '18' }], ['line', { x1: '14', x2: '14', y1: '10', y2: '14' }]],
  'monitor': [['rect', { x: '2', y: '3', width: '20', height: '14', rx: '2' }], ['path', { d: 'M8 21h8M12 17v4' }]],
  'activity': [['polyline', { points: '22 12 18 12 15 21 9 3 6 12 2 12' }]],
  'ticket': [['path', { d: 'M2 9a3 3 0 0 1 0 6v2a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-2a3 3 0 0 1 0-6V7a2 2 0 0 0-2-2H4a2 2 0 0 0-2 2Z' }], ['path', { d: 'M13 5v2' }], ['path', { d: 'M13 17v2' }], ['path', { d: 'M13 11v2' }]],
  'ban': [['circle', { cx: '12', cy: '12', r: '10' }], ['path', { d: 'm4.9 4.9 14.2 14.2' }]],
  'layers': [['path', { d: 'm12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83Z' }], ['path', { d: 'm22 17.65-9.17 4.16a2 2 0 0 1-1.66 0L2 17.65' }], ['path', { d: 'm22 12.65-9.17 4.16a2 2 0 0 1-1.66 0L2 12.65' }]],
}
</script>

<template>
  <svg v-if="iconMap[name]" xmlns="http://www.w3.org/2000/svg" :width="size" :height="size" viewBox="0 0 24 24" fill="none" stroke="currentColor" :stroke-width="strokeWidth" stroke-linecap="round" stroke-linejoin="round">
    <template v-for="(child, i) in iconMap[name]" :key="i">
      <component :is="child[0]" v-bind="child[1]" />
    </template>
  </svg>
</template>
