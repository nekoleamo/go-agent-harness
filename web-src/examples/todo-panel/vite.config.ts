import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// UI 插件产物:vite lib 模式(ES module 单入口 plugin.js;vue 打进产物,浏览器独立运行)。
export default defineConfig({
  plugins: [vue()],
  // lib 模式默认**保留** process.env.NODE_ENV(vite 面向库消费者);产物是在浏览器里 import 的,
  // 没有 process → "process is not defined" 把插件整个拖死(只留 console.warn 静默失效)。
  define: { 'process.env.NODE_ENV': JSON.stringify('production') },
  build: {
    lib: { entry: 'src/plugin.ts', formats: ['es'], fileName: () => 'plugin.js' },
    outDir: 'dist',
    emptyOutDir: true,
  },
})
