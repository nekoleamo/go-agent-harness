// streamrows.ts 单测:meta 必须按发生位置交织,不能永远钉在末尾。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { rowsOf } from './streamrows.ts'
import type { Msg } from './sse.ts'
import type { MetaLine } from './registry.ts'

function msg(seq: number, text = 'm' + seq): Msg {
  return { kind: 'assistant', text, seq, ts: '' }
}
function meta(after: number, text = 'x'): MetaLine {
  return { kind: 'error', text, after }
}
function shape(rows: ReturnType<typeof rowsOf>): string[] {
  return rows.map((r) => (r.t === 'msg' ? `m${r.m.seq}` : `x:${r.mm.text}`))
}

test('回合中途产生的 meta 落在它该在的位置,不在末尾', () => {
  const frames = [msg(1), msg(2), msg(3)]
  const metas = [meta(2, '回合已中止')] // 停止回合时最后落定的是 seq=2
  assert.deepEqual(shape(rowsOf(frames, metas)), ['m1', 'm2', 'x:回合已中止', 'm3'])
})

test('同一位置的多行按产生顺序聚在一起', () => {
  const frames = [msg(1), msg(2)]
  const metas = [meta(1, 'a'), meta(1, 'b'), meta(2, 'c')]
  assert.deepEqual(shape(rowsOf(frames, metas)), ['m1', 'x:a', 'x:b', 'm2', 'x:c'])
})

test('会话流为空时产生的 meta 落在末尾,不会凭空插到最前', () => {
  const frames = [msg(1)]
  const metas = [meta(99, 'late')]
  assert.deepEqual(shape(rowsOf(frames, metas)), ['m1', 'x:late'])
  assert.deepEqual(shape(rowsOf([], [meta(0, 'first')])), ['x:first'])
})

test('没有 meta 时行为不变(纯消息流)', () => {
  const frames = [msg(1), msg(2)]
  assert.deepEqual(shape(rowsOf(frames, [])), ['m1', 'm2'])
  assert.deepEqual(shape(rowsOf([], [])), [])
})