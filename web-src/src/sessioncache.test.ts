// 会话视图缓存单测(第一百三十九批;node --test,零新增依赖)。
// 钉住三件与体验直接相关的事:① 命中即不重放(切换不闪);② 关页签不丢缓存;
// ③ LRU 淘汰时**连带清续传游标** —— 留着游标会让下次连上按差集只发最后几条(真缺陷)。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { SessionCache, SESSION_CACHE_LIMIT, switchPlan } from './sessioncache.ts'
import { DEFAULT_TAB_LIMIT } from './tabset.ts'
import { newModel, consume } from './sse.ts'
import { newTraj } from './traj.ts'
import { newChanges } from './changes.ts'
import type { MetaLine } from './registry.ts'
import type { SessionEvent } from './types.ts'

function ev(Kind: string, Payload: unknown, Seq: number): SessionEvent {
  return { Kind, Seq, Payload, TS: new Date(Date.UTC(2026, 0, 1) + Seq * 1000).toISOString() }
}

function viewOf(text: string): {
  model: ReturnType<typeof newModel>
  traj: ReturnType<typeof newTraj>
  changes: ReturnType<typeof newChanges>
  metas: MetaLine[]
  scrollTop: number
  atBottom: boolean
} {
  const model = newModel()
  consume(model, ev('user/message', { Content: text }, 1))
  return { model, traj: newTraj(), changes: newChanges(), metas: [], scrollTop: 120, atBottom: false }
}

test('命中缓存拿回同一份对象(不拷贝):切换不需要重新重放', () => {
  const c = new SessionCache(4)
  const v = viewOf('A')
  c.put('a', v)
  const got = c.get('a')
  assert.ok(got)
  assert.equal(got.model, v.model, '模型身份应不变(同一份代理,不是拷贝)')
  assert.equal(got.model.msgs[0].text, 'A')
})

test('主会话占位键:"" 与 "main" 视为同一会话', () => {
  const c = new SessionCache(4)
  c.put('', viewOf('主'))
  assert.ok(c.get('main'), '"" 存进去的应能用 "main" 取到')
  assert.ok(c.get(''), '"main" 存进去的应能用 "" 取到')
})

test('LRU:命中续命,超限淘汰最久没用的,并通知调用方清游标', () => {
  const evicted: string[] = []
  const c = new SessionCache(2)
  c.setEvict((id) => evicted.push(id))
  c.put('a', viewOf('A'))
  c.put('b', viewOf('B'))
  c.get('a') // a 续命 ⇒ 最久没用的是 b
  c.put('c', viewOf('C'))
  assert.equal(c.size(), 2)
  assert.deepEqual(evicted, ['b'], '淘汰的应是 b(最久没用),不是 a')
  assert.ok(c.get('a'), 'a 刚被用过,不该被淘汰')
  assert.ok(c.get('c'))
  assert.equal(c.get('b'), undefined)
})

test('rename:占位 main 改绑到真实 id 后缓存跟过去;目标已有缓存则保留既有的', () => {
  const c = new SessionCache(4)
  const v = viewOf('占位')
  c.put('main', v)
  c.rename('main', 'sess-123')
  assert.equal(c.get('sess-123'), v, '改名后应能用真实 id 取到同一份')

  const c2 = new SessionCache(4)
  c2.put('main', viewOf('旧'))
  const fresh = viewOf('新')
  c2.put('sess-123', fresh)
  c2.rename('main', 'sess-123')
  assert.equal(c2.get('sess-123'), fresh, '目标已有更新鲜的缓存,不该被占位那份覆盖')
})

test('drop:会话被删后不占内存', () => {
  const c = new SessionCache(4)
  c.put('a', viewOf('A'))
  c.drop('a')
  assert.equal(c.size(), 0)
})

test('switchPlan:命中缓存保留流/metas 且**不清**游标;未命中三件事一起清', () => {
  assert.deepEqual(switchPlan(true), { keepStream: true, clearCursor: false, resetMetas: false })
  assert.deepEqual(switchPlan(false), { keepStream: false, clearCursor: true, resetMetas: true })
  // 三者必须同进同出:一边保留流一边又把游标/metas 清了 = 下次连上按差集只发最后几条,
  // 或把刚从缓存装回的速记行又抹掉(两个都真发生过:第一个是极向写反,第二个是无条件清 metas)
  for (const has of [true, false]) {
    const p = switchPlan(has)
    assert.equal(p.clearCursor, !p.keepStream)
    assert.equal(p.resetMetas, !p.keepStream)
  }
})

test('缓存上限与页签上限相等(两者分开写死,靠本用例钉住不漂)', () => {
  assert.equal(SESSION_CACHE_LIMIT, DEFAULT_TAB_LIMIT)
  assert.equal(new SessionCache().size(), 0)
})