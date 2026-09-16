import { test } from 'node:test'
import assert from 'node:assert/strict'
import { sameDir } from './wsdir.ts'

test('同一目录的不同写法算同一个(分隔符/大小写/尾部分隔符)', () => {
  assert.equal(sameDir('D:\\work\\proj', 'd:/work/proj/'), true)
  assert.equal(sameDir('C:\\work\\proj\\', 'C:\\work\\proj'), true)
  assert.equal(sameDir('/Users/x/proj/', '/Users/X/PROJ'), true)
  assert.equal(sameDir('C:\\', 'c:/'), true)
  assert.equal(sameDir('/', '/'), true)
})

test('不同目录不误判', () => {
  assert.equal(sameDir('D:\\work\\proj', 'D:\\work\\proj2'), false)
  assert.equal(sameDir('D:\\work', 'D:\\work\\sub'), false)
  // 大小写不同但确实是同一盘下不同目录的场景不存在,这里只保证不把前缀当相等
  assert.equal(sameDir('/a/b', '/a/bc'), false)
})

test('空值不当成相等(旧记录里 dir 可能为空)', () => {
  assert.equal(sameDir('', ''), false)
  assert.equal(sameDir('D:\\work', ''), false)
  assert.equal(sameDir('', '/tmp'), false)
})
