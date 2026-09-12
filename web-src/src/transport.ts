// 事件通道(M7.3):WS 优先,EventSource 自动降级。两者消费同一 JSON Frame(见 types.ts)。
// 通道 seam:后端 /api/events/ws(WS)与 /api/events(SSE)同 payload;断线续传统一经 after 游标
// (WS 重连带 after;SSE 由 Last-Event-ID 自动)。
import type { Frame } from './types'

export type TransportListener = (f: Frame) => void

export interface Transport {
  // onopen / onreconnecting / onclose 状态回调(UI 角标)
  onopen?: () => void
  onreconnecting?: () => void
  onclose?: () => void
  // 按帧类型订阅(服务端 event 名):见 web/events.go FrameXxx(session|status|error|confirm|command|question|doc|questiondone|confirmdone|schedule)
  on(type: string, fn: TransportListener): void
  close(): void
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

  constructor() {
    this.connect()
  }

  private connect(): void {
    // after 游标:最近一个会话帧 id(断线重连差集续传)
    const after = this.lastSeq()
    const url = `/api/events/ws${after > 0 ? '?after=' + after : ''}`
    const sock = new WebSocket(url)
    this.sock = sock
    sock.onopen = () => {
      if (this.closed) return
      this.retry = 0
      this.clearRetryTimer()
      this.onopen?.()
    }
    sock.onmessage = (e) => {
      try {
        const f = JSON.parse(String(e.data)) as Frame
        this.dispatch(f)
        if (f.id > 0) sessionStorage.setItem('gah.lastSeq', String(f.id))
      } catch {
        /* 坏帧忽略 */
      }
    }
    sock.onclose = () => {
      if (this.closed || this.es) return
      if (this.retry < WsTransport.maxRetry) {
        // 指数退避重连(短暂抖动/后端热重启数秒内回 WS)
        this.retry++
        this.onreconnecting?.()
        const delay = Math.min(1000 * 2 ** (this.retry - 1), 8000)
        this.retryTimer = setTimeout(() => {
          this.retryTimer = null
          this.connect()
        }, delay)
        return
      }
      // 持续失败 → 永久降级 EventSource(同 payload 续接;Last-Event-ID 自动差集)
      this.onreconnecting?.()
      this.es = new EsTransport(this.onopen, this.onreconnecting, this.onclose)
      for (const [t, fns] of this.listeners) {
        for (const fn of fns) this.es.on(t, fn)
      }
    }
    sock.onerror = () => {
      // 浏览器保证 onerror 后必触发 onclose(统一在那里调度重连/降级)
      this.onreconnecting?.()
    }
  }

  private clearRetryTimer(): void {
    if (this.retryTimer) {
      clearTimeout(this.retryTimer)
      this.retryTimer = null
    }
  }

  private lastSeq(): number {
    const v = Number(sessionStorage.getItem('gah.lastSeq'))
    return Number.isFinite(v) && v > 0 ? v : 0
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
  onopen?: () => void
  onreconnecting?: () => void
  onclose?: () => void

  constructor(open?: () => void, recon?: () => void, close?: () => void) {
    this.onopen = open
    this.onreconnecting = recon
    this.onclose = close
    this.connect()
  }

  private connect(): void {
    const es = new EventSource('/api/events')
    this.es = es
    es.onopen = () => this.onopen?.()
    es.onerror = () => {
      // 浏览器自动重连(带 Last-Event-ID);仅标志状态
      this.onreconnecting?.()
    }
    // SSE 的 event: <type> 对应帧类型
    // 帧类型白名单:必须覆盖 web/events.go 全部 FrameXxx(漏项 = 该帧在 SSE 降级路径被静默丢弃)
    for (const t of ['session', 'status', 'error', 'confirm', 'command', 'question', 'doc', 'questiondone', 'confirmdone', 'schedule']) {
      es.addEventListener(t, (e) => {
        try {
          const f = JSON.parse((e as MessageEvent).data) as Frame
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

  close(): void {
    this.es?.close()
    this.onclose?.()
  }
}
