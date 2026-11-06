// perfprobe 的单测:它是**默认关闭**的探针,最容易坏的地方恰好是"关着的时候别记"。
// node --test 每个文件独立进程 ⇒ 往 globalThis 挂 window 不污染其它用例。
import { test, beforeEach } from 'node:test'
import assert from 'node:assert/strict'

const g = globalThis as unknown as { window?: Record<string, unknown> }

beforeEach(() => {
  delete g.window
})

test('默认关闭:没有 window.__gahPerf 时探测不启用、也不记录', async () => {
  const { probeOn, probeRecord } = await import('./perfprobe.ts')
  g.window = {}
  assert.equal(probeOn(), false)
  probeRecord(12)
  assert.equal(g.window.__gahPatchMs, undefined, '关闭态绝不该写缓冲')
})

test('只有 __gahPerf === true 才算开(字符串 "true"/1 都不算)', async () => {
  const { probeOn } = await import('./perfprobe.ts')
  for (const v of ['true', 1, {}, null]) {
    g.window = { __gahPerf: v }
    assert.equal(probeOn(), false, `__gahPerf=${JSON.stringify(v)} 不该被当成开`)
  }
  g.window = { __gahPerf: true }
  assert.equal(probeOn(), true)
})

test('记录是四舍五入的整数,且环形缓冲按上限截断(最老的先丢)', async () => {
  const { probeRecord } = await import('./perfprobe.ts')
  g.window = { __gahPerf: true }
  probeRecord(3.4)
  probeRecord(3.6)
  assert.deepEqual(g.window.__gahPatchMs, [3, 4])
  for (let i = 0; i < 700; i++) probeRecord(i)
  const buf = g.window.__gahPatchMs as number[]
  assert.equal(buf.length, 600, '上限 600')
  assert.equal(buf[buf.length - 1], 699, '最新的在尾部')
})

test('probeStart 在没有 performance 的环境也能用(退 Date.now)', async () => {
  const { probeStart } = await import('./perfprobe.ts')
  const t = probeStart()
  assert.equal(typeof t, 'number')
  assert.ok(t > 0)
})
