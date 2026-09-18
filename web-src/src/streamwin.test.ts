// S-P1-2 长会话窗口单测:基线应用、上滚分页拼接(不重复/不丢)、贴底裁剪与游标前移、
// 提示文案与版面口径。纯逻辑直跑(node --test)。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { consume, msgsOfEvents, newModel, type Msg, type StreamModel } from './sse.ts'
import type { Baseline, SessionEvent } from './types.ts'
import {
  applyBaseline,
  earlierHint,
  LOAD_EARLIER_PX,
  MAX_LIVE_MSGS,
  oldestSeq,
  prependMsgs,
  shouldLoadEarlier,
  trimHead,
  windowPartial,
} from './streamwin.ts'

// 一个回合 = user(1) + assistant(1),Seq 连续;回合数 n → Seq 1..2n
function turnEvents(n: number, from = 1): SessionEvent[] {
  const out: SessionEvent[] = []
  let seq = from
  for (let i = 0; i < n; i++) {
    out.push({ Kind: 'user/message', Seq: seq++, Payload: { Content: `u${i}` }, TS: '2026-01-01T00:00:00Z' })
    out.push({ Kind: 'assistant/message', Seq: seq++, Payload: { Content: `a${i}`, ToolCalls: [] }, TS: '2026-01-01T00:00:00Z' })
  }
  return out
}

function modelOf(events: SessionEvent[]): StreamModel {
  const m = newModel()
  for (const ev of events) consume(m, ev)
  return m
}

function msgsOf(m: StreamModel): Msg[] {
  return m.msgs
}

test('applyBaseline 记录窗口边界', () => {
  const m = newModel()
  const b: Baseline = { from: 41, to: 80, count: 40, has_more: true, window: 400 }
  applyBaseline(m, b)
  assert.equal(m.from, 41)
  assert.equal(m.hasMore, true)
  // 空会话:不触发上滚入口
  applyBaseline(m, { from: 0, to: 0, count: 0, has_more: false, window: 400 })
  assert.equal(m.from, 0)
  assert.equal(m.hasMore, false)
})

test('首连尾窗:baseline 之后帧按序进模型', () => {
  const m = newModel()
  applyBaseline(m, { from: 5, to: 8, count: 4, has_more: true, window: 400 })
  for (const ev of turnEvents(2, 5)) consume(m, ev)
  assert.equal(m.msgs.length, 4) // 2 个回合 × (用户 + 助手)
  assert.equal(oldestSeq(m), 5)
  assert.deepEqual(
    m.msgs.map((x) => x.text),
    ['u0', 'a0', 'u1', 'a1'],
  )
  assert.equal(windowPartial(m), true)
})

test('上滚分页:拼接不重复且不丢', () => {
  const all = turnEvents(10) // seq 1..20 → 10 个回合
  // 首连窗口 = 尾部 3 个回合(seq 15..20)= 6 条消息
  const m = modelOf(all.slice(14))
  applyBaseline(m, { from: 15, to: 20, count: 6, has_more: true, window: 400 })
  assert.equal(m.msgs.length, 6)
  assert.equal(oldestSeq(m), 15)

  // 第一页:before=15 → 更早 3 个回合(seq 9..14)= 6 条消息
  const page1 = all.filter((e) => e.Seq < 15).slice(-6) // 服务端窗口 = 最近 3 个回合
  const added1 = prependMsgs(m, msgsOfEvents(page1), page1[0].Seq)
  assert.equal(page1.length, 6)
  assert.equal(added1, 6)
  assert.equal(m.msgs.length, 12)
  assert.equal(oldestSeq(m), 9)
  assert.equal(m.from, 9)

  // 第二页:游标位漂移导致**重叠**(页尾含已在窗口内的回合)→ 去重后只补真正更早的
  const page2 = all.filter((e) => e.Seq < 13)
  const added2 = prependMsgs(m, msgsOfEvents(page2), page2[0].Seq)
  assert.equal(added2, 8, '重叠的 seq 9..12 应被丢弃')
  assert.equal(m.msgs.length, 20)
  assert.equal(oldestSeq(m), 1)
  assert.equal(m.from, 1)

  // 三条路(首连窗口 + 两页)拼完全部 10 个回合,顺序正确且无重复
  const texts = m.msgs.map((x) => x.text)
  const expect: string[] = []
  for (let i = 0; i <= 9; i++) expect.push(`u${i}`, `a${i}`) // 时间正序:u0 在最上
  assert.deepEqual(texts, expect)
  assert.equal(new Set(texts).size, texts.length, '不得出现重复消息')
})

