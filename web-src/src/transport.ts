// 事件通道(M7.3):WS 优先,EventSource 自动降级。两者消费同一 JSON Frame(见 types.ts)。
// 通道 seam:后端 /api/events/ws(WS)与 /api/events(SSE)同 payload;断线续传统一经 after 游标
// (两条路都显式带 ?after=;SSE 另接受浏览器自动重连时自带的 Last-Event-ID)。
import type { Frame } from './types'

export type TransportListener = (f: Frame) => void

// 续传游标:最近一个会话帧 id(sessionStorage;页面刷新/切会话时 App 会清掉 → 回到首连语义)。
function afterCursor(): number {
  try {
    const v = Number(sessionStorage.getItem('gah.lastSeq'))
    return Number.isFinite(v) && v > 0 ? v : 0
  } catch {
    return 0 // 无痕模式/无 sessionStorage
  }
}
// 记下游标(仅会话帧有 id;非会话帧 id=0 不参与)
function markCursor(id: number): void {
  if (id <= 0) return
  try {
    sessionStorage.setItem('gah.lastSeq', String(id))
  } catch {
    /* 无痕模式忽略 */
  }
}

// 链路事件(S-P1-3):由 conn.ts 的状态机消费 —— 通道只报可观测事实(
// 握手成功 / 正在重连 / 放弃重连),不自行判定「离线」(用户可见语义在状态机里)。
export type LinkState = 'open' | 'reconnecting' | 'closed'

export interface Transport {
  // onopen / onreconnecting / onclose 状态回调(UI 角标)
  onopen?: () => void
  onreconnecting?: () => void
  onclose?: () => void
  // onstate 统一链路状态(优先于三个细分回调;两者可共存)
  onstate?: (s: LinkState) => void
  // 按帧类型订阅(服务端 event 名):见 web/events.go FrameXxx(session|status|error|confirm|command|question|doc|questiondone|confirmdone|schedule|diff|baseline)
  on(type: string, fn: TransportListener): void
  // reconnect 主动重建链路(唤醒/切网/用户点「重试连接」):重置退避计数与降级态,
  // 立即重连一次(不用等退避计时器)。
  reconnect(): void
  close(): void
}

// notifyState 统一上报链路的可观测状态(三个细分回调 + onstate)。
function notifyState(t: Transport, s: LinkState): void {
  t.onstate?.(s)
  if (s === 'open') t.onopen?.()
  else if (s === 'reconnecting') t.onreconnecting?.()
  else t.onclose?.()
}

// createTransport 建立事件通道:优先 WebSocket(握手失败/异常降级 EventSource)。
export function createTransport(): Transport {
  if (typeof WebSocket !== 'undefined') {
    const ws = new WsTransport()
    // 尝试连接;失败即降级(ws.onerror 触发前不可感知,降级由 WsTransport 内部完成并回调)
    return ws
  }
  return new EsTransport()
}

// WsTransport WebSocket 承载。断开后指数退避重连(1s/2s/4s,带 after 游标
// 差集续传);持续失败(约 7s)才永久降级 EventSource(浏览器自动重连自愈)。
class WsTransport implements Transport {
  private sock: WebSocket | null = null
  private listeners = new Map<string, TransportListener[]>()
  private closed = false
  private es: EsTransport | null = null
  private retry = 0
  private retryTimer: ReturnType<typeof setTimeout> | null = null
  // 降级前重连次数(退避 1s/2s/4s;超过 → 永久降级 SSE)
  private static readonly maxRetry = 3
  onopen?: () => void
  onreconnecting?: () => void
  onclose?: () => void
  onstate?: (s: LinkState) => void

  constructor() {
    this.connect()
  }

  private report(s: LinkState): void {
    notifyState(this, s)
  }

  private connect(): void {
    // after 游标:最近一个会话帧 id(断线重连差集续传)
    const after = afterCursor()
    const url = `/api/events/ws${after > 0 ? '?after=' + after : ''}`
    const sock = new WebSocket(url)
    this.sock = sock
    sock.onopen = () => {
      // sock !== this.sock = 旧 socket(已被 reconnect/降级取代)的迟到事件,一律丢弃
      if (this.closed || sock !== this.sock) return
      this.retry = 0
      this.clearRetryTimer()
      this.report('open')
    }
    sock.onmessage = (e) => {
      try {
        const f = JSON.parse(String(e.data)) as Frame
        this.dispatch(f)
        markCursor(f.id)
      } catch {
        /* 坏帧忽略 */
      }
    }
    sock.onclose = () => {
      if (this.closed || this.es || sock !== this.sock) return
      if (this.retry < WsTransport.maxRetry) {
        // 指数退避重连(短暂拖拽/后端热重启数秒内回 WS)
        this.retry++
        this.report('reconnecting')
        const delay = Math.min(1000 * 2 ** (this.retry - 1), 8000)
        this.retryTimer = setTimeout(() => {
          this.retryTimer = null
          this.connect()
        }, delay)
        return
      }
      // 持续失败 → 永久降级 EventSource(同 payload 续接;Last-Event-ID 自动差集)
      this.report('reconnecting')
      this.es = new EsTransport(this.onopen, this.onreconnecting, this.onclose, this.onstate)
      for (const [t, fns] of this.listeners) {
        for (const fn of fns) this.es.on(t, fn)
      }
    }
    sock.onerror = () => {
      // 浏览器保证 onerror 后必触发 onclose(统一在那里调度重连/降级)
      if (this.closed || sock !== this.sock) return
      this.report('reconnecting')
    }
  }

