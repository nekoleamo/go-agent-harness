// provider 自定义请求头解析(2026-10-06 随 OpenCode Go 接入)。
//
// 钉的是三条边界:注释/空行跳过、坏行**报错并指出行号**、空值=删键。
// 第三条尤其要紧:静默跳过会让人以为头生效了,而没生效的后果是网关限速/拒绝,现场极难定位。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { parseHeaderLines } from './providerheaders.ts'

test('逐行解析:裁空白、值内空白保留', () => {
  const got = parseHeaderLines('  x-a :  1  \nx-b:值 里 有 空格\n')
  assert.deepEqual(got, { 'x-a': '1', 'x-b': '值 里 有 空格' })
})

test('空行与 # 注释跳过', () => {
  const got = parseHeaderLines('# 这是注释\n\n   \nx-a: 1\n#x-b: 2\n')
  assert.deepEqual(got, { 'x-a': '1' })
})

test('坏行报错并指出行号(不静默跳过)', () => {
  // 少冒号
  assert.throws(() => parseHeaderLines('x-a: 1\n这行没有冒号'), /第 2 行/)
  // 冒号在最前 = 没有键
  assert.throws(() => parseHeaderLines(': 值'), /第 1 行/)
})

test('空值合法:语义是「删掉这个键」(与 Go 侧合并规则一致)', () => {
  assert.deepEqual(parseHeaderLines('x-a:'), { 'x-a': '' })
})

test('空输入 → 空对象(而不是 null)', () => {
  assert.deepEqual(parseHeaderLines(''), {})
})
