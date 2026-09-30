<template>
  <div>
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
          stroke="#EAEAEA" stroke-width="1"
        />
        <!-- 左轴刻度（柱状：每日总量） -->
        <text
          v-for="t in leftTicks" :key="'lt' + t"
          :x="padL - 6" :y="yL(t) + 3" text-anchor="end" fill="#787774" font-size="10"
        >{{ t }}</text>
        <!-- 右轴刻度（折线：分源调用量） -->
        <text
          v-for="t in rightTicks" :key="'rt' + t"
          :x="W - padR + 6" :y="yR(t) + 3" text-anchor="start" fill="#787774" font-size="10"
        >{{ t }}</text>
        <!-- 轴线 -->
        <line :x1="padL" :y1="padT" :x2="padL" :y2="baseline" stroke="#EAEAEA" />
        <line :x1="W - padR" :y1="padT" :x2="W - padR" :y2="baseline" stroke="#EAEAEA" />
        <line :x1="padL" :y1="baseline" :x2="W - padR" :y2="baseline" stroke="#EAEAEA" />

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
          <text :x="barX(di) + barW / 2" :y="H - 6" text-anchor="middle" fill="#787774" font-size="10">{{ d.slice(5) }}</text>
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
import { computed } from 'vue'

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
