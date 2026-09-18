// dock.ts 单测(S-P2-1 侧栏停靠区):布局状态的容错与收敛语义。
// 这些用例钉住的是「坏持久化不许让停靠区变成不可用/空白」——布局是呈现偏好,
// 一个字段写坏就应当只回落那一个字段,而不是整块失效。
import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  BUILTIN_PANELS,
  DOCK_DEFAULT_WIDTH,
  DOCK_MAX_WIDTH,
  DOCK_MIN_WIDTH,
  DOCK_NARROW_WIDTH,
  clampDockWidth,
  closeDock,
  isNarrow,
  newDock,
  openDockPanel,
  panelLabel,
  panelOf,
  parseDock,
  persistKey,
  serializeDock,
  toggleDock,
} from './dock.ts'

test('newDock:收起 / 第一个内建面板 / 默认宽度', () => {
  const d = newDock()
  assert.equal(d.open, false)
  assert.equal(d.panel, BUILTIN_PANELS[0].id)
  assert.equal(d.width, DOCK_DEFAULT_WIDTH)
})

test('内建面板与标签:三个宿主面板,id 不重复', () => {
  assert.deepEqual(
    BUILTIN_PANELS.map((p) => p.id),
    ['changes', 'board', 'jobs'],
  )
  assert.equal(panelLabel('changes'), '变更')
  assert.equal(panelLabel('board'), '看板')
  assert.equal(panelLabel('jobs'), '任务')
  // 未知面板不编造名字(标签行只渲染已知面板)
  assert.equal(panelOf('nope'), null)
  assert.equal(panelLabel('nope'), '')
  assert.equal(new Set(BUILTIN_PANELS.map((p) => p.id)).size, BUILTIN_PANELS.length)
})

test('clampDockWidth:上下限', () => {
  assert.equal(clampDockWidth(100), DOCK_MIN_WIDTH)
  assert.equal(clampDockWidth(5000), DOCK_MAX_WIDTH)
  assert.equal(clampDockWidth(420), 420)
  assert.equal(clampDockWidth(420.4), 420)
  // 非数值(NaN/Infinity)回落默认宽度,而不是写进样式变成 NaNpx
  assert.equal(clampDockWidth(Number.NaN), DOCK_DEFAULT_WIDTH)
  assert.equal(clampDockWidth(Number.POSITIVE_INFINITY), DOCK_MAX_WIDTH)
})

test('clampDockWidth:视口不足时给对话流让位', () => {
  // 1400 - 520 = 880 > 上限 → 仍夹在上限
  assert.equal(clampDockWidth(700, 1400), 700)
  // 1200 - 520 = 680 → 700 被压到 680
  assert.equal(clampDockWidth(700, 1200), 680)
  // 极窄视口:下限优先(不返回负数/0,停靠区仍可用)
  assert.equal(clampDockWidth(700, 600), DOCK_MIN_WIDTH)
  assert.equal(clampDockWidth(700, 0), 700)
})

test('isNarrow:窄屏退化为覆盖式抽屉', () => {
  assert.equal(isNarrow(DOCK_NARROW_WIDTH), false) // 边界值本身仍并排
  assert.equal(isNarrow(DOCK_NARROW_WIDTH - 1), true)
  assert.equal(isNarrow(1440), false)
  assert.equal(isNarrow(0), false) // 未知视口不误判成窄屏
  assert.equal(isNarrow(Number.NaN), false)
})

test('toggleDock / closeDock:保留面板与宽度', () => {
  const s = openDockPanel(newDock(), 'jobs')
  assert.equal(s.open, true)
  assert.equal(s.panel, 'jobs')
  assert.equal(toggleDock(s).open, false)
  assert.equal(toggleDock(s).panel, 'jobs') // 收起不改面板
  assert.equal(toggleDock(s).width, s.width)
  assert.equal(closeDock(s).open, false)
  assert.equal(closeDock(s).panel, 'jobs')
  // 再打开回到上次那一屏
  assert.equal(toggleDock(toggleDock(s)).panel, 'jobs')
})

test('openDockPanel:未知 id 不写入状态', () => {
  const s = newDock()
  assert.deepEqual(openDockPanel(s, 'planet'), s)
  assert.equal(openDockPanel(s, 'board').panel, 'board')
  assert.equal(openDockPanel(s, 'board').open, true)
})

test('parseDock:坏输入逐字段容错', () => {
  assert.deepEqual(parseDock(null), newDock())
  assert.deepEqual(parseDock(''), newDock())
  assert.deepEqual(parseDock('{坏 JSON'), newDock())
  assert.deepEqual(parseDock('[]'), newDock())
  assert.deepEqual(parseDock('"str"'), newDock())
  // 未知面板 → 回落基线面板;宽度越界 → 夹紧;open 类型不对 → 回落
  assert.deepEqual(parseDock('{"panel":"planet","width":99999,"open":"yes"}'), {
    open: false,
    panel: BUILTIN_PANELS[0].id,
    width: DOCK_MAX_WIDTH,
  })
  // 只坏一个字段时其余字段仍生效(不连坐)
  assert.deepEqual(parseDock('{"panel":"board","width":null}'), {
    open: false,
    panel: 'board',
    width: DOCK_DEFAULT_WIDTH,
  })
  assert.deepEqual(parseDock('{"open":true,"panel":"jobs","width":420}'), { open: true, panel: 'jobs', width: 420 })
})

test('serializeDock → parseDock 往返稳定', () => {
  const s = openDockPanel(newDock(), 'board')
  const round = parseDock(serializeDock(s))
  assert.deepEqual(round, { ...s, width: clampDockWidth(s.width) })
  // 序列化只写三个字段(持久化形状稳定)
  assert.deepEqual(Object.keys(JSON.parse(serializeDock(s))).sort(), ['open', 'panel', 'width'])
  // 越界宽度序列化时就被夹紧
  assert.equal(JSON.parse(serializeDock({ ...s, width: 9999 })).width, DOCK_MAX_WIDTH)
})

test('persistKey:键名固定(改名即持久化失效,需显式迁移)', () => {
  assert.equal(persistKey(), 'gah.dock')
})
