import { test } from 'node:test'
import assert from 'node:assert/strict'
import { currentModelValue, modelOptionValue, withCurrentModel, type ModelOption } from './modelsel.ts'

const opts: ModelOption[] = [
  { label: 'deepseek · deepseek-chat', value: 'deepseek|deepseek-chat' },
  { label: 'deepseek · deepseek-reasoner', value: 'deepseek|deepseek-reasoner' },
]

test('模型在枚举里:高亮命中,且不额外补行', () => {
  const o = withCurrentModel(opts, 'deepseek-chat', 'deepseek')
  assert.equal(o.length, 2)
  assert.equal(currentModelValue(o, 'deepseek-chat', 'deepseek'), 'deepseek|deepseek-chat')
})

test('模型不在枚举里(手填):补一条 (当前),高亮有落点', () => {
  const o = withCurrentModel(opts, 'my-custom-model', 'deepseek')
  assert.equal(o.length, 3)
  assert.equal(o[0].value, 'deepseek|my-custom-model')
  assert.match(o[0].label, /\(当前\)$/)
  assert.equal(currentModelValue(o, 'my-custom-model', 'deepseek'), 'deepseek|my-custom-model')
})

test('活跃 provider 与模型归属不一致:按模型 ID 唯一匹配(旧实现这里永远高亮不上)', () => {
  const o = withCurrentModel(opts, 'deepseek-chat', 'openrouter')
  // 不再凭空补一条错的 provider 行,而是在枚举里唯一命中
  assert.equal(o.length, 2)
  assert.equal(currentModelValue(o, 'deepseek-chat', 'openrouter'), 'deepseek|deepseek-chat')
})

test('多个 provider 都有同名模型:精确匹配优先,歧义时不猜', () => {
  const dup: ModelOption[] = [...opts, { label: 'other · deepseek-chat', value: 'other|deepseek-chat' }]
  assert.equal(currentModelValue(dup, 'deepseek-chat', 'other'), 'other|deepseek-chat')
  assert.equal(currentModelValue(dup, 'deepseek-chat', 'nobody'), '')
  // 歧义 + 活跃 provider 对不上 ⇒ 补一条(当前)作为落点,保证有高亮
  const filled = withCurrentModel(dup, 'deepseek-chat', 'nobody')
  assert.equal(filled[0].value, modelOptionValue('nobody', 'deepseek-chat'))
  assert.equal(currentModelValue(filled, 'deepseek-chat', 'nobody'), filled[0].value)
})

test('state 未给出模型 / 列表为空:不高亮也不报错', () => {
  assert.equal(currentModelValue(opts, '', 'deepseek'), '')
  assert.equal(currentModelValue([], 'x', 'deepseek'), '')
  assert.deepEqual(withCurrentModel([], '', 'deepseek'), [])
  assert.equal(currentModelValue(opts, '   ', 'deepseek'), '')
})
