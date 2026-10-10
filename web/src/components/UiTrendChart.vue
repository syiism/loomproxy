<template>
  <div ref="box">
    <div v-if="rows.length === 0" class="text-sm text-text-muted text-center py-6">近 7 天无调用记录</div>
    <template v-else>
      <div class="flex flex-wrap gap-x-4 gap-y-1.5 mb-3 text-xs text-text-muted">
        <span v-for="(s, i) in sources" :key="s" class="flex items-center gap-1.5">
          <span class="inline-block w-2.5 h-2.5 rounded-[2px]" :style="{ background: color(i) }"></span>{{ s }}
        </span>
      </div>
      <svg :viewBox="`0 0 ${W} ${H}`" class="w-full h-auto block" role="img" aria-label="近 7 天调用趋势">
        <!-- 横向网格线（按左轴刻度） -->
        <line
          v-for="t in leftTicks" :key="'g' + t"
          :x1="padL" :y1="yL(t)" :x2="W - padR" :y2="yL(t)"
          :style="{ stroke: 'rgb(var(--c-border))' }" stroke-width="1"
        />
        <!-- 左轴刻度（柱状：每日总量） -->
        <text
          v-for="t in leftTicks" :key="'lt' + t"
          :x="padL - 6" :y="yL(t) + 3" text-anchor="end" fill="rgb(var(--c-text-muted))" :font-size="tickFont"
        >{{ t }}</text>
        <!-- 右轴刻度（折线：分源调用量） -->
        <text
          v-for="t in rightTicks" :key="'rt' + t"
          :x="W - padR + 6" :y="yR(t) + 3" text-anchor="start" fill="rgb(var(--c-text-muted))" :font-size="tickFont"
        >{{ t }}</text>
        <!-- 轴线 -->
        <line :x1="padL" :y1="padT" :x2="padL" :y2="baseline" :style="{ stroke: 'rgb(var(--c-border))' }" />
        <line :x1="W - padR" :y1="padT" :x2="W - padR" :y2="baseline" :style="{ stroke: 'rgb(var(--c-border))' }" />
        <line :x1="padL" :y1="baseline" :x2="W - padR" :y2="baseline" :style="{ stroke: 'rgb(var(--c-border))' }" />

        <!-- 堆叠柱（每日总量，左轴） -->
        <g v-for="(d, di) in days" :key="d">
          <rect
            v-for="seg in segmentsOf(d, di)"
            :key="seg.source"
            :x="seg.x" :y="seg.y" :width="barW" :height="seg.h"
            :fill="seg.color" fill-opacity="0.45" :stroke="seg.color" stroke-width="0.75" rx="1"
          >
            <title>{{ d }} {{ seg.source }}：{{ seg.total }} 次（成功 {{ seg.success }}）</title>
          </rect>
          <text v-if="showDayLabel(di)" :x="barX(di) + barW / 2" :y="H - 6"
                text-anchor="middle" fill="rgb(var(--c-text-muted))" :font-size="dayFont">{{ d.slice(5) }}</text>
        </g>

        <!-- 分源折线（右轴） -->
        <polyline
          v-for="(s, si) in sources" :key="'line' + s"
          :points="linePoints(s)"
          fill="none" :stroke="color(si)" stroke-width="1.75" stroke-linejoin="round" stroke-linecap="round"
        />
        <g v-for="(s, si) in sources" :key="'pts' + s">
          <circle
            v-for="(pt, pi) in linePointsArr(s)" :key="pi"
            :cx="pt.x" :cy="pt.y" r="2.25" :fill="color(si)"
          >
            <title>{{ days[pi] }} {{ s }}：{{ valueOf(days[pi], s).total }} 次（成功 {{ valueOf(days[pi], s).success }}）</title>
          </circle>
        </g>
      </svg>
    </template>
  </div>
</template>

<script setup>
// 纯 SVG 组合趋势图（不引图表库）：堆叠柱（每日总量，左轴）+ 分源折线（右轴）
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

const props = defineProps({
  days: { type: Array, default: () => [] },     // ['2026-07-26', ...]
  sources: { type: Array, default: () => [] },  // ['novel_a', ...]
  rows: { type: Array, default: () => [] },     // [{day, source, total, success}]
})

const W = 720
const H = 220
const padL = 42
const padR = 42
const padT = 10
const bottomPad = 22
const baseline = H - bottomPad

