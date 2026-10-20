// 会话流消费引擎单测(E-E;node --test,零新增依赖)。
// 覆盖:参数/结果摘要截断、user/assistant/tool 落定、工具行状态与快照同步、
// 异常到达顺序(结果先于调用)、取消/摘要 meta 行、usage 判定。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { argsSummary, cleanAnswer, cleanThink, consume, isUsage, newModel, resultSummary } from './sse.ts'
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

test('宿主算好的一行人话摘要优先于截断全文(2026-10-20 B1)', () => {
  const m = newModel()
  consume(m, ev('tool/call', { ID: 't5', Name: 'file_read', Arguments: '{"path":"/a/b.go"}' }, 1))
  consume(
    m,
    ev(
      'tool/result',
      {
        CallID: 't5',
        Name: 'file_read',
        Content: JSON.stringify({ path: '/a/b.go', content: 'package main\n' }),
        Summary: '读 2 行 · 12 B',
      },
      2
    )
  )
  const row = m.msgs.at(-1)!
  assert.equal(row.kind, 'tool')
  // 摘要直接用（不是 JSON 前部）
  assert.equal(row.summary, '读 2 行 · 12 B')
  assert.ok(row.full.includes('package main'))
})

test('内容级错误(工具把失败写在结果里)→ 错误行而非绿勾', () => {
  const m = newModel()
  consume(m, ev('tool/call', { ID: 't6', Name: 'file_read', Arguments: '{}' }, 1))
  consume(
    m,
    ev('tool/result', { CallID: 't6', Name: 'file_read', Content: '{"error":"permission denied"}', Summary: 'permission denied', SummaryErr: true }, 2)
  )
  const row = m.msgs.at(-1)!
  assert.equal(row.err, true)
  assert.ok(row.text.startsWith('✗ file_read'))
})

test('旧日志(无摘要)→ 回退到截断全文(向后兼容)', () => {
  const m = newModel()
  consume(m, ev('tool/call', { ID: 't7', Name: 'shell', Arguments: '{}' }, 1))
  consume(m, ev('tool/result', { CallID: 't7', Name: 'shell', Content: 'hello' }, 2))
  const row = m.msgs.at(-1)!
  assert.equal(row.summary, undefined)
  assert.equal(row.full, 'hello')
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

// 角色切换(R-3):事件要变成流里的一条 meta 行(否则"这段话是谁写的"在 UI 上看不出来);
// 名称用事件里的快照(角色可能随后被改名/删,不能反查)。
test('role/switch → meta 行(含回基线)', () => {
  const m = newModel()
  consume(m, ev('role/switch', { ID: 'finance', Name: '财务' }, 1))
  assert.equal(m.msgs.at(-1)?.kind, 'meta')
  assert.match(m.msgs.at(-1)?.text ?? '', /角色切换.*财务/)
  consume(m, ev('role/switch', {}, 2))
  assert.match(m.msgs.at(-1)?.text ?? '', /默认（基线）/, 'ID 空 = 回基线,要说清楚')
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

// 思维增量必须单独落位(此前整个丢弃,「思考过程与最终输出结果」看不出区别
// —— 2026-10-03 用户反馈)。
test('thinking 增量与正文分开落位,不混进最终答复', () => {
  const m = newModel()
  consume(m, ev('assistant/chunk', { Thinking: '先推理甲。' }))
  consume(m, ev('assistant/chunk', { Thinking: '再推理乙。' }))
  consume(m, ev('assistant/chunk', { Delta: '结论丙。' }))
  consume(m, ev('assistant/message', { Content: '结论丙。', ToolCalls: [] }))
  assert.equal(m.msgs.length, 1)
  const msg = m.msgs[0]
  assert.equal(msg.think, '先推理甲。再推理乙。')
  assert.equal(msg.text, '结论丙。')
  assert.ok(!msg.text.includes('推理'), '思维内容不得混进正文')
  // 下一轮不串味
  assert.equal(m.pendingThink, '')
})

test('只有思维、没有正文的轮次也保留思考内容', () => {
  const m = newModel()
  consume(m, ev('assistant/chunk', { Thinking: '在想。' }))
  consume(m, ev('assistant/message', { Content: '', ToolCalls: [] }))
  assert.equal(m.msgs[0].think, '在想。')
  assert.equal(m.msgs[0].text, '')
})

test('思考标签:正文开头漏出的闭标签被剥掉(网关把分隔符当正文推)', () => {
  const m = newModel()
  consume(m, ev('assistant/chunk', { Thinking: '想法' }, 1))
  consume(m, ev('assistant/message', { Content: '</think>\n\n正文' }, 2))
  assert.equal(m.msgs[0].text, '正文')
  assert.equal(m.msgs[0].think, '想法')
})

test('思考标签:思维文本尾部的闭标签被剥掉', () => {
  const m = newModel()
  consume(m, ev('assistant/chunk', { Thinking: '想法</think>' }, 1))
  consume(m, ev('assistant/message', { Content: '正文' }, 2))
  assert.equal(m.msgs[0].think, '想法')
})

test('思考标签:流式拆成两截的半截标签也不先露出来', () => {
  assert.equal(cleanAnswer('</thi'), '')
  assert.equal(cleanAnswer('\n</think'), '')
  assert.equal(cleanThink('想法</thi'), '想法')
  assert.equal(cleanThink('想法</think>'), '想法')
})

test('思考标签:只在边界剥,正文里正常讨论时一个字都不动', () => {
  assert.equal(cleanAnswer('讨论 </think> 的用法'), '讨论 </think> 的用法')
  assert.equal(cleanThink('提到 <thinking> 标签时'), '提到 <thinking> 标签时')
  assert.equal(cleanAnswer('正文'), '正文')
})

// 工具实时输出(2026-10-20 C1):瞬时过程,归位到「正在跑」的那一行。
test('tool/progress:增量贴到进行中的工具行', () => {
  const m = newModel()
  consume(m, ev('tool/call', { ID: 't7', Name: 'shell', Arguments: '{}' }, 1))
  consume(m, ev('tool/progress', { CallID: 't7', Name: 'shell', Chunk: '编译中', Seq: 1 }, 0))
  consume(m, ev('tool/progress', { CallID: 't7', Name: 'shell', Chunk: '链接中', Seq: 2 }, 0))
  assert.equal(m.pendingTool?.live, '编译中链接中')
  // 瞬时事件不进消息流(它是过程,不是会话内容)
  assert.equal(m.msgs.length, 0)
})

test('tool/progress:找不到对应调用 ⇒ 丢弃(不张冠李戴)', () => {
  const m = newModel()
  consume(m, ev('tool/progress', { CallID: 'nobody', Name: 'shell', Chunk: '孤儿块', Seq: 1 }, 0))
  assert.equal(m.pendingTool, undefined)
  assert.equal(m.msgs.length, 0)
})

test('tool/progress:工具结束后不再更新(结论不被过程改写)', () => {
  const m = newModel()
  consume(m, ev('tool/call', { ID: 't8', Name: 'shell', Arguments: '{}' }, 1))
  consume(m, ev('tool/result', { CallID: 't8', Name: 'shell', Content: 'ok' }, 2))
  consume(m, ev('tool/progress', { CallID: 't8', Name: 'shell', Chunk: '迟到', Seq: 3 }, 0))
  assert.equal(m.pendingTool, undefined)
  assert.equal(m.msgs.at(-1)!.kind, 'tool')
})
