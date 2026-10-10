import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  boundSession,
  initSessionFromURL,
  sessionQS,
  setBoundSession,
  shouldFollowSwitch,
} from './session-scope.ts'

// 多窗口作用域:URL ?session= 决定本窗口看哪个会话;没带 = 主会话(行为与从前一致)。
test('session-scope:未绑定时 query 为空(旧客户端零变化)', () => {
  setBoundSession('')
  assert.equal(boundSession(), '')
  assert.equal(sessionQS(), '')
  assert.equal(sessionQS({ before: 10, limit: 50 }), '?before=10&limit=50')
})

test('session-scope:绑定后所有 query 都带 session', () => {
  setBoundSession('20260930-101010')
  assert.equal(sessionQS(), '?session=20260930-101010')
  assert.equal(sessionQS({ after: 7 }), '?session=20260930-101010&after=7')
  assert.equal(sessionQS({ after: undefined }), '?session=20260930-101010')
})

test('session-scope:从 URL 读取并做 URL 解码往返', () => {
  const g = globalThis as unknown as { window?: { location: { search: string } } }
  const orig = g.window
  g.window = { location: { search: '?session=a-b%20c' } }
  try {
    assert.equal(initSessionFromURL(), 'a-b c')
    assert.equal(sessionQS(), '?session=a-b+c')
  } finally {
    if (orig) g.window = orig
    else delete g.window
    setBoundSession('')
  }
})

// W1 根修:服务端当前会话切走的显式信号到达时,谁跟随、谁不动。
// 三条「不跟随」各自防一类误动作:多窗口把本窗口从钉住的会话上拽走;用户正看着别的会话时
// 把它改绑走;以及重复/空信号下的无效跟随(空 id 交给快照兜底)。
test('follow-switch:只有「代表服务端当前会话」的当前页签跟随', () => {
  // 主页签在当前视图,信号说切到了 s2 → 跟随
  assert.equal(shouldFollowSwitch('s2', 'main', 'main', 'main'), true)
  assert.equal(shouldFollowSwitch('s2', 's1', 's1', 's1'), true)
  // 多窗口 / ?session= 启动:本窗口没有主页签(null)⇒ 不动
  assert.equal(shouldFollowSwitch('s2', null, 'hist-1', 'hist-1'), false)
  // 用户正看着**别的**页签:不能把它改绑走(主页签切回去时再追)
  assert.equal(shouldFollowSwitch('s2', 'main', 'hist-1', 'hist-1'), false)
  // 已经在这个会话上了 / 空 id(旧形态、取不到当前会话)⇒ 不重复跟随,交快照兜底
  assert.equal(shouldFollowSwitch('s2', 'main', 'main', 's2'), false)
  assert.equal(shouldFollowSwitch('', 'main', 'main', 'main'), false)
  assert.equal(shouldFollowSwitch('   ', 'main', 'main', 'main'), false)
})

test('session-scope:无 window(SSR/单测环境)不炸,回落主会话', () => {
  const g = globalThis as unknown as { window?: unknown }
  const orig = g.window
  delete g.window
  try {
    assert.equal(initSessionFromURL(), '')
  } finally {
    if (orig !== undefined) g.window = orig
  }
})
