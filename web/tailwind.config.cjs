module.exports = {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  theme: {
    extend: {
      colors: {
        bg: '#FBFBFA',
        surface: '#FFFFFF',
        'surface-alt': '#F9F9F8',
        border: '#EAEAEA',
        text: '#111111',
        'text-muted': '#787774',
        accent: '#111111',
        'pale-red': { bg: '#FDEBEC', fg: '#9F2F2D' },
        'pale-blue': { bg: '#E1F3FE', fg: '#1F6C9F' },
        'pale-green': { bg: '#EDF3EC', fg: '#346538' },
        'pale-yellow': { bg: '#FBF3DB', fg: '#956400' },
        'pale-gray': { bg: '#F2F1EF', fg: '#787774' },
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
