// 轨迹视图模型单测(S-P0-1;node --test,零新增依赖)。
// 覆盖:turn 边界派生、step/tool 归属、度量只取事件 TS、进行中不给时长、usage 汇总、
// 异常顺序(结果晚到/回合已结束)、隐式回合、概览聚合与格式化口径。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { clip, fmtBytes, fmtMs, fmtTok, newTraj, trajOverview, trajPush, turnMs, turnStats, toolMs, stepMs } from './traj.ts'
import type { TrajModel } from './traj.ts'
import { argsSummary } from './sse.ts'
import type { SessionEvent } from './types.ts'

const TS = (s: number): string => new Date(Date.UTC(2026, 0, 1, 0, 0, s)).toISOString()
const ev = (Kind: string, Payload: unknown, Seq: number, sec = 0): SessionEvent => ({
  Kind,
  Seq,
  Payload,
  TS: TS(sec),
})

// 一个两回合会话(每回合一 step、工具一成功一失败)+ 第三回合进行中
function sample(): TrajModel {
  const m = newTraj()
  trajPush(m, ev('user/message', { Content: '看看仓库' }, 1, 0))
  trajPush(m, ev('step/start', null, 2, 0))
  trajPush(m, ev('tool/call', { ID: 'c1', Name: 'fs_list', Arguments: '{"path":"."}' }, 3, 1))
  trajPush(m, ev('tool/result', { CallID: 'c1', Name: 'fs_list', Content: 'x'.repeat(2048) }, 4, 2))
  trajPush(m, ev('session/usage', { Model: 'gpt-5', Usage: { PromptTokens: 100, CompletionTokens: 20, CachedTokens: 40 } }, 5, 2))
  trajPush(m, ev('assistant/message', { Content: '仓库有 3 个目录' }, 6, 3))
  trajPush(m, ev('step/end', null, 7, 3))
  trajPush(m, ev('turn/end', 'done', 8, 4))
  trajPush(m, ev('user/message', { Content: '改一下' }, 9, 5))
  trajPush(m, ev('step/start', null, 10, 5))
  trajPush(m, ev('tool/call', { ID: 'c2', Name: 'fs_write', Arguments: '{"path":"a.txt"}' }, 11, 5))
  trajPush(m, ev('tool/result', { CallID: 'c2', Name: 'fs_write', Content: '', Error: '权限拒绝' }, 12, 6))
  trajPush(m, ev('session/usage', { Model: 'gpt-5', Usage: { PromptTokens: 200, CompletionTokens: 30, CachedTokens: 80 } }, 13, 6))
  trajPush(m, ev('step/end', null, 14, 6))
  trajPush(m, ev('turn/end', 'done', 15, 7))
  trajPush(m, ev('user/message', { Content: '再来一次' }, 16, 8))
  trajPush(m, ev('step/start', null, 17, 8))
  trajPush(m, ev('tool/call', { ID: 'c3', Name: 'shell', Arguments: '{"command":"sleep 5"}' }, 18, 8))
  return m
}

test('turn 边界由 user/message 与 turn/end 派生,turn/index 递增', () => {
  const m = sample()
  assert.equal(m.turns.length, 3)
  assert.deepEqual(
    m.turns.map((t) => t.index),
    [1, 2, 3],
  )
  assert.equal(m.turns[0].user, '看看仓库')
  assert.equal(m.turns[0].endTs, TS(4))
  assert.equal(m.turns[2].endTs, undefined)
  assert.equal(m.cur?.index, 3)
})

test('tool/call 归属当前 step,结果回填状态与输出字节', () => {
  const m = sample()
  const t1 = m.turns[0]
  assert.equal(t1.steps.length, 1)
  assert.deepEqual(t1.steps[0].toolIds, ['c1'])
  assert.equal(t1.tools['c1'].status, 'ok')
  assert.equal(t1.tools['c1'].outBytes, 2048)
  const t2 = m.turns[1]
  assert.equal(t2.tools['c2'].status, 'err')
  assert.equal(t2.tools['c2'].error, '权限拒绝')
})

test('度量只取事件 TS:step/tool/turn 时长按时间戳相减', () => {
  const m = sample()
  const t1 = m.turns[0]
  assert.equal(turnMs(t1), 4000)
  assert.equal(stepMs(t1.steps[0]), 3000)
  assert.equal(toolMs(t1.tools['c1']), 1000)
})

