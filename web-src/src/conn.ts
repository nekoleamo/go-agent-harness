// 连接健康状态机(S-P1-3,对标 Codex v0.153 的断连显式语义)。
//
// 与 transport.ts 的分工:transport 负责**怎么连**(WS 优先 / SSE 降级 / 退避重连),本模块负责
// **当前算不算可用**,以及由此推导的 UI 语义(横幅文案、是否允许提交)。纯函数、无副作用:可单测,
// 不依赖 DOM/网络。
//
// 三态定义(刻意区分「短暂抖动」与「明确断开」):
//   open         链路已确认可用(WS open 或 SSE open)
//   reconnecting 链路正在自我修复(退避重连中 / 浏览器自动重连中)。**仍允许提交** ——
//                REST 上行与事件通道是两条独立连接,WS 抖动不代表服务端不可达;
//   offline      明确不可用:浏览器报告无网络、SSE 已放弃重连、或主动探活失败。
//                → 禁止提交、显示横幅、保留草稿与附件。
//
// 纪律:降级判定必须来自**可观测事实**(navigator.onLine / readyState / 探活结果),不用
// 「多久没收到帧」这类猜测 —— 安静会话本来就可能几分钟没有事件。
export type ConnState = 'open' | 'reconnecting' | 'offline'

export interface ConnModel {
  state: ConnState
  // 浏览器网络层报告(navigator.onLine);false 时任何链路事件都不足以判定 open
  netOnline: boolean
}

// ConnEv 状态机输入:
//  link-open      链路握手成功(WS open / SSE open)
//  link-retrying  链路正在重连(WS 退避中 / SSE error,浏览器会自动重连)
//  link-closed    链路放弃(SSE readyState=CLOSED,浏览器不再自动重连)
//  net            浏览器网络状态变化(navigator.onLine / online/offline 事件)
//  probe-ok/fail  主动探活(GET /api/state)
export type ConnEv =
  | { k: 'link-open' }
  | { k: 'link-retrying' }
  | { k: 'link-closed' }
  | { k: 'net'; online: boolean }
  | { k: 'probe'; ok: boolean }

export function newConn(): ConnModel {
  // 初始乐观:页面刚加载,链路状态未知(navigator.onLine 为 false 时首帧即降级)
  const net = navigatorOnline()
  return { state: net ? 'open' : 'offline', netOnline: net }
}

export function navigatorOnline(): boolean {
  // 无 navigator(SSR/测试)按在线处理:不知道该不该拦时不要拦
  if (typeof navigator === 'undefined') return true
  return navigator.onLine !== false
}

// connReduce 状态迁移;无变化时返回**原对象**(Vue 引用比较,避免无谓重渲染)。
export function connReduce(m: ConnModel, ev: ConnEv): ConnModel {
  let next = m.state
  const net = m.netOnline
  switch (ev.k) {
    case 'net':
      return next2(m, ev.online ? (m.netOnline ? m.state : 'reconnecting') : 'offline', ev.online)
    case 'link-open':
      // 浏览器说没网时,链路事件不足以判定可用(桌面壳睡眠后常见:旧 socket 自认为还开着)
      next = net ? 'open' : 'offline'
      break
    case 'link-retrying':
      next = net ? 'reconnecting' : 'offline'
      break
    case 'link-closed':
      next = 'offline'
      break
    case 'probe':
      // 探活只回答「服务端此刻可达吗」(HTTP 上行):成功则解除离线拦截,但事件通道未确认
      // 前保持 reconnecting(不谎报已连接);失败即降级。
      if (!ev.ok || !net) next = 'offline'
      else next = m.state === 'offline' ? 'reconnecting' : m.state
      break
  }
  return next2(m, next, net)
}

function next2(m: ConnModel, state: ConnState, netOnline: boolean): ConnModel {
  if (m.state === state && m.netOnline === netOnline) return m
  return { state, netOnline }
}

// submitAllowed 是否允许提交回合/远端动作(仅明确断开时拦截)。
export function submitAllowed(m: ConnModel): boolean {
  return m.state !== 'offline'
}

// connLabel 链路的短标签(状态栏/横幅共用同一口径);收 ConnState 而非模型,
// 便于只拿得到状态字符串的组件(StatusBar)复用。
export function connLabel(state: ConnState): string {
  switch (state) {
    case 'open':
      return '已连接'
    case 'reconnecting':
      return '重连中'
    default:
      return '已断开'
  }
}

// offlineHint 离线横幅文案(非离线返回空串 → 不渲染)。
export function offlineHint(m: ConnModel): string {
  if (m.state !== 'offline') return ''
  return m.netOnline
    ? '与服务端的连接已断开。草稿与附件已保留,恢复后请手动重新发送。'
    : '当前网络不可用。草稿与附件已保留,网络恢复后请手动重新发送。'
}
