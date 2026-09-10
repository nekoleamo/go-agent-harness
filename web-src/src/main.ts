import { createApp } from 'vue'
import App from './App.vue'
import './style.css'
import {
  registerDefault,
  registerExtraPanel,
  registerSidebarAction,
} from './registry'
import { loadUIPlugins } from './plugins'
import StreamView from './components/StreamView.vue'
import InputBar from './components/InputBar.vue'
import StatusBar from './components/StatusBar.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'
import DocPanel from './components/doc/DocPanel.vue'
import IMConnectPanel from './components/im/IMConnectPanel.vue'
import IMBadge from './components/im/IMBadge.vue'
import { upsertIMStatus } from './imstore'
import { api } from './api'
import { imSpec } from './imstore'

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

// IM 连接卡(E 组 E1/E2/E3,内置 extra-panel + 侧栏徽标):
// 先探测 /api/im/connect/spec(渠道未装配 → 503),可用才注册入口。
async function registerIMPanel(): Promise<void> {
  try {
    const spec = await api.imConnectSpec()
    imSpec.value = spec
    try {
      upsertIMStatus(await api.imConnectState())
    } catch {
      /* 状态兜底失败:保留 SSE 推送的状态 */
    }
    registerExtraPanel('im-connect', { component: IMConnectPanel, priority: 0, title: 'IM 通道' })
    registerSidebarAction('im-connect', { component: IMBadge, priority: 0, title: 'IM 通道' })
  } catch {
    /* 未装配 IM 连接服务:隐藏入口(不影响其它面板) */
  }
}

// 装配期加载外部 UI 插件(M7.2;失败静默保持默认实现,不阻塞界面)
void Promise.all([loadUIPlugins(), registerDocPanel(), registerIMPanel()]).then(() => {
  createApp(App).mount('#app')
})
