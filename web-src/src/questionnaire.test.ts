// 整卷问卷状态机单测(P3 扩展;node --test,零新增依赖)。
// 覆盖:初始化(补推已答)/伪选项/单选前进/多选勾选与空提交/手写覆盖/聊聊这个/
// 确认页跳回/切题循环/摘要。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  qnAfterAnswer,
  qnAllAnswered,
  qnAnswered,
  qnCommitMulti,
  qnCurrent,
  qnFreeTextOnly,
  qnGoTab,
  qnInit,
  qnItems,
  qnMove,
  qnOpenInput,
  qnPickOption,
  qnSetInput,
  qnSubmitInput,
  qnSummary,
  qnTab,
  qnToggle,
} from './questionnaire.ts'
import type { QuestionnaireRequest } from './types.ts'

function req(): QuestionnaireRequest {
  return {
    id: 'qn1',
    title: '本轮待确认',
    questions: [
      {
        id: 'scope',
        label: 'Scope',
        prompt: '范围到哪?',
        hint: '决定切片',
        options: [
          { value: 'web', label: '只做 Web（推荐）', desc: '最快' },
          { value: 'all', label: '三端' },
        ],
      },
      { id: 'prio', label: '优先级', prompt: '先做哪个?', multiple: true, options: [{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }] },
      { id: 'free', label: '补充', prompt: '还有什么?' },
    ],
  }
}

// 伪选项:有选项时追加「自己输入」「聊聊这个」;无选项时一个都不给(输入框是唯一路径)。
test('qnItems 追加伪选项 / 无选项不追加', () => {
  const r = req()
  const items = qnItems(r.questions[0])
  assert.deepEqual(items.map((i) => i.kind), ['option', 'option', 'other', 'chat'])
  assert.equal(qnItems(r.questions[2]).length, 0)
  assert.equal(qnFreeTextOnly(r.questions[2]), true)
  // allow_other/allow_chat = false 时不追加
  const bare = { ...r.questions[0], allow_other: false, allow_chat: false }
  assert.deepEqual(qnItems(bare).map((i) => i.kind), ['option', 'option'])
})

// 初始化:补推的 answered 直接进 draft ⇒ tab 栏 ■ 与确认页摘要都能恢复。
test('qnInit 接收补推已答', () => {
  const r = req()
  r.answered = { scope: { values: ['web'], index: 1 }, prio: { values: ['a', 'b'] } }
  const s = qnInit(r)
  assert.equal(qnAnswered(s, r.questions[0]), true)
  assert.equal(qnAnswered(s, r.questions[1]), true)
  assert.equal(qnAnswered(s, r.questions[2]), false)
  assert.equal(qnAllAnswered(s, r), false)
  // 已有两题作答 ⇒ 光标落在第一道未答题(恢复位置而不是回第 1 题)
  assert.equal(qnCurrent(s, r)?.id, 'free')
})

// 刷新恢复:光标落在第一道未答的题(不是永远回第 1 题);全答完直接到确认页。
test('qnInit 恢复位置', () => {
  const r = req()
  r.answered = { scope: { values: ['web'], index: 1 } }
  assert.equal(qnInit(r).tab, 1)
  r.answered = { scope: { values: ['web'] }, prio: { values: ['a'] }, free: { text: '好', custom: true } }
  assert.equal(qnInit(r).tab, r.questions.length)
  delete r.answered
  assert.equal(qnInit(r).tab, 0)
})

// 单选落地:记 values+index,并自动前进到下一题(多题)。
test('qnPickOption 单选 → 前进', () => {
  const r = req()
  let s = qnInit(r)
  s = qnPickOption(s, r, { kind: 'option', index: 1, value: 'all', label: '三端' })
  assert.deepEqual(s.draft.scope.values, ['all'])
  assert.equal(s.draft.scope.index, 2)
  assert.equal(s.tab, 1)
})

// 单题问卷:答完原地不动(由调用方立即收口,不跳确认页)。
test('qnAfterAnswer 单题原地', () => {
  const one: QuestionnaireRequest = { id: 'x', questions: [req().questions[0]] }
  const s = qnAfterAnswer(qnInit(one), one)
  assert.equal(s.tab, 0)
})

