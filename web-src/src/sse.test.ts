// 会话流消费引擎单测(E-E;node --test,零新增依赖)。
// 覆盖:参数/结果摘要截断、user/assistant/tool 落定、工具行状态与快照同步、
// 异常到达顺序(结果先于调用)、取消/摘要 meta 行、usage 判定。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { argsSummary, consume, isUsage, newModel, resultSummary } from './sse.ts'
import type { StreamModel } from './sse.ts'
import type { SessionEvent } from './types.ts'

const ev = (Kind: string, Payload: unknown, Seq = 1): SessionEvent => ({ Kind, Seq, Payload, TS: '2026-10-11T00:00:00Z' })

test('argsSummary:JSON 取前 3 键并截断,非 JSON 原样截断', () => {
  assert.equal(argsSummary('{"a":1,"b":"x"}'), 'a=1 b=x')
  assert.equal(argsSummary('{"a":1,"b":2,"c":3,"d":4}'), 'a=1 b=2 c=3')
  const long = JSON.stringify({ q: 'x'.repeat(200) })
  assert.ok(argsSummary(long).length <= 81 && argsSummary(long).endsWith('…'))
  assert.equal(argsSummary('not-json'), 'not-json')
  assert.ok(argsSummary('y'.repeat(200)).length <= 81)
})

test('resultSummary:短文本原样,长文本截断并保留全文', () => {
  const short = resultSummary('ok')
  assert.deepEqual(short, { text: 'ok', full: 'ok' })
  const long = resultSummary('z'.repeat(1000))
  assert.equal(long.full.length, 1000)
  assert.ok(long.text.length < 500 && long.text.endsWith('…'))
})

test('user/message 带附件入流', () => {
  const m = newModel()
  consume(m, ev('user/message', { Content: '你好', Attachments: [{ Name: 'a.png' }] }))
  assert.equal(m.msgs.length, 1)
  assert.equal(m.msgs[0].kind, 'user')
  assert.equal(m.msgs[0].text, '你好')
  assert.equal(m.msgs[0].atts?.length, 1)
})

test('assistant chunk 累积 → message 落定', () => {
  const m = newModel()
  consume(m, ev('assistant/chunk', { Delta: '你' }, 1))
  consume(m, ev('assistant/chunk', { Delta: '好' }, 2))
  assert.equal(m.pending, '你好')
  consume(m, ev('assistant/message', { Content: '你好', ToolCalls: [] }, 3))
  assert.equal(m.pending, '')
  assert.equal(m.msgs.at(-1)?.kind, 'assistant')
  assert.equal(m.msgs.at(-1)?.text, '你好')
})

test('工具调用 → 结果:状态/摘要/快照同步', () => {
  const m = newModel()
  consume(m, ev('tool/call', { ID: 't1', Name: 'shell', Arguments: '{"command":"ls"}' }, 1))
  assert.equal(m.pendingTool?.id, 't1')
  consume(m, ev('assistant/message', { Content: '', ToolCalls: [{ ID: 't1', Name: 'shell', Arguments: '{"command":"ls"}' }] }, 2))
  const as = m.msgs.at(-1)!
  assert.equal(as.tools?.length, 1)
  consume(m, ev('tool/result', { CallID: 't1', Name: 'shell', Content: 'file1' }, 3))
  const tool = m.msgs.at(-1)!
  assert.equal(tool.kind, 'tool')
  assert.equal(tool.err, false)
  assert.ok(tool.text.startsWith('✓ shell'))
  // assistant 快照同步为 ok
  assert.equal(as.tools?.[0].status, 'ok')
  assert.equal(m.pendingTool, undefined)
})

test('工具错误 → err 行与 err 标记', () => {
  const m = newModel()
  consume(m, ev('tool/call', { ID: 't2', Name: 'shell', Arguments: '{}' }, 1))
  consume(m, ev('tool/result', { CallID: 't2', Name: 'shell', Error: 'boom' }, 2))
  const row = m.msgs.at(-1)!
  assert.equal(row.err, true)
  assert.ok(row.text.startsWith('✗ shell'))
  assert.equal(row.full, 'boom')
})

test('结果帧先于调用帧到达不丢结果', () => {
  const m = newModel()
  consume(m, ev('tool/result', { CallID: 't9', Name: 'web_search', Content: 'r' }, 1))
  assert.equal(m.msgs.at(-1)?.kind, 'tool')
  assert.ok(m.msgs.at(-1)?.text.includes('web_search'))
})

test('chunk 携带工具增量时归并 pendingTool', () => {
  const m = newModel()
  consume(m, ev('assistant/chunk', { Delta: '', ToolCallID: 't3', ToolCallName: 'sh', ToolCallArgs: '{"a"' }, 1))
  consume(m, ev('assistant/chunk', { Delta: '', ToolCallID: 't3', ToolCallArgs: ':1}' }, 2))
  assert.equal(m.pendingTool?.args, '{"a":1}')
})

test('滚动摘要与取消回合 → meta 行', () => {
  const m = newModel()
  consume(m, ev('session/summary', { Summary: 'x'.repeat(300) }, 1))
  assert.equal(m.msgs.at(-1)?.kind, 'meta')
  assert.ok((m.msgs.at(-1)?.text ?? '').length <= 130)
  consume(m, ev('turn/end', 'cancelled', 2))
  assert.equal(m.msgs.at(-1)?.err, true)
  // 正常收尾不加行
  const before = m.msgs.length
  consume(m, ev('turn/end', 'done', 3))
  assert.equal(m.msgs.length, before)
})

test('isUsage 只认 session/usage', () => {
  assert.equal(isUsage(ev('session/usage', {})), true)
  assert.equal(isUsage(ev('assistant/chunk', {})), false)
})

test('未知帧与空载荷不崩(前向兼容)', () => {
  const m: StreamModel = newModel()
  for (const k of ['step/start', 'step/end', 'turn/start', '未来/kinds']) consume(m, ev(k, null))
  consume(m, ev('assistant/chunk', {}))
  assert.equal(m.msgs.length, 0)
})
