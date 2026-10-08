// 作用域判据单测(第一百三十四批)。
//
// 这组测试的由来是一个**真 bug**:第一版判据用 `model_from === 'session'`,而
// sdk.EffectiveModel 只要没有角色声明就返回 "session" —— 于是**跟随全局的会话也被
// 判成独立**,页签条挂满方块。测试桩当时只在 override 时才给 model_from,把 bug 藏住了。
// 下面第一组就是为「别再退回 *__from 判据」钉的。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { hasSessionOverride, isSessionSet, sessionSetCount } from './scope.ts'

test('跟随全局:session_prefs 缺省 ⇒ 全 false', () => {
  assert.equal(isSessionSet({}, 'model'), false)
  assert.equal(hasSessionOverride({}), false)
  assert.equal(sessionSetCount({}), 0)
})

test('跟随全局:session_prefs 带全 false 也算跟随', () => {
  const s = { session_prefs: { model: false, thinking: false, sandbox: false, approval: false } }
  assert.equal(hasSessionOverride(s), false, '后端只带 true 的键;带 false 也不能算独立')
})

test('独立项被逐项识别', () => {
  const s = { session_prefs: { model: true, sandbox: true } }
  assert.equal(isSessionSet(s, 'model'), true)
  assert.equal(isSessionSet(s, 'sandbox'), true)
  assert.equal(isSessionSet(s, 'thinking'), false)
  assert.equal(isSessionSet(s, 'approval'), false)
  assert.equal(sessionSetCount(s), 2)
  assert.equal(hasSessionOverride(s), true)
})

test('角色给的 model_from 不算独立(它已不在判据里)', () => {
  // 这一条是回归钉子:早期判据读 model_from,而后端在「没有角色」时**也**返回 'session',
  // 于是跟随全局的会话被误判。真判据只看 session_preps,与 model_from 无关。
  const s = { model_from: 'session', session_prefs: {} } as unknown as { session_prefs?: Record<string, boolean> }
  assert.equal(hasSessionOverride(s), false, 'model_from="session" 不足以判定独立')
  const s2 = { model_from: 'role', session_prefs: { model: true } } as unknown as { session_prefs?: Record<string, boolean> }
  assert.equal(hasSessionOverride(s2), true, '角色指定的同时会话也压过 → 独立(会话档优先,会话自己设过)')
})

test('未知键不影响计数', () => {
  const s = { session_prefs: { role: true, model: true, 未知键: true } }
  assert.equal(sessionSetCount(s), 1, 'role 与未知键都不计入四项运行参数')
})

test('四个字段各设一次都能数到', () => {
  for (const f of ['model', 'thinking', 'sandbox', 'approval'] as const) {
    assert.equal(sessionSetCount({ session_prefs: { [f]: true } }), 1, f)
  }
})