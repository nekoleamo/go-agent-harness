// 介入条(D3)的纯逻辑用例:四态与优先级。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { interventionOf, type InterventionInput } from './intervention.ts'

const base: InterventionInput = {
  pendingConfirm: false,
  pendingQuestion: false,
  lastError: '',
  running: false,
  unreadBelow: false,
}

test('无介入 ⇒ 空态(不占位)', () => {
  const got = interventionOf({ ...base })
  assert.equal(got.kind, '')
  assert.equal(got.text, '')
})

test('四态各自呈现', () => {
  const appr = interventionOf({ ...base, pendingConfirm: true })
  assert.equal(appr.kind, 'approve')
  assert.ok(appr.text.includes('待审批'))

  const q = interventionOf({ ...base, pendingQuestion: true })
  assert.equal(q.kind, 'question')
  assert.ok(q.text.includes('待回答'))

  const err = interventionOf({ ...base, lastError: '端点 500' })
  assert.equal(err.kind, 'error')
  assert.ok(err.text.includes('出错'))
  assert.ok(err.text.includes('端点 500'))

  const done = interventionOf({ ...base, unreadBelow: true })
  assert.equal(done.kind, 'done')
  assert.ok(done.text.includes('跑完了'))
})

test('优先级:待审批 > 待回答 > 出错 > 跑完了', () => {
  const all = interventionOf({
    pendingConfirm: true,
    pendingQuestion: true,
    lastError: '炸了',
    running: false,
    unreadBelow: true,
  })
  assert.equal(all.kind, 'approve')
  assert.equal(interventionOf({ ...base, pendingQuestion: true, lastError: '炸了' }).kind, 'question')
  assert.equal(interventionOf({ ...base, lastError: '炸了', unreadBelow: true }).kind, 'error')
  assert.equal(interventionOf({ ...base, unreadBelow: true }).kind, 'done')
})

test('运行中不报「跑完了」', () => {
  const got = interventionOf({ ...base, running: true, unreadBelow: true })
  assert.equal(got.kind, '')
})

test('错误文本超长要裁(不留一行巨长文字)', () => {
  const got = interventionOf({ ...base, lastError: 'x'.repeat(300) })
  assert.ok(got.text.length <= 80, `过长: ${got.text.length}`)
  assert.ok(got.text.includes('…'))
})
