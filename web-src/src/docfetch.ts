// 会话流 markdown 渲染的**共享**取件层(批二):缓存 + 并发闸。
//
// 为何要有(实测,2026-10-08):会话流里每条 assistant 消息挂一个 DocText,挂载即发一次
// `POST /api/doc/render`(`immediate: true`)。窗口 800 条 ⇒ 400 次请求、补齐 2.1s,
// 且**没有任何上限** —— 长会话每开一次窗就是一轮小洪水。三条现实路径都打在这上面:
// ① 首屏/上滚把几百条一次性挂上;② 流式期间 pending 块每 700ms 发一次;③ 会话来回切。
//
// 两条对策,都不碰后端(零契约、零依赖):
//   ① LRU 缓存:同一段文本只渲染一次。会话切换来回、历史重放、流式 pending 反复增长
//      都会命中(流式时文本逐次不同 ⇒ 命中率低,但落定后的那条与 pending 最后一次相同)。
//   ② 并发闸:同时在飞的请求封顶。浏览器对同源并发本就有上限,但那是排队不是拒绝,
//      且每个在途请求都占一份响应体;显式封顶让「几百条一次性挂载」变成平滑排队。
export interface RenderResult {
  blocks: unknown[] | null
}

// textKey 文本键:FNV-1a 32 位 + 长度。全量文本当键会让 Map 持有整段正文(内存),
// 哈希冲突概率对本场景可忽略(且冲突只导致「渲染错一次」,不会静默丢内容 —— 下次换文本即换键)。
export function textKey(text: string): string {
  let h = 0x811c9dc5
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return text.length + ':' + (h >>> 0).toString(16)
}

// LruCache 定容 LRU。Map 的插入序即访问序:命中后 delete+set 把它挪到末尾,
// 淘汰时取最早的键。容量按条数(渲染结果本身的大小差异极大,按字节定量更复杂且不必要)。
export class LruCache<V> {
  private m = new Map<string, V>()
  private readonly max: number

  constructor(max: number) {
    this.max = max
  }

  get(k: string): V | undefined {
    const v = this.m.get(k)
    if (v === undefined) return undefined
    this.m.delete(k)
    this.m.set(k, v)
    return v
  }

  set(k: string, v: V): void {
    if (this.m.has(k)) this.m.delete(k)
    this.m.set(k, v)
    while (this.m.size > this.max) {
      const oldest = this.m.keys().next()
      if (oldest.done) break
      this.m.delete(oldest.value)
    }
  }

  /** 淘汰落在某键上的条目(会话切走等需要主动腾地方时用)。 */
  delete(k: string): void {
    this.m.delete(k)
  }

  clear(): void {
    this.m.clear()
  }

  get size(): number {
    return this.m.size
  }
}

/**
 * 并发闸:同时在飞不超过 limit 个,超出排队。
 * 不用第三方信号量(依赖红线)。释放时顺带动下一条,失败也照样放行(闸不许吞错)。
 */
export class Gate {
  private active = 0
  private queue: (() => void)[] = []
  private readonly limit: number

  constructor(limit: number) {
    this.limit = limit
  }

  get inFlight(): number {
    return this.active
  }

  get waiting(): number {
    return this.queue.length
  }

  async run<T>(fn: () => Promise<T>): Promise<T> {
    if (this.active >= this.limit) await new Promise<void>((ok) => this.queue.push(ok))
    this.active++
    try {
      return await fn()
    } finally {
      this.active--
      const next = this.queue.shift()
      if (next) next()
    }
  }
}

/**
 * 渲染取件器:缓存命中直接返回;未命中经并发闸发请求,成功后入缓存。
 * 失败**不入缓存**(下一轮还要能重试),由调用方决定降级。
 */
export class DocRenderClient {
  private readonly cache: LruCache<RenderResult>
  private readonly gate: Gate
  private readonly fetchText: (text: string) => Promise<RenderResult>

  constructor(fetchText: (text: string) => Promise<RenderResult>, opts?: { cacheSize?: number; concurrency?: number }) {
    this.fetchText = fetchText
    this.cache = new LruCache<RenderResult>(opts?.cacheSize ?? 256)
    this.gate = new Gate(opts?.concurrency ?? 4)
  }

  render(text: string): Promise<RenderResult> {
    const k = textKey(text)
    const hit = this.cache.get(k)
    if (hit) return Promise.resolve(hit)
    return this.gate.run(async () => {
      // 等闸的这段时间里可能已被别的请求渲染好了 —— 再查一次,别重复发。
      const again = this.cache.get(k)
      if (again) return again
      const r = await this.fetchText(text)
      this.cache.set(k, r)
      return r
    })
  }

  /** 命中率/容量观测(诊断与测试用)。 */
  stats(): { size: number; inFlight: number; waiting: number } {
    return { size: this.cache.size, inFlight: this.gate.inFlight, waiting: this.gate.waiting }
  }
}