// 工具结果渲染器的选择规则(第一百六十五批)。
//
// 钉三件容易错的事:
//  ① 精确匹配优先于 '*' 兜底 —— 即使兜底插件 priority 更高("我想接管没人管的"不该
//     抢掉"我专门懂这个工具"的那一个);
//  ② 同档按 priority 降序;
//  ③ tools 为空的声明直接忽略(它永远不会匹配,注册了只会让人以为生效了)。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { registerToolRenderer, toolRendererFor, toolRendererSnapshot, clearToolRenderers } from './registry.ts'

function comp(name: string) {
  return { name } as never
}

test('toolRenderer:tools 为空的声明被忽略', () => {
  clearToolRenderers()
  registerToolRenderer('empty', { tools: [], component: comp('Empty'), priority: 99 })
  assert.equal(toolRendererFor('shell'), null)
  assert.deepEqual(toolRendererSnapshot(), {})
})

test('toolRenderer:精确匹配优先于更高优先级的通配', () => {
  clearToolRenderers()
  registerToolRenderer('wild', { tools: ['*'], component: comp('Wild'), priority: 100 })
  registerToolRenderer('img', { tools: ['image_ops'], component: comp('Img'), priority: 10 })
  assert.equal(toolRendererFor('image_ops')?.key, 'img')
  assert.equal(toolRendererFor('shell')?.key, 'wild', '没人专门管 ⇒ 兜底接管')
})

test('toolRenderer:同档 priority 高者胜(同 key 后写覆盖)', () => {
  clearToolRenderers()
  registerToolRenderer('a', { tools: ['shell'], component: comp('A'), priority: 5 })
  registerToolRenderer('b', { tools: ['shell'], component: comp('B'), priority: 8 })
  assert.equal(toolRendererFor('shell')?.key, 'b')
  registerToolRenderer('b', { tools: ['shell'], component: comp('B2'), priority: 1 })
  assert.equal(toolRendererFor('shell')?.key, 'a', '同 key 覆盖后优先级重算,该让 a 胜')
})

test('toolRenderer:快照按工具名给出插件 id(面板/文档断言用)', () => {
  clearToolRenderers()
  registerToolRenderer('img', { tools: ['image_ops', 'image_crop'], component: comp('Img'), priority: 10 })
  assert.deepEqual(toolRendererSnapshot(), { image_ops: 'img', image_crop: 'img' })
})
