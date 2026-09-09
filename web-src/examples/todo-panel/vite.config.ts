import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// UI 插件产物:vite lib 模式(ES module 单入口 plugin.js;vue 打进产物,浏览器独立运行)。
export default defineConfig({
  plugins: [vue()],
  build: {
    lib: { entry: 'src/plugin.ts', formats: ['es'], fileName: () => 'plugin.js' },
    outDir: 'dist',
    emptyOutDir: true,
  },
})
