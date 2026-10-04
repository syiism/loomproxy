// 通用工具：格式化、标签色调映射、toast、滚动渐入

export function escapeHtml(s) {
  if (s == null) return ''
  return String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]))
}

export function fmtDate(s) {
  if (!s) return '—'
  const d = new Date(s)
  if (isNaN(d)) return s
  const pad = n => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// 角色代码 → 淡彩色调
export function roleTone(code) {
  const map = { admin: 'red', vip: 'yellow', user: 'gray' }
  return map[code] || 'gray'
}

export function statusTone(status) {
  return status === 1 ? 'green' : 'red'
}

// toast 默认 3 秒。**安全类提示不是装饰**（例如「上次登录以来有 N 台设备被移出」），
// 所以留一个 ms 参数让调用方能按重要性延长，而不是新造一套弹窗组件。
export function toast(msg, type = 'info', ms = 3000) {
  const el = document.createElement('div')
  el.className = 'animate-slide-in bg-surface border border-border rounded-lg px-4 py-3 text-sm min-w-[240px] max-w-full'
  el.style.borderLeft = '3px solid ' + (type === 'error' ? '#9F2F2D' : type === 'success' ? '#346538' : '#111111')
  el.textContent = msg
  const container = document.getElementById('toast')
  if (container) {
    container.appendChild(el)
    setTimeout(() => {
      el.style.opacity = '0'
      el.style.transition = 'opacity 200ms'
      setTimeout(() => el.remove(), 200)
    }, ms)
  }
}

let io
export function revealObserve() {
  if (!io) {
    io = new IntersectionObserver(entries => {
      entries.forEach(e => {
        if (e.isIntersecting) {
          e.target.classList.add('in')
          io.unobserve(e.target)
        }
      })
    }, { threshold: 0.05 })
  }
  document.querySelectorAll('.reveal:not(.in)').forEach((el, i) => {
    el.style.setProperty('--i', i % 8)
    io.observe(el)
  })
}

// 额度档位（待办清单 P42）：**卡片上的三颗圆点与右上角百分比标签共用这一处判据**。
// 说的是「还剩多少」：剩余 ≤33% 红、34~66% 黄、≥67% 绿。
// 判据长在一处是这条的全部意义——两处各写一套阈值，一张卡片就能同时说「黄」和「绿」。
export const quotaBand = (remaining) => {
  if (remaining == null || isNaN(remaining)) return null
  if (remaining <= 33) return 'red'
  if (remaining <= 66) return 'yellow'
  return 'green'
}

// remainingPct 直接从后端的 remaining / effective_total 算，不用 `100 - usage_pct`：
// usage_pct 是整数**向下**取整的已用比例，两头各取一次整，会读出「已用 0%、剩余 0%」这种自相矛盾的数。
// total<0（不限额）返回 null —— 那不是「还剩很多」，是「没有额度概念」，视觉上必须是缺格而不是绿档。
export const remainingPct = (item) => {
  if (!item) return null
  const total = item.effective_total
  if (total == null || total < 0) return null
  if (total === 0) return 0 // 额度 0 = 一点都没有 = 红档（这是有额度概念、且已经没了）
  return Math.min(100, Math.max(0, Math.round((item.remaining / total) * 100)))
}

// 亮几颗灯：一档 1 颗红、二档 2 颗黄、三档 3 颗绿；null 一颗都不亮
export const quotaLitCount = (band) => (band === 'red' ? 1 : band === 'yellow' ? 2 : band === 'green' ? 3 : 0)

// 灯的颜色写成**静态类名表**：Tailwind 只扫源码里出现过的字面量，
// 拼字符串（'bg-pale-' + band + '-fg'）不会产出 CSS，而且不报错——正是 P31 那一族的形状。
export const QUOTA_DOT_CLASS = {
  red: 'bg-pale-red-fg',
  yellow: 'bg-pale-yellow-fg',
  green: 'bg-pale-green-fg',
}
