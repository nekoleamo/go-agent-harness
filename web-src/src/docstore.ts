// 文档预览跨组件请求通道(D1):工具行「预览」按钮 → 打开工作台并定位文件。
// 单一意图:App 监听窗口事件打开抽屉,DocPanel 观察 request 值加载该文件。
import { ref } from 'vue'

// 待打开的文件路径(相对工作区;空 = 无请求)
export const docRequest = ref('')

// OPEN_DOC_EVENT 会话流/侧栏发起预览意图时派发(解耦于抽屉归属组件)
export const OPEN_DOC_EVENT = 'gah:open-doc'

// requestDoc 发起预览意图(路径为工作区相对路径)
export function requestDoc(path: string): void {
  if (!path) return
  window.dispatchEvent(new CustomEvent(OPEN_DOC_EVENT, { detail: path }))
}

// 可预览扩展名白名单(纯前端判定:工具行是否显示「预览」按钮;后端仍是权威)
const PREVIEWABLE = /\.(md|markdown|txt|log|csv|tsv|json|ya?ml|toml|ini|go|rs|py|js|ts|tsx|jsx|vue|java|c|cc|cpp|h|hpp|cs|rb|php|sql|sh|bash|zsh|html?|png|jpe?g|gif|webp|svg|pdf|docx?|xlsx?|pptx?|ipynb)$/i

// previewablePathOf 从工具参数摘要中取 path=<值> 并做扩展名白名单(取不到返回空)
export function previewablePathOf(args: string): string {
  if (!args) return ''
  const m = /(?:^|\s)path=("?)([^\s"]+)\1/.exec(args)
  if (!m) return ''
  return PREVIEWABLE.test(m[2]) ? m[2] : ''
}
