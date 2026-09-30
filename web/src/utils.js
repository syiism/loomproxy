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

export function toast(msg, type = 'info') {
  const el = document.createElement('div')
  el.className = 'animate-slide-in bg-surface border border-border rounded-lg px-4 py-3 text-sm min-w-[240px]'
  el.style.borderLeft = '3px solid ' + (type === 'error' ? '#9F2F2D' : type === 'success' ? '#346538' : '#111111')
  el.textContent = msg
  const container = document.getElementById('toast')
  if (container) {
    container.appendChild(el)
    setTimeout(() => {
      el.style.opacity = '0'
      el.style.transition = 'opacity 200ms'
      setTimeout(() => el.remove(), 200)
    }, 3000)
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
