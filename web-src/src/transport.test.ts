// 事件通道链路状态单测(S-P1-3;node --test,零新增依赖)。
//
// 用假的 WebSocket / EventSource / sessionStorage 驱动真实 transport.ts:验证「链路状态上报」
// 与「主动重连」这两条对外契约(conn.ts 消费它们)。不测帧内容(那是 sse.test.ts 的职责)。
// node --test 每个文件独立进程 → 注入全局不污染其它测试。
import { test, mock, beforeEach, afterEach } from 'node:test'
import assert from 'node:assert/strict'

type Handler = ((e: unknown) => void) | null

class FakeWS {
  static instances: FakeWS[] = []
  url: string
  onopen: Handler = null
  onmessage: Handler = null
  onclose: Handler = null
  onerror: Handler = null
  readyState = 0
  closedByUs = false
  constructor(url: string) {
    this.url = url
    FakeWS.instances.push(this)
  }
  close(): void {
    this.readyState = 3
    this.onclose?.({}) // 浏览器语义:close() 后必触发 onclose
  }
  fireOpen(): void {
    this.readyState = 1
    this.onopen?.({})
  }
  fireError(): void {
    this.onerror?.({})
  }
  fireClose(): void {
    this.readyState = 3
    this.onclose?.({})
  }
  static last(): FakeWS {
    return FakeWS.instances[FakeWS.instances.length - 1]
  }
}

class FakeES {
  static instances: FakeES[] = []
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  url: string
  onopen: Handler = null
  onerror: Handler = null
  readyState = 0
  constructor(url: string) {
    this.url = url
    FakeES.instances.push(this)
  }
  addEventListener(t: string, fn: (e: unknown) => void): void {
    const arr = this.handlers.get(t) ?? []
    arr.push(fn)
    this.handlers.set(t, arr)
  }
  private handlers = new Map<string, ((e: unknown) => void)[]>()
  // fireFrame 模拟服务端下发一帧(event: <type> + data: JSON)
  fireFrame(type: string, frame: unknown): void {
    const data = JSON.stringify(frame)
    for (const fn of this.handlers.get(type) ?? []) fn({ data })
  }
  close(): void {
    this.readyState = 2
  }
  fireOpen(): void {
    this.readyState = 1
    this.onopen?.({})
  }
  fireError(closed = false): void {
    if (closed) this.readyState = 2
    this.onerror?.({})
  }
  static last(): FakeES {
    return FakeES.instances[FakeES.instances.length - 1]
  }
}

let store: Record<string, string> = {}
let states: string[] = []

function installGlobals(): void {
  store = {}
  states = []
  FakeWS.instances = []
  FakeES.instances = []
  const g = globalThis as unknown as Record<string, unknown>
  g.WebSocket = FakeWS
  g.EventSource = FakeES
  g.sessionStorage = {
    getItem: (k: string) => (k in store ? store[k] : null),
    setItem: (k: string, v: string) => {
      store[k] = v
    },
    removeItem: (k: string) => {
      delete store[k]
    },
  }
}

async function newTransport() {
  const mod = await import('./transport.ts')
  const t = mod.createTransport()
  t.onstate = (s: string) => states.push(s)
  return t
}

beforeEach(() => {
  mock.timers.enable({ apis: ['setTimeout'] })
  installGlobals()
})
afterEach(() => {
  mock.timers.reset()
})

test('WS 握手成功 → open;带 after 游标(断线续传基准)', async () => {
  store['gah.lastSeq'] = '42'
  await newTransport()
  FakeWS.last().fireOpen()
  assert.deepEqual(states, ['open'])
  assert.match(FakeWS.last().url, /after=42$/)
})

test('WS 断开 → reconnecting(退避重连,1s/2s),不发 closed', async () => {
  await newTransport()
  const ws0 = FakeWS.last()
  ws0.fireOpen()
  ws0.fireError()
  ws0.fireClose()
  assert.deepEqual(states, ['open', 'reconnecting', 'reconnecting'])
  // 退避 1s 后重连:产生新 socket
  mock.timers.tick(1000)
  assert.equal(FakeWS.instances.length, 2)
})

