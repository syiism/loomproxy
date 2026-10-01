import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 开发时将后端 API 代理到本地 Go 服务（默认 8081）
const apiProxy = ['/auth', '/admin', '/quota',
      '/rank', '/datasources', '/data', '/verify'].reduce((acc, prefix) => {
  acc[prefix] = { target: 'http://localhost:8081', changeOrigin: true }
  return acc
}, {})

export default defineConfig({
  base: '/panel/',
  plugins: [vue()],
  server: {
    proxy: apiProxy,
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  }
})
