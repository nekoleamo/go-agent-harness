import { createApp } from 'vue'
import App from './App.vue'
import './style.css'
import {
  registerDefault,
} from './registry'
import { loadUIPlugins } from './plugins'
import StreamView from './components/StreamView.vue'
import InputBar from './components/InputBar.vue'
import StatusBar from './components/StatusBar.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'

// 宿主默认实现注册(优先级 0;外部 UI 插件可覆盖)
registerDefault('stream', StreamView)
registerDefault('input', InputBar)
registerDefault('statusbar', StatusBar)
registerDefault('confirm', ConfirmDialog)

// 装配期加载外部 UI 插件(M7.2;失败静默保持默认实现,不阻塞界面)
void loadUIPlugins().then(() => {
  createApp(App).mount('#app')
})