test('退避耗尽(3 次)→ 永久降级 EventSource,状态继续上报', async () => {
  await newTransport()
  const ws = FakeWS.last()
  ws.fireOpen()
  for (let i = 0; i < 3; i++) {
    FakeWS.last().fireClose()
    mock.timers.tick(8000) // 越过退避
  }
  // 第 4 次关闭触发降级(maxRetry=3)
  FakeWS.last().fireClose()
  assert.equal(FakeES.instances.length, 1)
  FakeES.last().fireOpen()
  assert.deepEqual(states, ['open', 'reconnecting', 'reconnecting', 'reconnecting', 'reconnecting', 'open'])
})

test('SSE 路径也记 after 游标,主动重建时带上(不带会被当成全新连接重复回放窗口)', async () => {
  const t = await newTransport()
  for (let i = 0; i < 4; i++) {
    FakeWS.last().fireClose()
    mock.timers.tick(8000)
  }
  const es0 = FakeES.last()
  assert.equal(es0.url, '/api/events') // 首连无游标 = 尾部窗口 + baseline
  es0.fireFrame('session', { id: 42, type: 'session', payload: {} })
  assert.equal(store['gah.lastSeq'], '42')
  es0.fireFrame('baseline', { id: 0, type: 'baseline', payload: {} })
  assert.equal(store['gah.lastSeq'], '42', '非会话帧(id=0)不动游标')
  t.reconnect()
  assert.equal(FakeES.last().url, '/api/events?after=42')
})

test('SSE 明确放弃(readyState=CLOSED)→ closed;短暂错误 → reconnecting', async () => {
  await newTransport()
  for (let i = 0; i < 4; i++) {
    FakeWS.last().fireClose()
    mock.timers.tick(8000)
  }
  states = []
  const es = FakeES.last()
  es.fireError() // 浏览器会自动重连
  assert.deepEqual(states, ['reconnecting'])
  es.fireError(true) // CLOSED:不再自动重连
  assert.deepEqual(states, ['reconnecting', 'closed'])
})

test('reconnect 重置退避并立即重连(睡眠/切网后不干等退避计时器)', async () => {
  const t = await newTransport()
  const ws0 = FakeWS.last()
  ws0.fireOpen()
  ws0.fireClose() // 进入退避等待
  states = []
  t.reconnect()
  assert.deepEqual(states, ['reconnecting'])
  // 不等 tick 也已重连(立即产生新 socket),旧 socket 已被主动关闭
  assert.equal(FakeWS.instances.length, 2)
  assert.equal(ws0.readyState, 3)
})

test('reconnect 在 SSE 降级路径下重建 EventSource(不等浏览器 3s 重试)', async () => {
  const t = await newTransport()
  for (let i = 0; i < 4; i++) {
    FakeWS.last().fireClose()
    mock.timers.tick(8000)
  }
  const es0 = FakeES.last()
  states = []
  t.reconnect()
  assert.deepEqual(states, ['reconnecting'])
  assert.equal(FakeES.instances.length, 2)
  assert.equal(es0.readyState, 2) // 旧 EventSource 已关闭
  FakeES.last().fireOpen()
  assert.deepEqual(states, ['reconnecting', 'open'])
})

test('close 之后旧的链路回调不再改状态(代际守门的通道侧保证)', async () => {
  const t = await newTransport()
  const ws = FakeWS.last()
  ws.fireOpen()
  t.close()
  states = []
  ws.fireError() // 关闭后迟到的 error
  ws.fireOpen()
  assert.deepEqual(states, [])
})

test('坏帧不影响链路状态(解析失败静默忽略)', async () => {
  const t = await newTransport()
  const ws = FakeWS.last()
  ws.fireOpen()
  const got: unknown[] = []
  t.on('session', (f) => got.push(f))
  ws.onmessage?.({ data: '{不是 JSON' })
  ws.onmessage?.({ data: '{"id":7,"type":"session","payload":{}}' })
  assert.equal(got.length, 1)
  assert.deepEqual(states, ['open'])
})
