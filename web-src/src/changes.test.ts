// S-P1-1 changes.ts 单测:聚合(file/change 事件 → 按文件累计)、路径定位、diff 行分类、格式化。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  changesPush,
  changesStats,
  diffLineKind,
  diffLines,
  fileChangeOf,
  findChange,
  fmtBytes,
  fmtClock,
  newChanges,
  type ChangesModel,
} from './changes.ts'
import type { SessionEvent } from './types.ts'

function ev(seq: number, payload: unknown, ts = ''): SessionEvent {
  return { Kind: 'file/change', Seq: seq, Payload: payload, TS: ts }
}

function build(...evs: SessionEvent[]): ChangesModel {
  const m = newChanges()
  for (const e of evs) changesPush(m, e)
  return m
}

// 线形状护栏:夹具必须与 sdk.FileChangeEvent 的 json tag 一致(小写)。
// 2026-09-18 的失效正是「TS 按 PascalCase 取值 + 线上是小写」——两边各自都对,
// 合起来静默取空;这里用一个真实抓包样本钉住形状。
test('载荷键名与线上形状一致(小写,取自真实抓包)', () => {
  const wire = {
    path: '/tmp/gahboard/board-check.txt',
    rel: 'board-check.txt',
    op: 'write',
    tool: 'file_write',
    added: 1,
    removed: 0,
    created: true,
    bytes: 15,
    diff: '@@ -1,0 +1,1 @@\n+hello from mock\n',
  }
  const m = newChanges()
  assert.equal(changesPush(m, ev(1, wire)), true)
  assert.deepEqual(changesStats(m), { files: 1, added: 1, removed: 0, changes: 1 })
  assert.equal(m.files[0].key, 'board-check.txt')
  assert.equal(m.files[0].path, '/tmp/gahboard/board-check.txt')
  assert.equal(m.files[0].hunks[0].tool, 'file_write')
})

test('聚合同一文件多次改动:计数累加、操作去重保序、hunk 保时间序', () => {
  const m = build(
    ev(2, { path: '/w/a.go', rel: 'a.go', op: 'write', tool: 'file_write', added: 10, removed: 0, created: true, diff: '@@ -1,0 +1,10 @@\n+x\n' }, '2026-09-18T10:00:01Z'),
    ev(5, { path: '/w/a.go', rel: 'a.go', op: 'edit', tool: 'file_edit', added: 2, removed: 1, diff: '@@ -3,2 +3,3 @@\n-a\n+b\n+c\n' }, '2026-09-18T10:00:05Z'),
    ev(6, { path: '/w/a.go', rel: 'a.go', op: 'edit', tool: 'file_edit', added: 1, removed: 0, truncated: true, diff: '@@ -9,1 +9,2 @@\n+y\n' }),
    ev(7, { path: '/w/b.bin', rel: 'b.bin', op: 'append', binary: true, bytes: 4096 }),
  )
  assert.equal(m.total, 4)
  assert.equal(m.files.length, 2)
  const a = m.byKey['a.go']
  assert.equal(a.added, 13)
  assert.equal(a.removed, 1)
  assert.deepEqual(a.ops, ['write', 'edit'])
  assert.equal(a.created, true)
  assert.equal(a.truncated, true)
  assert.deepEqual(
    a.hunks.map((h) => h.seq),
    [2, 5, 6],
  )
  assert.equal(a.hunks[0].created, true)
  assert.equal(m.byKey['b.bin'].binary, true)
  assert.deepEqual(changesStats(m), { files: 2, added: 13, removed: 1, changes: 4 })
})

test('非 file/change 事件与缺路径载荷一律忽略(不产生幽灵文件)', () => {
  const m = newChanges()
  assert.equal(fileChangeOf({ Kind: 'tool/call', Seq: 1, Payload: {}, TS: '' } as SessionEvent), null)
  assert.equal(fileChangeOf(ev(2, null)), null)
  assert.equal(fileChangeOf(ev(3, { rel: '', path: '' })), null)
  assert.equal(fileChangeOf(ev(4, 'x')), null)
  assert.equal(changesPush(m, ev(5, { added: 3 })), false)
  assert.equal(changesPush(m, { Kind: 'turn/end', Seq: 6, Payload: {}, TS: '' } as SessionEvent), false)
  assert.equal(m.files.length, 0)
  assert.deepEqual(changesStats(m), { files: 0, added: 0, removed: 0, changes: 0 })
})

test('无 Rel 时以绝对路径为键(外部/极端路径不失真)', () => {
  const m = build(ev(1, { path: '/tmp/x.txt', op: 'write', added: 1 }))
  assert.equal(m.files[0].key, '/tmp/x.txt')
  assert.equal(m.files[0].path, '/tmp/x.txt')
})

test('findChange:精确 → 后缀 → 文件名,歧义取首个', () => {
  const m = build(
    ev(1, { path: '/w/plugins/host/jobs.go', rel: 'plugins/host/jobs.go', op: 'edit', added: 1 }),
    ev(2, { path: '/w/notes/new.md', rel: 'notes/new.md', op: 'write', added: 5 }),
    ev(3, { path: '/w/other/jobs.go', rel: 'other/jobs.go', op: 'edit', added: 2 }),
  )
  assert.equal(findChange(m, 'notes/new.md')?.key, 'notes/new.md')
  assert.equal(findChange(m, 'host/jobs.go')?.key, 'plugins/host/jobs.go')
  assert.equal(findChange(m, 'jobs.go')?.key, 'plugins/host/jobs.go') // 歧义 → 首个
  assert.equal(findChange(m, 'nope.go'), undefined)
  assert.equal(findChange(m, ''), undefined)
  assert.equal(findChange(m, 'other\\jobs.go')?.key, 'other/jobs.go') // 反斜杠归一
})

test('diffLineKind:头/新增/删除/上下文四类,前导空白容忍', () => {
  assert.equal(diffLineKind('@@ -1,3 +1,6 @@'), 'hunk')
  assert.equal(diffLineKind('+++ b/a.go'), 'file')
  assert.equal(diffLineKind('--- a/a.go'), 'file')
  assert.equal(diffLineKind('+x'), 'add')
  assert.equal(diffLineKind('    +x'), 'add')
  assert.equal(diffLineKind('-x'), 'del')
  assert.equal(diffLineKind(' x'), 'ctx')
  assert.equal(diffLineKind(''), 'ctx')
  // 业务文本里的 "++" 不在行首不应误判
  assert.equal(diffLineKind('a + b'), 'ctx')
})

test('diffLines:逐行分类且不吞中间空行(仅去尾部换行)', () => {
  const lines = diffLines('@@ -1,2 +1,3 @@\n a\n\n-b\n+c\n')
  assert.deepEqual(
    lines.map((l) => l.kind),
    ['hunk', 'ctx', 'ctx', 'del', 'add'],
  )
  assert.equal(lines[1].text, ' a')
  assert.equal(lines[2].text, '')
  assert.deepEqual(diffLines(''), [{ text: '', kind: 'ctx' }])
})

test('fmtBytes / fmtClock:格式化与解析失败不编造', () => {
  assert.equal(fmtBytes(0), '0 B')
  assert.equal(fmtBytes(512), '512 B')
  assert.equal(fmtBytes(2048), '2.0 KB')
  assert.equal(fmtBytes(3 * 1024 * 1024), '3.0 MB')
  assert.equal(fmtBytes(-5), '0 B')
  assert.equal(fmtClock(''), '')
  assert.equal(fmtClock('not-a-date'), '')
  assert.match(fmtClock('2026-09-18T10:00:05Z'), /^\d{2}:\d{2}:\d{2}$/)
})
