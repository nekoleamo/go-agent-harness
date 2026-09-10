// 文档预览意图通道单测(E-E;node --test)。
// 覆盖:工具参数摘要 → 可预览路径提取的扩展名白名单(含大小写、无 path、非白名单)。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { previewablePathOf } from './docstore.ts'

test('previewablePathOf 白名单内的扩展名可提取', () => {
  assert.equal(previewablePathOf('path=README.md'), 'README.md')
  assert.equal(previewablePathOf('command=x path="a/b.PDF"'), 'a/b.PDF')
  assert.equal(previewablePathOf('path=dir/report.xlsx lines=10'), 'dir/report.xlsx')
  assert.equal(previewablePathOf('path=notes.ipynb'), 'notes.ipynb')
})

test('previewablePathOf 非白名单/无参数返回空', () => {
  assert.equal(previewablePathOf('path=main.exe'), '')
  assert.equal(previewablePathOf('path=archive.zip'), '')
  assert.equal(previewablePathOf('command=ls -la'), '')
  assert.equal(previewablePathOf(''), '')
})

test('previewablePathOf 只认 path= 键(不误取其它含 path 的参数)', () => {
  assert.equal(previewablePathOf('datapath=README.md'), '')
  assert.equal(previewablePathOf('filepath=x.md'), '')
})