test('进行中的 turn/step/tool 一律不给时长(不编造)', () => {
  const m = sample()
  const t3 = m.turns[2]
  assert.equal(turnMs(t3), undefined)
  assert.equal(stepMs(t3.steps[0]), undefined)
  assert.equal(toolMs(t3.tools['c3']), undefined)
  const st = turnStats(t3)
  assert.equal(st.ms, undefined)
  assert.equal(st.pending, 1)
})

test('usage 汇总到所在回合(prompt/completion/cached/requests/model)', () => {
  const m = sample()
  const u1 = m.turns[0].usage
  assert.deepEqual(u1, { model: 'gpt-5', prompt: 100, completion: 20, cached: 40, requests: 1 })
  assert.equal(m.turns[1].usage.prompt, 200)
  assert.equal(m.turns[0].usage.requests, 1)
})

test('turnStats:步数/工具数/失败数/待回填/token 合计', () => {
  const m = sample()
  assert.deepEqual(turnStats(m.turns[0]), { steps: 1, tools: 1, failed: 0, pending: 0, tokens: 120, ms: 4000 })
  assert.deepEqual(turnStats(m.turns[1]), { steps: 1, tools: 1, failed: 1, pending: 0, tokens: 230, ms: 2000 })
})

test('assistant 文本多步累积(步骤间第二次文本追加)', () => {
  const m = newTraj()
  trajPush(m, ev('user/message', { Content: 'q' }, 1, 0))
  trajPush(m, ev('assistant/message', { Content: '第一段' }, 2, 1))
  trajPush(m, ev('assistant/message', { Content: '第二段' }, 3, 2))
  assert.equal(m.turns[0].assistant, '第一段\n\n第二段')
})

test('结果帧先于调用帧/回合结束后到达:不丢结果,跨回合回溯定位', () => {
  const m = newTraj()
  trajPush(m, ev('user/message', { Content: 'q' }, 1, 0))
  trajPush(m, ev('tool/result', { CallID: 'cx', Name: 'shell', Content: 'out' }, 2, 1))
  assert.equal(m.turns[0].tools['cx'], undefined) // 无调用帧:不臆造工具行
  trajPush(m, ev('tool/call', { ID: 'cx', Name: 'shell', Arguments: '{}' }, 3, 1))
  trajPush(m, ev('turn/end', 'done', 4, 2))
  trajPush(m, ev('tool/result', { CallID: 'cx', Name: 'shell', Content: 'late' }, 5, 3))
  assert.equal(m.turns[0].tools['cx'].status, 'ok')
  assert.equal(m.turns[0].tools['cx'].outBytes, 4)
})

test('无 step 的工具调用补隐式 step(防归属丢失)', () => {
  const m = newTraj()
  trajPush(m, ev('user/message', { Content: 'q' }, 1, 0))
  trajPush(m, ev('tool/call', { ID: 'z', Name: 'shell', Arguments: '{}' }, 2, 1))
  assert.equal(m.turns[0].steps.length, 1)
  assert.deepEqual(m.turns[0].steps[0].toolIds, ['z'])
})

test('只有过程帧而没有 user/message 时开隐式回合;turn/start 不重复开回合', () => {
  const m = newTraj()
  trajPush(m, ev('turn/start', null, 1, 0))
  trajPush(m, ev('step/start', null, 2, 0))
  trajPush(m, ev('turn/start', null, 3, 1)) // 已有 cur:不重复
  assert.equal(m.turns.length, 1)
  assert.equal(m.turns[0].steps.length, 1)
})

test('turn/start 先行(新账本顺序)时用户消息回填同一回合,不产生空回合', () => {
  const m = newTraj()
  trajPush(m, ev('turn/start', null, 1, 0))
  trajPush(m, ev('user/message', { Content: '看看仓库' }, 2, 0))
  trajPush(m, ev('step/start', null, 3, 0))
  trajPush(m, ev('assistant/message', { Content: '好' }, 4, 1))
  trajPush(m, ev('turn/end', 'done', 5, 2))
  assert.equal(m.turns.length, 1)
  assert.equal(m.turns[0].user, '看看仓库')
  assert.equal(m.turns[0].seq, 2) // 锚点 = 用户消息 seq
  assert.equal(turnMs(m.turns[0]), 2000) // 时长从用户消息 ts 起算
  // 同一回合内后到的 user/message(不可能但需不吞):开新回合
  trajPush(m, ev('user/message', { Content: '第二条' }, 6, 3))
  assert.equal(m.turns.length, 2)
  assert.equal(m.turns[1].user, '第二条')
})

