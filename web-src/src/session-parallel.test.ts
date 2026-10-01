import { test } from 'node:test'
import assert from 'node:assert/strict'
import { othersRunning, runLabel, sessionRunning } from './session-parallel.ts'

// 多会话并行视图的派生逻辑。核心诚实边界:**步数只对本窗口会话给**,
// 别的会话只说「运行中」——事件连接按会话过滤,收不到别的会话的帧,
// 编一个步数会让用户以为它卡住了。

test('sessionRunning:按 id 判定(主会话用自己的 id)', () => {
  assert.equal(sessionRunning(['a', 'b'], 'b'), true)
  assert.equal(sessionRunning(['a'], 'b'), false)
  assert.equal(sessionRunning(undefined, 'b'), false)
})

test('runLabel:本窗口会话给步数,别的会话只说运行中', () => {
  assert.equal(runLabel(['cur'], 'cur', 'cur', 3), '运行中 · 第 3 步')
  assert.equal(runLabel(['other'], 'other', 'cur', 3), '运行中')
  assert.equal(runLabel(['cur'], 'cur', 'cur', 0), '运行中')
  assert.equal(runLabel([], 'cur', 'cur', 3), '')
})

test('runLabel:curId 为空(未绑定)时也不给步数', () => {
  assert.equal(runLabel(['s1'], 's1', '', 5), '运行中')
})

test('othersRunning:本会话在跑时不提示(底栏已写着运行中)', () => {
  assert.equal(othersRunning(['a', 'b'], true), 0)
  assert.equal(othersRunning(['a', 'b'], false), 2)
  assert.equal(othersRunning(undefined, false), 0)
})
