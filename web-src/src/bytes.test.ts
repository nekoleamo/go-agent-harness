import { test } from 'node:test'
import assert from 'node:assert/strict'
import { byteLength } from './bytes.ts'

test('ASCII:字节数 = 字符数', () => {
  assert.equal(byteLength(''), 0)
  assert.equal(byteLength('abc'), 3)
  assert.equal(byteLength('a\nb'), 3)
})

test('中文:一个字 3 字节(与 Go 的 len(string) 同口径,不是 length)', () => {
  assert.equal('中文'.length, 2) // JS 码元数
  assert.equal(byteLength('中文'), 6) // UTF-8 字节 —— 面板显示的必须是这个
  assert.equal(byteLength('全局指令'), 12)
})

test('非 BMP 字符:UTF-16 码元 2 个,UTF-8 字节 4 个', () => {
  assert.equal('🙂'.length, 2)
  assert.equal(byteLength('🙂'), 4)
})

test('混排按各自字节累加', () => {
  assert.equal(byteLength('role: 财务 ok 🙂'), 'role: '.length + 6 + ' ok '.length + 4)
})