// 多选:勾选/取消勾选;空集合回车 → 一次性提示且**不前进**。
test('qnToggle + qnCommitMulti 空集合提示', () => {
  const r = req()
  let s = qnGoTab(qnInit(r), r, 1)
  s = qnToggle(s, r, { kind: 'option', index: 0, value: 'a', label: 'A' })
  s = qnToggle(s, r, { kind: 'option', index: 1, value: 'b', label: 'B' })
  assert.deepEqual(s.draft.prio.values, ['a', 'b'])
  s = qnToggle(s, r, { kind: 'option', index: 0, value: 'a', label: 'A' })
  assert.deepEqual(s.draft.prio.values, ['b'])
  s = qnToggle(s, r, { kind: 'option', index: 1, value: 'b', label: 'B' }) // 清空
  assert.deepEqual(s.draft.prio.values, [])
  const kept = qnCommitMulti(s, r)
  assert.equal(kept.tab, 1)
  assert.match(kept.notice, /勾选/)
  // 有勾选才前进
  s = qnToggle(s, r, { kind: 'option', index: 0, value: 'a', label: 'A' })
  assert.equal(qnCommitMulti(s, r).tab, 2)
})

// 手写:覆盖该题(值清空、custom=true);空白按 (no response) 记,不丢题。
test('qnSubmitInput 手写覆盖与空白兜底', () => {
  const r = req()
  let s = qnOpenInput(qnInit(r), 'scope')
  assert.equal(s.inputQid, 'scope')
  s = qnSetInput(s, '  只做 Web 的第一屏  ')
  s = qnSubmitInput(s, r)
  assert.equal(s.draft.scope.text, '只做 Web 的第一屏')
  assert.deepEqual(s.draft.scope.values, [])
  assert.equal(s.draft.scope.custom, true)
  assert.equal(s.tab, 1)

  let blank = qnOpenInput(qnInit(r), 'free')
  blank = qnSubmitInput(blank, r)
  assert.equal(blank.draft.free.text, '(no response)')
  assert.equal(blank.inputQid, null)
})

// 聊聊这个:chat 置为题 id(调用方据此收口),不动已答。
test('qnPickOption chat 逃生口', () => {
  const r = req()
  const s = qnPickOption(qnInit(r), r, { kind: 'chat' })
  assert.equal(s.chat, 'scope')
  assert.equal(s.tab, 0)
  assert.equal(qnAnswered(s, r.questions[0]), false)
})

// 切题:循环(含确认页);直接跳 tab 越界忽略;切题清光标与手写态。
test('qnTab / qnGoTab 边界', () => {
  const r = req()
  let s = qnInit(r)
  s = qnTab(s, r, -1)
  assert.equal(s.tab, r.questions.length) // 从第 0 题往前 = 确认页
  s = qnTab(s, r, 1)
  assert.equal(s.tab, 0)
  s = qnGoTab(s, r, 2)
  assert.equal(s.tab, 2)
  assert.equal(qnGoTab(s, r, 9).tab, 2) // 越界不改
  const input = qnOpenInput(qnGoTab(qnInit(r), r, 0), 'scope')
  assert.equal(qnGoTab(input, r, 1).inputQid, null)
})

// 光标夹紧(确认页无光标)。
test('qnMove 夹紧', () => {
  const r = req()
  let s = qnMove(qnInit(r), r, -5)
  assert.equal(s.sel, 0)
  s = qnMove(s, r, 99)
  assert.equal(s.sel, qnItems(r.questions[0]).length - 1)
  const confirm = qnGoTab(qnInit(r), r, r.questions.length)
  assert.equal(qnMove(confirm, r, 1).sel, 0)
})

// 摘要:未答显式标 (未答);手写标 custom;多选拼标签。
test('qnSummary 三种口径', () => {
  const r = req()
  let s = qnInit(r)
  s = qnPickOption(s, r, { kind: 'option', index: 0, value: 'web', label: '只做 Web' })
  s = qnGoTab(s, r, 1)
  s = qnToggle(s, r, { kind: 'option', index: 0, value: 'a', label: 'A' })
  s = qnToggle(s, r, { kind: 'option', index: 1, value: 'b', label: 'B' })
  const sum = qnSummary(r, s)
  assert.equal(sum[0].text, '只做 Web（推荐）')
  assert.equal(sum[1].text, 'A、B')
  assert.equal(sum[2].text, '(未答)')
  // 手写口径
  let w = qnOpenInput(qnInit(r), 'free')
  w = qnSetInput(w, '还想加个 i18n')
  w = qnSubmitInput(w, r)
  assert.equal(qnSummary(r, w)[2].custom, true)
  assert.equal(qnSummary(r, w)[2].text, '还想加个 i18n')
})
