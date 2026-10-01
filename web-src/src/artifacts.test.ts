import { test } from 'node:test'
import assert from 'node:assert/strict'
import { artifactLabel, artifactsBrief, artifactsOf, artifactsVisible } from './artifacts.ts'
import type { FileChange } from './changes.ts'

// 产物栏的纯逻辑。三条口径里最要紧的一条:
// **只列已落盘的改动** —— 被沙箱/审批拒绝的写压根没有 file/change 事件,
// 所以这里"不列"是正确的,不是数据缺失(界面里也这么写)。

function fc(path: string, over: Partial<FileChange> = {}): FileChange {
  return {
    key: path,
    path,
    added: 0,
    removed: 0,
    ops: ['write'],
    hunks: [],
    created: false,
    binary: false,
    truncated: false,
    ...over,
  }
}

function hunk(ts: string, op = 'write') {
  return { seq: 1, ts, op, tool: 'file_write', added: 1, removed: 0, diff: '', truncated: false, binary: false, created: false, coarse: false }
}

test('artifactsOf:按最近改动倒序(刚产出的排最前)', () => {
  const items = artifactsOf([
    fc('/w/a.md', { hunks: [hunk('2026-09-30T10:00:00Z')] }),
    fc('/w/b.md', { hunks: [hunk('2026-09-30T12:00:00Z')] }),
  ])
  assert.deepEqual(items.map((i) => i.path), ['/w/b.md', '/w/a.md'])
})

test('artifactsOf:删除以最后一个操作为准;新建单独一档', () => {
  const items = artifactsOf([
    fc('/w/gone.md', { hunks: [hunk('2026-09-30T10:00:00Z', 'write'), hunk('2026-09-30T11:00:00Z', 'delete')] }),
    fc('/w/new.md', { created: true, hunks: [hunk('2026-09-30T11:30:00Z', 'create')] }),
  ])
  const gone = items.find((i) => i.path === '/w/gone.md')!
  assert.equal(gone.deleted, true, '最后一个操作是 delete ⇒ 判删除')
  const nw = items.find((i) => i.path === '/w/new.md')!
  assert.equal(nw.created, true)
  assert.equal(nw.deleted, false)
})

test('artifactsOf:没 hunks 的条目不猜删除(缺失信息不编造)', () => {
  const items = artifactsOf([fc('/w/x.md')])
  assert.equal(items[0].deleted, false)
  assert.equal(items[0].created, false)
})

test('artifactsOf:空输入不炸(装 host-session-log 未装配时是空账本)', () => {
  assert.deepEqual(artifactsOf([] as FileChange[]), [])
})

test('artifactsBrief:空清单返回空串(不显示空栏)', () => {
  assert.equal(artifactsBrief([]), '')
})

test('artifactsBrief:文件数/新建/增删行', () => {
  const items = artifactsOf([
    fc('/w/a.md', { added: 10, removed: 2, hunks: [hunk('2026-09-30T10:00:00Z')] }),
    fc('/w/b.md', { created: true, added: 5, hunks: [hunk('2026-09-30T11:00:00Z')] }),
  ])
  const s = artifactsBrief(items)
  assert.match(s, /2 个文件/)
  assert.match(s, /新建 1/)
  assert.match(s, /\+15/)
  assert.match(s, /−2/)
})

test('artifactLabel:文件名 + 操作 + 行数(二进制不带行数)', () => {
  assert.equal(artifactLabel({ path: '/w/a.md', created: false, deleted: false, added: 3, removed: 1, binary: false, ts: '' }), 'a.md · 修改 +3 −1')
  assert.equal(artifactLabel({ path: '/w/n.txt', created: true, deleted: false, added: 0, removed: 0, binary: false, ts: '' }), 'n.txt · 新建')
  assert.match(artifactLabel({ path: '/w/p.png', created: false, deleted: false, added: 0, removed: 0, binary: true, ts: '' }), /二进制/)
})

test('artifactsVisible:空清单不占位(提示要有理由才出现)', () => {
  assert.equal(artifactsVisible([]), false)
  assert.equal(artifactsVisible([{ path: '/a', created: false, deleted: false, added: 0, removed: 0, binary: false, ts: '' }]), true)
})
