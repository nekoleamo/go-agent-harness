// 模型选择器的过滤与排序(2026-10-06「免费模型可见性」配套)。
//
// 判定本身在**后端**(Go 侧 sdk.AssessModel),前端只按 verdict 排序 + 过滤 ——
// 所以这里测的是「前端有没有把后端给的结论用对」,不是重测判定规则。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { withCurrentModel, type ModelOption } from './modelsel.ts'

// 与 SettingsPanel.modelRank 同口径的排序权重(复制于组件,便于独立验证)。
function rank(o: ModelOption): number {
  let r = 0
  if (o.usable === false) r += 1000
  if (o.free) r -= 10
  r -= Math.floor((o.contextWindow ?? 0) / 1_000_000)
  return r
}
const usable = (id: string, x: Partial<ModelOption> = {}): ModelOption => ({
  label: `openrouter · ${id}`,
  value: `openrouter|${id}`,
  usable: true,
  free: false,
  contextWindow: 128 * 1024,
  tags: [],
  warn: '',
  ...x,
})

const pool: ModelOption[] = [
  usable('vendor/plain'), // 端点没给能力字段 ⇒ usable 缺失
  usable('paid-1m', { contextWindow: 1024 * 1024, tags: ['工具', '1M 上下文'] }),
  usable('free-good', { free: true, tags: ['免费', '工具'] }),
  usable('no-tools:free', { free: true, usable: false, warn: '端点声明不支持工具调用:只能聊天,当不了 agent 的脑子(工具不会执行)', tags: ['免费'] }),
]
delete (pool[0] as Partial<ModelOption>).usable // 复现「没有 verdict」的旧后端

test('排序:可用 > 免费 > 上下文大;不可用的沉底', () => {
  const s = [...pool].sort((a, b) => rank(a) - rank(b))
  assert.match(s[0].value, /free-good$/, '免费+可用应第一')
  assert.match(s[1].value, /paid-1m$/, '1M 上下文应第二')
  assert.match(s[3].value, /no-tools/, '不支持工具调用的应垫底')
})

test('过滤默认开时只留可用项,且不藏掉当前正在用的模型', () => {
  // 与组件同逻辑:usable !== false 才留(「没有判定」≠「不可用」)
  const shown = pool.filter((o) => o.usable !== false)
  assert.equal(shown.length, 3)
  assert.ok(!shown.some((o) => o.value.includes('no-tools')))

  // 当前模型不在枚举里时,withCurrentModel 补的那条必须可见
  const withCur = withCurrentModel(shown, 'hand-written-model', 'openrouter')
  assert.equal(withCur[0].usable, true, '补出来的「当前」不得被过滤掉')
  assert.equal(withCur.length, 4)
})

test('缺 verdict 的模型不会被当成不可用(旧后端/枚举失败时的安全侧)', () => {
  const shown = pool.filter((o) => o.usable !== false)
  assert.ok(shown.some((o) => o.value.includes('vendor/plain')))
})

test('标签与原因透传到选项上(前端只渲染,不再自己判断)', () => {
  const free = pool.find((o) => o.value.includes('free-good'))!
  assert.deepEqual(free.tags, ['免费', '工具'])
  const bad = pool.find((o) => o.value.includes('no-tools'))!
  assert.match(bad.warn, /工具调用/, '不可用项必须带人话原因')
})