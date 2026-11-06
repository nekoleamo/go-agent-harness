// 前端性能探针(**默认关闭**,零成本):把「一帧批处理 + Vue patch」的耗时记进环形缓冲。
//
// 为什么需要它(2026-11-06):`test/perf.test.mjs` 用 rAF 间隔量稳态追加成本,但那条指标
// 有一档盲区 —— 批处理耗时只要**还在帧预算内**(<16.7ms),帧间隔就恒定在 17ms,量不出
// 「14ms 略降」这种改善。而 docs/WEB_OPTIMIZE.md 要判的正是这一档(400 条窗口下 p50 就在
// 14ms 上下)。所以给它一个精确的读数:App 侧在批处理前后打点(结尾用 nextTick,把 Vue 的
// patch 也算进来),结果写 `window.__gahPatchMs`。
//
// 为什么不常开:这类探针一旦常驻,就会有人依赖它做断言 —— 而它量的是"当前实现"的耗时,
// 不是契约。开关只有一个入口:`window.__gahPerf = true`(由脚手架在 addInitScript 里设)。
// 关着的时候每次批处理只多一次函数调用 + 一次布尔判断。

const CAP = 600

/** 探针是否启用(唯一开关:`window.__gahPerf === true`)。 */
export function probeOn(): boolean {
  const w = (globalThis as unknown as { window?: { __gahPerf?: unknown } }).window
  return w?.__gahPerf === true
}

/** 起始时刻。performance 不可用(纯 node 单测)时退 Date.now()。 */
export function probeStart(): number {
  return typeof performance !== 'undefined' && typeof performance.now === 'function' ? performance.now() : Date.now()
}

/** 记一次批处理耗时(毫秒,四舍五入;只保留最近 CAP 次)。未启用时是 no-op。 */
export function probeRecord(ms: number): void {
  if (!probeOn()) return
  const w = (globalThis as unknown as { window: { __gahPatchMs?: number[] } }).window
  const arr = (w.__gahPatchMs ??= [])
  arr.push(Math.round(ms))
  if (arr.length > CAP) arr.splice(0, arr.length - CAP)
}