  private clearRetryTimer(): void {
    if (this.retryTimer) {
      clearTimeout(this.retryTimer)
      this.retryTimer = null
    }
  }

  on(type: string, fn: TransportListener): void {
    if (this.es) {
      this.es.on(type, fn)
      return
    }
    const arr = this.listeners.get(type) ?? []
    arr.push(fn)
    this.listeners.set(type, arr)
  }

  private dispatch(f: Frame): void {
    for (const fn of this.listeners.get(f.type) ?? []) fn(f)
  }

  reconnect(): void {
    if (this.closed) return
    this.clearRetryTimer()
    this.retry = 0
    // 已降级到 SSE:重建 EventSource(浏览器内置重连可能要等 3s)
    if (this.es) {
      this.es.reconnect()
      return
    }
    // 关掉可能半死(睡眠/切网后 TCP 已断但 JS 未感知)的旧 socket,重新握一次。
    // 先摘掉 this.sock 再 close:否则 close 同步触发的 onclose 会再排一次退避重连
    // (双份 socket + 双份状态上报)。
    const old = this.sock
    this.sock = null
    try {
      old?.close()
    } catch {
      /* 已关团 */
    }
    this.report('reconnecting')
    this.connect()
  }

  close(): void {
    this.closed = true
    this.clearRetryTimer()
    this.sock?.close()
    this.es?.close()
  }
}

// EsTransport EventSource 承载(降级路径;同接口)。
class EsTransport implements Transport {
  private es: EventSource | null = null
  private listeners = new Map<string, TransportListener[]>()
  private closed = false
  onopen?: () => void
  onreconnecting?: () => void
  onclose?: () => void
  onstate?: (s: LinkState) => void

  constructor(open?: () => void, recon?: () => void, close?: () => void, state?: (s: LinkState) => void) {
    this.onopen = open
    this.onreconnecting = recon
    this.onclose = close
    this.onstate = state
    this.connect()
  }

  private report(s: LinkState): void {
    notifyState(this, s)
  }

  private connect(): void {
    // S-P1-2:显式带上 after 游标 —— 自己重建的 EventSource 不会带 Last-Event-ID,
    // 不带游标就会被服务端当成「全新连接」重放尾部窗口 → 已有消息被重复追加。
    const after = afterCursor()
    const es = new EventSource('/api/events' + (after > 0 ? '?after=' + after : ''))
    this.es = es
    es.onopen = () => {
      if (this.closed || es !== this.es) return
      this.report('open')
    }
    es.onerror = () => {
      if (this.closed || es !== this.es) return
      // readyState=CLOSED 意味着浏览器已放弃(不再自动重连):明确上报,不再假装重连中
      if (es.readyState === EventSource.CLOSED) this.report('closed')
      else this.report('reconnecting')
    }
    // SSE 的 event: <type> 对应帧类型
    // 帧类型白名单:必须覆盖 web/events.go 全部 FrameXxx(漏项 = 该帧在 SSE 降级路径被静默丢弃)
    for (const t of ['session', 'status', 'error', 'confirm', 'command', 'question', 'doc', 'questiondone', 'confirmdone', 'schedule', 'diff', 'baseline', 'notice']) {
      es.addEventListener(t, (e) => {
        try {
          const f = JSON.parse((e as MessageEvent).data) as Frame
          markCursor(f.id)
          for (const fn of this.listeners.get(t) ?? []) fn(f)
        } catch {
          /* 坏帧忽略 */
        }
      })
    }
  }

  on(type: string, fn: TransportListener): void {
    const arr = this.listeners.get(type) ?? []
    arr.push(fn)
    this.listeners.set(type, arr)
  }

  reconnect(): void {
    if (this.closed) return
    // 浏览器内置重连可能要等 3s(retry: 3000);主动重建一次,唤醒后不等
    this.closeSocket()
    this.report('reconnecting')
    this.connect()
  }

  private closeSocket(): void {
    const old = this.es
    this.es = null // 先摘引用:关闭后到达的回调不再被当成当前链路
    try {
      old?.close()
    } catch {
      /* 已关团 */
    }
  }

  close(): void {
    this.closed = true
    this.closeSocket()
    this.report('closed')
  }
}
