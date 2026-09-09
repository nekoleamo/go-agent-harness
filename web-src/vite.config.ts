import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 产物固定落地 web/dist(主包 go:embed 依赖该路径;缺失主包构建失败)。
// CSP 安全:运行时纯静态、无外链,禁止内联脚本由构建产物天然规避(默认无 inline)。
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../web/dist',
    emptyOutDir: true,
    target: 'es2020',
    sourcemap: false,
  },
  server: {
    port: 2233,
    // 开发态:经 data.static_dir 指向 web-src/dist 由 Go 服务托管;
    // vite dev server 仅供源码 HMR(代理数据接口到 Go 服务)。
    proxy: {
      '/api': 'http://127.0.0.1:2233',
    },
  },
})
