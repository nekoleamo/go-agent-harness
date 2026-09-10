// IM 连接状态跨组件通道(E 组 E0/E1/E3):SSE `imconnect` 帧 → 模块级状态 → 连接卡/徽标渲染。
// 与 docstore 同型:App 是唯一 SSE 消费者,面板/徽标只读状态并发起 REST 动作。
import { ref } from 'vue'
import type { IMConnectSpec, IMConnectStatus } from './types'

// 当前渠道连接状态(相位/二维码/错误;不含凭证)
export const imStatus = ref<IMConnectStatus | null>(null)
// 渠道连接方式声明(渲染二维码卡还是表单)
export const imSpec = ref<IMConnectSpec | null>(null)

// OPEN_PANEL_EVENT 侧栏徽标 → 打开抽屉抽屉(解耦于 App 的 openPanel 状态)
export const OPEN_PANEL_EVENT = 'gah:open-panel'

// requestPanel 请求打开指定 key 的附加面板
export function requestPanel(key: string): void {
  window.dispatchEvent(new CustomEvent(OPEN_PANEL_EVENT, { detail: key }))
}

// upsertIMStatus 更新状态(SSE 帧 / 轮询兜底共用)
export function upsertIMStatus(st: IMConnectStatus | null): void {
  if (!st) return
  imStatus.value = st
}

// connected 是否已连接(徽标语义:done = 已连接;其余 = 未连接/进行中)
export function connected(): boolean {
  return imStatus.value?.phase === 'done'
}

// phaseLabel 相位 → 中文短标签
export function phaseLabel(phase?: string): string {
  switch (phase) {
    case 'done':
      return '已连接'
    case 'waiting_scan':
      return '待扫码'
    case 'scanned':
      return '已扫码'
    case 'expired_refresh':
      return '刷新二维码'
    case 'validating':
      return '校验中'
    case 'failed':
      return '失败'
    default:
      return '未连接'
  }
}