test('prependMsgs:整页都已在窗口内则新增 0,但游标仍前进(防死循环)', () => {
  const all = turnEvents(5)
  const m = modelOf(all)
  applyBaseline(m, { from: 1, to: 10, count: 10, has_more: true, window: 400 })
  const dup = all.slice(0, 4)
  const added = prependMsgs(m, msgsOfEvents(dup), 1)
  assert.equal(added, 0)
  // 页里全是非消息事件时同样不能卡住:游标必须推进
  const added2 = prependMsgs(m, [], 1)
  assert.equal(added2, 0)
  assert.equal(m.from, 1)
  const added3 = prependMsgs(m, [], 0) // from=0 = 服务端未给边界 → 游标不动
  assert.equal(added3, 0)
  assert.equal(m.from, 1)
})

test('贴底裁剪:游标前移且被裁回合仍可取回', () => {
  const all = turnEvents(600) // 1200 条消息 > 上限
  const m = modelOf(all)
  applyBaseline(m, { from: 1, to: 1200, count: 1200, has_more: false, window: 400 })
  const dropped = trimHead(m)
  assert.equal(dropped, 1200 - MAX_LIVE_MSGS)
  assert.equal(m.msgs.length, MAX_LIVE_MSGS)
  assert.equal(m.trimmed, dropped)
  assert.equal(m.hasMore, true, '被裁的内容就是更早历史')
  // 游标前移到剩余最老消息:更老的事件(含被裁回合)仍在 before 范围内 → 可重新取回
  const cursor = m.from
  assert.equal(cursor, m.msgs[0].seq)
  const refetch = all.filter((e) => e.Seq < cursor)
  assert.ok(refetch.length > 0, '被裁回合的事件仍在分页范围内')
  const back = msgsOfEvents(refetch.slice(-4))
  const added = prependMsgs(m, back, refetch.slice(-4)[0].Seq)
  assert.equal(added, back.length, '取回的消息不因裁剪被去重丢掉')
  assert.equal(oldestSeq(m), back[0].seq)
})

test('trimHead:未超上限不动', () => {
  const m = modelOf(turnEvents(3))
  assert.equal(trimHead(m), 0)
  assert.equal(m.trimmed, 0)
  assert.equal(m.hasMore, false)
})

test('shouldLoadEarlier:贴顶且还有更早历史且不在加载中', () => {
  assert.equal(shouldLoadEarlier({ scrollTop: 0 }, true, false), true)
  assert.equal(shouldLoadEarlier({ scrollTop: LOAD_EARLIER_PX - 1 }, true, false), true)
  assert.equal(shouldLoadEarlier({ scrollTop: LOAD_EARLIER_PX }, true, false), false)
  assert.equal(shouldLoadEarlier({ scrollTop: 0 }, false, false), false)
  assert.equal(shouldLoadEarlier({ scrollTop: 0 }, true, true), false, '加载中不重复触发')
  assert.equal(shouldLoadEarlier(null, true, false), false)
})

test('提示文案与窗口口径', () => {
  assert.equal(earlierHint({ hasMore: false, trimmed: 0 }), '')
  assert.equal(earlierHint({ hasMore: true, trimmed: 0 }), '更早的消息未加载 · 上滚加载')
  assert.equal(earlierHint({ hasMore: true, trimmed: 12 }), '已折叠 12 条更早消息 · 上滚加载')
  assert.equal(windowPartial({ hasMore: false, trimmed: 0 }), false)
  assert.equal(windowPartial({ hasMore: true, trimmed: 0 }), true)
  assert.equal(windowPartial({ hasMore: false, trimmed: 3 }), true)
})

test('msgsOfEvents:助手消息的工具行靠同页 tool/result 回填', () => {
  // 页内必须整批喂进 consume:助手消息的工具行状态由后续 tool/result 事件回填
  const page: SessionEvent[] = [
    { Kind: 'user/message', Seq: 1, Payload: { Content: 'q' }, TS: '' },
    { Kind: 'assistant/chunk', Seq: 2, Payload: { Delta: '思考' }, TS: '' },
    { Kind: 'assistant/message', Seq: 3, Payload: { Content: '调用', ToolCalls: [{ ID: 't1', Name: 'shell', Arguments: '{"command":"ls"}' }] }, TS: '' },
    { Kind: 'tool/result', Seq: 4, Payload: { CallID: 't1', Name: 'shell', Content: 'ok' }, TS: '' },
    { Kind: 'assistant/message', Seq: 5, Payload: { Content: '答', ToolCalls: [] }, TS: '' },
  ]
  const msgs = msgsOfEvents(page)
  assert.equal(msgs.length, 4, 'user + 助手(带工具行) + 工具结果行 + 助手文本')
  const withTools = msgs.find((x) => x.tools && x.tools.length > 0)
  assert.ok(withTools, '助手消息应带上工具行')
  assert.equal(withTools!.tools![0].name, 'shell')
  const toolRow = msgs.find((x) => x.kind === 'tool')
  assert.equal(toolRow?.text.startsWith('✓ shell'), true, '工具行状态已回填')
  // 独立模型:不污染调用方模型
  const m = newModel()
  assert.equal(m.msgs.length, 0)
  assert.equal(m.from, 0)
})
