// 帧合帧队列:把一个动画帧内到达的多个帧合并成一次批处理。
//
// 为何要它(实测,2026-10-08):会话帧此前是「到达一条即同步消费一条」,每条都触发一次
// 消息列表的 Vue patch。keyed diff 会跳过未变节点但仍要遍历整棵列表做 key 比对 ⇒ 追加一条
// 是 O(n);首屏回放 400 个事件(200 条消息)就是 200 × O(n)。实测 800 条消息的首屏是
// **单个 7.9s 主线程长任务**(页面白屏到出内容),翻倍即 4 倍 = O(n²)。
// 改成「入队 → 一个动画帧内批量消费」后,一帧只 patch 一次 ⇒ O(n),实测同场景 135ms。
//
// 为什么调度器要注入而不是内部直接调 rAF:
//   ① 单测(node --test,无 DOM)可传同步执行器,断言批次数与顺序,不依赖真浏览器;
//   ② 页面隐藏时 rAF 不会触发 —— 靠注入点让宿主换成 setTimeout 兜底,否则后台标签页
//      会一直攒帧(内存涨),回前台再一次性 O(n²) 爆出来。
export class FrameQueue<T> {
  private buf: T[] = []
  private scheduled = false
  private readonly run: (batch: T[]) => void
  private readonly schedule: (fn: () => void) => void

  /**
   * @param run 批处理回调:收到一个动画帧内累积的全部帧(顺序即到达顺序)
   * @param schedule 调度器:把回调排到下一个「批处理时机」(浏览器传 rAF,隐藏时传 setTimeout)
   */
  constructor(run: (batch: T[]) => void, schedule: (fn: () => void) => void) {
    this.run = run
    this.schedule = schedule
  }

  /** 入队一帧。已排期则只追加 —— 同一批里来多少帧都只批处理一次。 */
  push(x: T): void {
    this.buf.push(x)
    if (this.scheduled) return
    this.scheduled = true
    this.schedule(() => this.flush())
  }

  /**
   * 立即批处理当前积压(排期回调也走这里)。
   * 空队列是 no-op:clear() 之后已排期的回调照样会来,不该白跑一次消费。
   */
  flush(): void {
    this.scheduled = false
    if (this.buf.length === 0) return
    const batch = this.buf
    this.buf = []
    this.run(batch)
  }

  /** 丢弃积压(会话切换 / 连接重建)。已排期的回调仍会来,但那是空批 no-op。 */
  clear(): void {
    this.buf = []
  }

  /** 当前积压条数(诊断与单测用)。 */
  size(): number {
    return this.buf.length
  }
}

/**
 * 批处理调度器(浏览器侧):可见时用 requestAnimationFrame 与渲染对齐;页面隐藏时 rAF
 * 不触发,退化成 setTimeout(0) —— 否则后台标签页会一直攒帧。
 * 不用 setTimeout(常数)的理由同上:rAF 与下一帧绘制对齐,少一次「改了但还没画」的中间态。
 */
export function rafScheduler(doc: Document | undefined = typeof document === 'undefined' ? undefined : document): (fn: () => void) => void {
  return (fn) => {
    if (doc && doc.hidden) setTimeout(fn, 0)
    else requestAnimationFrame(fn)
  }
}