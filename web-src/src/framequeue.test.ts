// 帧合帧队列单测(批零)。纯逻辑直跑(node --test,无 DOM):
// 断言「一批只批处理一次」「顺序即到达顺序」「clear 后空批 no-op」「排期只发生一次」。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { FrameQueue, rafScheduler } from './framequeue.ts'

// 同步执行器:排期即执行(单测里等价于「一帧内来多少都合并成一批」)
function sync() {
  const batches: number[][] = []
  const pending: (() => void)[] = []
  let schedules = 0
  const q = new FrameQueue<number>(
    (b) => batches.push(b),
    (fn) => {
      schedules++
      pending.push(fn)
    },
  )
  return {
    q,
    batches,
    get schedules() {
      return schedules
    },
    runPending() {
      while (pending.length) pending.shift()!()
    },
  }
}

test('一批内多次 push 只批处理一次', () => {
  const h = sync()
  for (let i = 0; i < 800; i++) h.q.push(i)
  assert.equal(h.q.size(), 800, '全部积压')
  assert.equal(h.schedules, 1, '排期只发生一次 —— 这是 59× 的来源')
  assert.equal(h.batches.length, 0, '排期不等于执行')
  h.runPending()
  assert.equal(h.batches.length, 1, '只批处理一次')
  assert.equal(h.batches[0].length, 800, '一批拿到全部 800 帧')
  assert.equal(h.q.size(), 0, '批处理后清空')
})

test('批内顺序 = 到达顺序', () => {
  const h = sync()
  for (let i = 0; i < 5; i++) h.q.push(i)
  h.runPending()
  assert.deepEqual(h.batches[0], [0, 1, 2, 3, 4])
})

test('批处理过程中新到的帧进下一批,不丢不重', () => {
  const h = sync()
  h.q.push(1)
  h.q.push(2)
  h.runPending()
  // 批处理回调里再推(模拟 flush 期间又到帧)
  h.q.push(3)
  h.runPending()
  assert.deepEqual(h.batches, [[1, 2], [3]])
})

test('clear 后已排期的回调是空批 no-op', () => {
  const h = sync()
  h.q.push(1)
  h.q.clear()
  assert.equal(h.q.size(), 0)
  h.runPending()
  assert.deepEqual(h.batches, [], '会话切换丢弃积压后不得白消费一次')
})

test('空队列 flush 不跑回调', () => {
  const h = sync()
  h.q.flush()
  assert.deepEqual(h.batches, [])
})

test('flush 后能再次排期(不卡死)', () => {
  const h = sync()
  h.q.push(1)
  h.runPending()
  h.q.push(2)
  assert.equal(h.schedules, 2)
  h.runPending()
  assert.deepEqual(h.batches, [[1], [2]])
})

test('rafScheduler:页面可见走 rAF,隐藏走 setTimeout', async () => {
  const calls: string[] = []
  const g = globalThis as unknown as { requestAnimationFrame: (fn: () => void) => number }
  const origRaf = g.requestAnimationFrame
  g.requestAnimationFrame = (fn) => {
    calls.push('raf')
    fn()
    return 1
  }
  try {
    // 可见:走 rAF
    rafScheduler({ hidden: false } as Document)(() => {})
    assert.deepEqual(calls, ['raf'])
    // 隐藏:rAF 不触发(浏览器行为),必须退化成 setTimeout,否则后台攒帧
    calls.length = 0
    rafScheduler({ hidden: true } as Document)(() => calls.push('flush'))
    assert.deepEqual(calls, [], '排期时不立即跑')
    await new Promise((r) => setTimeout(r, 5))
    assert.deepEqual(calls, ['flush'], '隐藏时靠 setTimeout 兜底执行')
  } finally {
    g.requestAnimationFrame = origRaf
  }
})