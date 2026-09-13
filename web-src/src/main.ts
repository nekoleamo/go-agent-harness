import { createApp } from 'vue'
import App from './App.vue'
import './style.css'
import { installTips } from './tip'
import {
  registerDefault,
  registerExtraPanel,
} from './registry'
import { loadUIPlugins } from './plugins'
import StreamView from './components/StreamView.vue'
import InputBar from './components/InputBar.vue'
import StatusBar from './components/StatusBar.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'
import DocPanel from './components/doc/DocPanel.vue'
import { api } from './api'

// 宿主默认实现注册(优先级 0;外部 UI 插件可覆盖)
registerDefault('stream', StreamView)
registerDefault('input', InputBar)
registerDefault('statusbar', StatusBar)
registerDefault('confirm', ConfirmDialog)

// 文档预览工作台(D1,内置 extra-panel,priority 0 供外部 UI 插件同 key 覆盖):
// 先探测 /api/doc/tree 是否可用(host-docview 未装配 → 503),可用才注册侧栏入口。
async function registerDocPanel(): Promise<void> {
  try {
    await api.docTree('.', 1)
    registerExtraPanel('host-docview', { component: DocPanel, priority: 0, title: '文档预览' })
  } catch {
    /* 未装配文档服务:隐藏入口(不影响其它面板) */
  }
}

// data-tip 提示层(单例 fixed 层,防 overflow 裁剪/视口溢出)
installTips()

// 装配期加载外部 UI 插件(M7.2;失败静默保持默认实现,不阻塞界面)
void Promise.all([loadUIPlugins(), registerDocPanel()]).then(() => {
  createApp(App).mount('#app')
})
