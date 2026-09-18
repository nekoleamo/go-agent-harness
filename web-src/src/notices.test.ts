// NOND-N1 notices.ts 单测:去重(实时帧与回填重叠)、游标只增、限窗显式计数、TTL 语义、
// 过旧回填过滤、gap 传递、显式关闭。全部为纯函数 → 无定时器依赖,可确定性断言。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { type Notice, type NoticePage } from './types.ts'
import {
  BACKFILL_WINDOW_MS,
  INFO_TTL_MS,
  MAX_TOASTS,
  applyPage,
  dismiss,
  expire,
  hasPending,
  levelClass,
  newToasts,
  pushNotice,
  ttlFor,
} from './notices.ts'

const NOW = 1_700_000_000_000

function n(id: number, level: Notice['level'] = 'info', ts = new Date(NOW).toISOString()): Notice {
  return { id, level, title: 't' + id, ts }
}

test('levelClass:未知级别按 info,不假装严重', () => {
  assert.equal(levelClass('warn'), 'warn')
  assert.equal(levelClass('error'), 'error')
  assert.equal(levelClass('info'), 'info')
  assert.equal(levelClass('critical'), 'info')
  assert.equal(levelClass(''), 'info')
})

test('ttlFor:info 自动消失,warn/error 常驻', () => {
  assert.equal(ttlFor('info'), INFO_TTL_MS)
  assert.equal(ttlFor('warn'), 0)
  assert.equal(ttlFor('error'), 0)
})

test('pushNotice:去重 + 游标只增 + 新条目在前', () => {
  const st = newToasts()
  assert.equal(pushNotice(st, n(1), NOW), true)
  assert.equal(pushNotice(st, n(1), NOW), false, '同一 id 不重复入列')
  assert.equal(pushNotice(st, n(3), NOW), true)
  assert.equal(pushNotice(st, n(2), NOW), true, '乱序到达(回填晚到)也要收')
  assert.deepEqual(st.items.map((t) => t.id), [2, 3, 1], '最新在前')
  assert.equal(st.since, 3, '游标 = 已见最大 id')
})

test('pushNotice:游标不被晚到的旧 id 拽回', () => {
  const st = newToasts()
  pushNotice(st, n(9), NOW)
  pushNotice(st, n(4), NOW)
  assert.equal(st.since, 9)
})

test('pushNotice:可见上限挤出并显式计数(不静默丢)', () => {
  const st = newToasts()
  for (let i = 1; i <= MAX_TOASTS + 2; i++) pushNotice(st, n(i), NOW)
  assert.equal(st.items.length, MAX_TOASTS)
  assert.equal(st.overflow, 2)
  assert.equal(hasPending(st), true)
  assert.deepEqual(st.items.map((t) => t.id), [5, 4, 3], '保留最新的 N 条')
})

test('expire:info 到期消失,warn 常驻', () => {
  const st = newToasts()
  pushNotice(st, n(1, 'info'), NOW)
  pushNotice(st, n(2, 'warn'), NOW)
  expire(st, NOW + INFO_TTL_MS)
  assert.deepEqual(st.items.map((t) => t.id), [2], 'info 到期即走(不靠点击)')
  expire(st, NOW + 10 * INFO_TTL_MS)
  assert.deepEqual(st.items.map((t) => t.id), [2], 'warn 不会自己消失')
})

test('dismiss:显式关闭一条', () => {
  const st = newToasts()
  pushNotice(st, n(1, 'error'), NOW)
  pushNotice(st, n(2, 'error'), NOW)
  dismiss(st, 1)
  assert.deepEqual(st.items.map((t) => t.id), [2])
})

test('applyPage:回填与实时帧重叠时靠 id 去重,gap 传递', () => {
  const st = newToasts()
  pushNotice(st, n(3), NOW) // 实时帧先到
  const page: NoticePage = { items: [n(2), n(3), n(4)], max_id: 4, gap: true }
  assert.equal(applyPage(st, page, NOW), 2, '只有 2、4 是新条目')
  assert.equal(st.gap, true, 'gap 必须如实传递(回填不完整不得谎报)')
  assert.equal(st.since, 4)
})

test('applyPage:过旧条目不入列(打开页面不重弹半小时前的提示)', () => {
  const st = newToasts()
  const old = new Date(NOW - BACKFILL_WINDOW_MS - 1000).toISOString()
  const fresh = new Date(NOW - 1000).toISOString()
  const added = applyPage(st, { items: [n(1, 'error', old), n(2, 'error', fresh)], max_id: 2 }, NOW)
  assert.equal(added, 1)
  assert.deepEqual(st.items.map((t) => t.id), [2])
})

test('applyPage:ts 缺失/坏值时不当过旧(不因解析失败吞掉提示)', () => {
  const st = newToasts()
  const added = applyPage(st, { items: [{ id: 1, level: 'error', title: 'x' }], max_id: 1 }, NOW)
  assert.equal(added, 1)
})

test('applyPage:空页/缺字段不炸,游标仍推进', () => {
  const st = newToasts()
  assert.equal(applyPage(st, null, NOW), 0)
  assert.equal(applyPage(st, { items: [], max_id: 7 }, NOW), 0)
  assert.equal(st.since, 7)
})

test('applyPage:过旧条目记入 seen —— 回填已消费该 id,不再重复打扰', () => {
  const st = newToasts()
  const old = new Date(NOW - BACKFILL_WINDOW_MS - 1000).toISOString()
  applyPage(st, { items: [n(1, 'error', old)], max_id: 1 }, NOW)
  assert.equal(st.items.length, 0, '过旧的不上屏')
  assert.equal(pushNotice(st, n(1, 'error'), NOW), false, '同一 id 不因回填跳过而重新弹出')
  assert.equal(st.since, 1, '游标仍推进(已消费)')
})