test('turn/end 其它载荷(cancelled/max_steps)原样记录', () => {
  const m = newTraj()
  trajPush(m, ev('user/message', { Content: 'q' }, 1, 0))
  trajPush(m, ev('turn/end', 'cancelled', 2, 1))
  assert.equal(m.turns[0].reason, 'cancelled')
})

test('turn/end 无载荷(undefined)记为 done', () => {
  const m = newTraj()
  trajPush(m, ev('user/message', { Content: 'q' }, 1, 0))
  trajPush(m, ev('turn/end', null, 2, 1))
  assert.equal(m.turns[0].reason, 'done')
})

test('session/usage 出现在回合外(无 cur)时不崩且不记账', () => {
  const m = newTraj()
  trajPush(m, ev('session/usage', { Model: 'x', Usage: { PromptTokens: 1 } }, 1, 0))
  assert.equal(m.turns.length, 0)
})

test('trajOverview:回合数/总时长/累计 token 与缓存;有未结束回合则总时长为 undefined', () => {
  const done = newTraj()
  trajPush(done, ev('user/message', { Content: 'a' }, 1, 0))
  trajPush(done, ev('session/usage', { Model: 'm', Usage: { PromptTokens: 10, CompletionTokens: 5, CachedTokens: 4 } }, 2, 1))
  trajPush(done, ev('turn/end', 'done', 3, 2))
  const ov = trajOverview(done)
  assert.deepEqual(ov, { turns: 1, running: false, ms: 2000, tokens: 15, cached: 4 })

  const running = sample()
  const ov2 = trajOverview(running)
  assert.equal(ov2.turns, 3)
  assert.equal(ov2.running, true)
  assert.equal(ov2.ms, undefined) // 进行中回合:总时长不编造
  assert.equal(ov2.tokens, 120 + 230)
})

test('格式化口径:ms/tok/bytes/clip', () => {
  assert.equal(fmtMs(undefined), '')
  assert.equal(fmtMs(930), '930ms')
  assert.equal(fmtMs(4200), '4.2s')
  assert.equal(fmtMs(62000), '1m2s')
  assert.equal(fmtTok(120), '120')
  assert.equal(fmtTok(12500), '12.5K')
  assert.equal(fmtTok(2500000), '2.50M')
  assert.equal(fmtBytes(undefined), '')
  assert.equal(fmtBytes(512), '512B')
  assert.equal(fmtBytes(2048), '2.0KB')
  assert.equal(fmtBytes(3 * 1024 * 1024), '3.0MB')
  assert.equal(clip('  多行\n文本  '), '多行 文本')
  assert.equal(clip('x'.repeat(80), 10), 'xxxxxxxxxx…')
})

test('args 保留原始 JSON:展示口径经 sse.argsSummary 与流视图一致', () => {
  const m = sample()
  assert.equal(m.turns[0].tools['c1'].args, '{"path":"."}')
  assert.equal(argsSummary(m.turns[0].tools['c1'].args), 'path=.') // 两边同源,摘要不漂移
})

test('同一事件流重放两次结果一致(纯函数、可重放)', () => {
  const a = sample()
  const b = newTraj()
  trajPush(b, ev('user/message', { Content: '看看仓库' }, 1, 0))
  trajPush(b, ev('step/start', null, 2, 0))
  trajPush(b, ev('tool/call', { ID: 'c1', Name: 'fs_list', Arguments: '{"path":"."}' }, 3, 1))
  trajPush(b, ev('tool/result', { CallID: 'c1', Name: 'fs_list', Content: 'x'.repeat(2048) }, 4, 2))
  trajPush(b, ev('session/usage', { Model: 'gpt-5', Usage: { PromptTokens: 100, CompletionTokens: 20, CachedTokens: 40 } }, 5, 2))
  trajPush(b, ev('assistant/message', { Content: '仓库有 3 个目录' }, 6, 3))
  trajPush(b, ev('step/end', null, 7, 3))
  trajPush(b, ev('turn/end', 'done', 8, 4))
  assert.deepEqual(turnStats(b.turns[0]), turnStats(a.turns[0]))
  assert.equal(b.turns[0].assistant, a.turns[0].assistant)
})
