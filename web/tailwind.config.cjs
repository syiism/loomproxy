module.exports = {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  theme: {
    extend: {
      colors: {
        // 色板全部走 CSS 变量（RGB 三元组），明暗两套值在 styles.css 的 :root / [data-theme=dark] 里定义。
        // 新增颜色请加到同一处，不要在组件里写死 hex——否则暗色下会漏洞。
        bg: 'rgb(var(--c-bg) / <alpha-value>)',
        surface: 'rgb(var(--c-surface) / <alpha-value>)',
        'surface-alt': 'rgb(var(--c-surface-alt) / <alpha-value>)',
        border: 'rgb(var(--c-border) / <alpha-value>)',
        text: 'rgb(var(--c-text) / <alpha-value>)',
        'text-muted': 'rgb(var(--c-text-muted) / <alpha-value>)',
        accent: 'rgb(var(--c-accent) / <alpha-value>)',
        'on-accent': 'rgb(var(--c-on-accent) / <alpha-value>)',
        'pale-red': { bg: 'rgb(var(--c-pale-red-bg) / <alpha-value>)', fg: 'rgb(var(--c-pale-red-fg) / <alpha-value>)' },
        'pale-blue': { bg: 'rgb(var(--c-pale-blue-bg) / <alpha-value>)', fg: 'rgb(var(--c-pale-blue-fg) / <alpha-value>)' },
        'pale-green': { bg: 'rgb(var(--c-pale-green-bg) / <alpha-value>)', fg: 'rgb(var(--c-pale-green-fg) / <alpha-value>)' },
        'pale-yellow': { bg: 'rgb(var(--c-pale-yellow-bg) / <alpha-value>)', fg: 'rgb(var(--c-pale-yellow-fg) / <alpha-value>)' },
        'pale-gray': { bg: 'rgb(var(--c-pale-gray-bg) / <alpha-value>)', fg: 'rgb(var(--c-pale-gray-fg) / <alpha-value>)' },
      },
      fontFamily: {
        sans: ['Geist Sans', 'SF Pro Display', 'Helvetica Neue', 'Switzer', 'sans-serif'],
        serif: ['Newsreader', 'Playfair Display', 'Instrument Serif', 'Georgia', 'serif'],
        mono: ['Geist Mono', 'SF Mono', 'JetBrains Mono', 'Menlo', 'monospace'],
      },
      letterSpacing: {
        tight: '-0.02em',
        tighter: '-0.03em',
      },
    },
  },
}