// 区分度更高的柔和配色（蓝/绿/橙/紫/红/青/黄/灰循环）
const PALETTE = ['#4A90D9', '#55A868', '#ED8B33', '#8E6FBF', '#D9534F', '#3BA8A0', '#C9A227', '#7A7A7A']
const color = (i) => PALETTE[i % PALETTE.length]

const box = ref(null)
const boxW = ref(0)
let ro = null

// SVG 靠 viewBox 等比缩放，所以 `font-size="10"` 到了窄屏不是 10px 而是**约 5px**
// （390 视口去掉内边距后 scale≈0.49）。原来记的读数是"基本读不出日期"，根因是字号跟着几何缩、
// 不跟着可读性定。这里按容器宽度分档：窄屏放大字号，并把 7 个日期收成首/中/末三个（待办清单 P33）。
// 刻意不按视口断点写死——断点与真实容器宽度无关，卡片列一收窄就又糊了。
const scale = computed(() => (boxW.value ? boxW.value / W : 1))
const narrow = computed(() => scale.value < 0.75)
const tickFont = computed(() => (narrow.value ? 16 : 10))
const dayFont = computed(() => (narrow.value ? 18 : 10))
const showDayLabel = (di) => {
  if (!narrow.value) return true
  const last = Math.max(props.days.length - 1, 0)
  return di === 0 || di === last || di === Math.round(last / 2)
}

onMounted(() => {
  if (!box.value || typeof ResizeObserver === 'undefined') return
  boxW.value = box.value.clientWidth
  ro = new ResizeObserver((entries) => { boxW.value = entries[0].contentRect.width })
  ro.observe(box.value)
})
onBeforeUnmount(() => { if (ro) ro.disconnect() })

const nDays = computed(() => Math.max(props.days.length, 1))
const barW = computed(() => Math.min(40, ((W - padL - padR) / nDays.value) * 0.45))
const barX = (di) => padL + ((di + 0.5) * (W - padL - padR)) / nDays.value - barW.value / 2
const centerX = (di) => padL + ((di + 0.5) * (W - padL - padR)) / nDays.value

// "day|source" -> row 索引
const rowMap = computed(() => {
  const m = {}
  for (const r of props.rows) m[r.day + '|' + r.source] = r
  return m
})
const valueOf = (d, s) => rowMap.value[d + '|' + s] || { total: 0, success: 0 }
const dayTotal = (d) => props.sources.reduce((sum, s) => sum + valueOf(d, s).total, 0)

// 刻度：max 向上取整为 1/2/5×10^n，分 4 档
const niceCeil = (v) => {
  if (v <= 0) return 4
  const pow = Math.pow(10, Math.floor(Math.log10(v)))
  for (const m of [1, 2, 5, 10]) {
    if (m * pow >= v) return m * pow
  }
  return 10 * pow
}
const ticksOf = (max) => {
  const top = niceCeil(max)
  return [0, 1, 2, 3, 4].map((i) => Math.round((top * i) / 4))
}

const barMax = computed(() => Math.max(1, ...props.days.map(dayTotal)))
const lineMax = computed(() => {
  let m = 1
  for (const d of props.days) {
    for (const s of props.sources) m = Math.max(m, valueOf(d, s).total)
  }
  return m
})
const leftTicks = computed(() => ticksOf(barMax.value))
const rightTicks = computed(() => ticksOf(lineMax.value))

const yL = (v) => baseline - (v / leftTicks.value[4]) * (baseline - padT)
const yR = (v) => baseline - (v / rightTicks.value[4]) * (baseline - padT)

// 某天柱子的堆叠分段（从基线向上，左轴刻度）
const segmentsOf = (d, di) => {
  let acc = 0
  const segs = []
  props.sources.forEach((s, si) => {
    const r = valueOf(d, s)
    if (!r.total) return
    const h = (r.total / leftTicks.value[4]) * (baseline - padT)
    segs.push({ source: s, total: r.total, success: r.success, x: barX(di), y: baseline - acc - h, h, color: color(si) })
    acc += h
  })
  return segs
}

// 某数据源跨天折线（右轴刻度）
const linePointsArr = (s) => props.days.map((d, di) => ({ x: centerX(di), y: yR(valueOf(d, s).total) }))
const linePoints = (s) => linePointsArr(s).map((p) => `${p.x},${p.y}`).join(' ')
</script>
