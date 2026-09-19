import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// UI 插件示例(B5/v2 扩展点):三槽位多入口(setting-section/sidebar-action/extra-panel)。
// v1 覆盖槽位(stream/input/statusbar/confirm)与 v2 扩展点共用产物机制(vite lib 多入口)。
export default defineConfig({
  plugins: [vue()],
  // lib 模式默认**保留** process.env.NODE_ENV(vite 面向库消费者);产物是在浏览器里 import 的,
  // 没有 process → "process is not defined" 把插件整个拖死(只留 console.warn 静默失效)。
  define: { 'process.env.NODE_ENV': JSON.stringify('production') },
  build: {
    lib: {
      entry: {
        section: 'src/section.ts',
        action: 'src/action.ts',
        panel: 'src/panel.ts',
      },
      formats: ['es'],
      fileName: (_format, name) => `${name}.js`,
    },
    outDir: 'dist',
    emptyOutDir: true,
  },
})
