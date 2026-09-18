// S-P2-2 board.ts 单测:卡片派生(用量/回合/任务/变更/计划)、布局规范化(加卡片自动出现、
// 未知 id 丢弃)、显示/隐藏与排序、可见卡片投影、持久化解析容错。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  BOARD_CARDS,
  boardCards,
  fmtMs,
  fmtTok,
  jobStateLabel,
  moveCard,
  newBoardLayout,
  normalizeBoard,
  parseBoard,
  pct,
  serializeBoard,
  toggleCard,
  visibleCards,
  type BoardInput,
} from './board.ts'
import type { StateView } from './types.ts'

function st(over: Partial<StateView['stats']> = {}): StateView {
  return {
    model: 'm',
    thinking: 'off',
    sandbox: 'workspace-write',
    stats: { PromptTokens: 0, CompletionTokens: 0, CachedTokens: 0, Requests: 0, Window: 0, ...over },
    running: false,
    version: 'dev',
  }
}

function input(over: Partial<BoardInput> = {}): BoardInput {
  return {
    state: st(),
    traj: { turns: 0, running: false, ms: 0, tokens: 0, cached: 0, tools: 0, failed: 0 },
    changes: { files: 0, added: 0, removed: 0, count: 0 },
    jobs: { running: 0, total: 0 },
    plan: { total: 0, enabled: 0 },
    ...over,
  }
}

function card(id: string, inp: BoardInput) {
  const c = boardCards(inp).find((x) => x.id === id)
  assert.ok(c, `缺卡片 ${id}`)
  return c!
}

function row(id: string, label: string, inp: BoardInput): string {
  const r = card(id, inp).rows.find((x) => x.label === label)
  assert.ok(r, `${id} 缺行 ${label}`)
  return r.value
}

test('格式化口径与 traj.ts 一致', () => {
  assert.equal(fmtTok(0), '0')
  assert.equal(fmtTok(999), '999')
  assert.equal(fmtTok(1500), '1.5K')
  assert.equal(fmtTok(2_000_000), '2.00M')
  assert.equal(fmtMs(0), '0ms')
  assert.equal(fmtMs(999), '999ms')
  assert.equal(fmtMs(1500), '1.5s')
  assert.equal(fmtMs(65_000), '1m5s')
  assert.equal(pct(1, 4), 25)
  assert.equal(pct(5, 0), 0, '分母 0 不假精确')
  assert.equal(pct(200, 100), 100, '占比封顶 100')
})

test('任务状态中文名与其他端一致', () => {
  assert.equal(jobStateLabel('running'), '运行中')
  assert.equal(jobStateLabel('done'), '完成')
  assert.equal(jobStateLabel('failed'), '失败')
  assert.equal(jobStateLabel('killed'), '已终止')
  assert.equal(jobStateLabel('weird'), 'weird', '未知状态原样透出,不吞')
})

test('卡片集合与顺序固定(看板顺序 = 规范顺序)', () => {
  const cards = boardCards(input())
  assert.deepEqual(
    cards.map((c) => c.id),
    [...BOARD_CARDS],
  )
})

test('用量卡:累计口径 + 缓存占比 + 窗口未知不假精确', () => {
  const inp = input({
    state: st({ PromptTokens: 12000, CompletionTokens: 3000, CachedTokens: 6000, Requests: 7, Window: 0 }),
  })
  assert.equal(row('usage', '输入', inp), '12.0K')
  assert.equal(row('usage', '输出', inp), '3.0K')
  assert.equal(row('usage', '缓存', inp), '6.0K · 50%')
  assert.equal(row('usage', '请求', inp), '7')
  assert.equal(row('usage', '上下文', inp), '15.0K(窗口未知)')
  const withWin = input({ state: st({ PromptTokens: 12000, CompletionTokens: 3000, Window: 128000 }) })
  assert.equal(row('usage', '上下文', withWin), '15.0K/128.0K · 12%')
  // 窗口占用 ≥90% 才给提醒档
  const full = input({ state: st({ PromptTokens: 118000, Window: 128000 }) })
  const ctx = card('usage', full).rows.find((r) => r.label === '上下文')
  assert.equal(ctx?.level, 'warn')
})

test('用量卡:全零时上下文显示占位', () => {
  assert.equal(row('usage', '上下文', input()), '–')
  assert.equal(row('usage', '缓存', input()), '0')
})

test('回合卡:进行中回合的时长显示进行中,失败计数给错误档', () => {
  const inp = input({
    traj: { turns: 3, running: true, ms: undefined, tokens: 3000, cached: 0, tools: 9, failed: 2 },
  })
  assert.equal(row('turns', '已完成', inp), '3')
  assert.equal(row('turns', '总时长', inp), '进行中')
  assert.equal(row('turns', '平均 token', inp), '1.0K')
  assert.equal(card('turns', inp).rows.find((r) => r.label === '工具失败')?.level, 'err')
  assert.equal(card('turns', inp).note?.includes('有回合进行中'), true, '进行中在口径里说明')
  assert.equal(card('turns', input()).note?.includes('进行中'), false, '空闲时不提进行中')
  // 无回合 → 平均 token 占位(不显示 0)
  assert.equal(row('turns', '平均 token', input()), '–')
})

test('任务/变更/计划卡:数值与动作目标', () => {
  const inp = input({
    jobs: { running: 2, total: 5, latest: 'subagent 已提交' },
    changes: { files: 3, added: 12, removed: 4, count: 5 },
    plan: { total: 2, enabled: 1, next: '今天 09:00(2 小时后)', last: '成功' },
  })
  assert.equal(row('jobs', '运行中', inp), '2')
  assert.equal(row('jobs', '最近', inp), 'subagent 已提交')
  assert.equal(card('jobs', inp).action?.target, 'jobs')
  assert.equal(row('changes', '新增行', inp), '+12')
  assert.equal(row('changes', '删除行', inp), '−4')
  assert.equal(card('changes', inp).action?.target, 'changes')
  assert.equal(row('plan', '启用', inp), '1/2')
  assert.equal(row('plan', '下次运行', inp), '今天 09:00(2 小时后)')
  assert.equal(card('plan', inp).action?.target, 'settings')
  // 空值占位
  assert.equal(row('jobs', '最近', input()), '–')
  assert.equal(row('plan', '下次运行', input()), '未排期')
  assert.equal(row('plan', '最近终态', input()), '–')
})

test('布局:默认顺序与规范化容错', () => {
  assert.deepEqual(newBoardLayout(), { order: [...BOARD_CARDS], hidden: [] })
  // 未知 id 丢弃、重复只留一次、缺失的追加尾部
  const l = normalizeBoard({ order: ['plan', 'nope', 'plan', 'usage'], hidden: ['changes', 'nope', 7] })
  assert.deepEqual(l.order, ['plan', 'usage', 'turns', 'jobs', 'changes'])
  assert.deepEqual(l.hidden, ['changes'])
  // 坏输入:非数组 / 非对象 / null 一律回默认
  assert.deepEqual(normalizeBoard({ order: 'x', hidden: 1 }).order, [...BOARD_CARDS])
  assert.deepEqual(normalizeBoard(null).order, [...BOARD_CARDS])
  assert.deepEqual(newBoardLayout(['a', 'b']), { order: ['a', 'b'], hidden: [] }, '自定义卡片集')
})

test('布局:加卡片自动出现(向前兼容)', () => {
  const old = serializeBoard({ order: ['plan', 'usage'], hidden: [] })
  const l = parseBoard(old)
  assert.deepEqual(l.order, ['plan', 'usage', 'turns', 'jobs', 'changes'], '老布局缺的新卡片追加在尾部')
})

test('布局:持久化解析容错(坏 JSON 回默认)', () => {
  assert.deepEqual(parseBoard(null), newBoardLayout())
  assert.deepEqual(parseBoard(''), newBoardLayout())
  assert.deepEqual(parseBoard('{not json'), newBoardLayout())
  assert.deepEqual(parseBoard('[1,2,3]'), newBoardLayout())
  const l = { order: ['jobs', 'usage', 'turns', 'changes', 'plan'], hidden: ['turns'] }
  assert.deepEqual(parseBoard(serializeBoard(l)), l, '往返一致')
})

test('布局:显示/隐藏与排序', () => {
  let l = newBoardLayout()
  l = toggleCard(l, 'jobs')
  assert.deepEqual(l.hidden, ['jobs'])
  l = toggleCard(l, 'jobs')
  assert.deepEqual(l.hidden, [])
  assert.equal(toggleCard(l, 'nope'), l, '未知 id 原样返回')
  // 上移/下移
  l = moveCard(l, 'turns', -1)
  assert.deepEqual(l.order.slice(0, 2), ['turns', 'usage'])
  l = moveCard(l, 'turns', 1)
  assert.deepEqual(l.order.slice(0, 2), ['usage', 'turns'])
  assert.deepEqual(moveCard(l, 'usage', -1).order, l.order, '越界不动')
  assert.deepEqual(moveCard(l, 'plan', 1).order, l.order, '越界不动')
  assert.deepEqual(moveCard(l, 'nope', 1).order, l.order, '未知 id 不动')
})

test('visibleCards:按布局排序并过滤隐藏项', () => {
  const cards = boardCards(input())
  const l = { order: ['jobs', 'usage', 'turns', 'changes', 'plan'], hidden: ['turns', 'changes'] }
  assert.deepEqual(
    visibleCards(cards, l).map((c) => c.id),
    ['jobs', 'usage', 'plan'],
  )
  const all = newBoardLayout()
  assert.deepEqual(
    visibleCards(cards, { order: all.order, hidden: [...BOARD_CARDS] }),
    [],
    '全部隐藏 = 空看板',
  )
  // 卡片缺失(插件未装配等)只跳过,不抛错
  assert.deepEqual(
    visibleCards(cards.slice(0, 1), all).map((c) => c.id),
    ['usage'],
  )
})
