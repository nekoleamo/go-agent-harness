// 帧归属判定测试(纯函数;Node 内置 test 跑,无框架)。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { frameOwner, ownedHere, shortSessionId, foreignTodoText } from './frame-routing.ts'

test('空归属映射成主会话 id(与服务端口径一致)', () => {
  assert.equal(frameOwner({}, 'cur-1'), 'cur-1')
  assert.equal(frameOwner({ session: '' }, 'cur-1'), 'cur-1')
  assert.equal(frameOwner({ session: 'sB' }, 'cur-1'), 'sB')
})

test('本会话的帧归本会话', () => {
  assert.ok(ownedHere({ session: 'cur-1' }, 'cur-1'))
  assert.ok(ownedHere({}, 'cur-1'))
  assert.ok(ownedHere({}, ''))
})

test('别的会话的帧不归本会话(这是要拦住的那一类)', () => {
  assert.equal(ownedHere({ session: 'sB' }, 'cur-1'), false)
  // 连空主会话对比都不能被绕过:主会话 id 为空时,任何非空归属都是别人的
  assert.equal(ownedHere({ session: 'sB' }, ''), false)
})

test('会话 id 短形', () => {
  assert.equal(shortSessionId('20260107-143022'), '143022')
  assert.equal(shortSessionId('20260107-143022-2'), '143022')
  assert.equal(shortSessionId(''), '')
  assert.equal(shortSessionId('  '), '')
})

test('别的会话待办文案:说清是谁 + 是什么,且不丢正文', () => {
  assert.match(foreignTodoText('confirm', '20260107-143022', '删除全部文件?'), /^会话 143022有待确认的操作:删除全部文件\?$/)
  assert.equal(foreignTodoText('question', '20260107-143022', ''), '会话 143022有待你作答的问题')
  assert.equal(foreignTodoText('command', '', 'x'), '另一个会话有命令输出:x')
})

test('超长正文截断(一行 meta 行不铺满屏幕)', () => {
  const long = 'x'.repeat(300)
  const t = foreignTodoText('confirm', 's1', long)
  assert.ok(t.length < 120, '应被截断,实际 ' + t.length)
})
// foreignOwner:拿不准就当自己的(误拒弹层 = 回合永远阻塞,代价不对称)。
import { foreignOwner } from './frame-routing.ts'

test('外来帧判定:只在两边都有 id 且明确不等时才判外来', () => {
  assert.equal(foreignOwner({ session: 'sB' }, 'cur-1'), 'sB')
  assert.equal(foreignOwner({ session: 'cur-1' }, 'cur-1'), '')
  assert.equal(foreignOwner({}, 'cur-1'), '', '未带归属(旧服务端/单例日志)不得判外来')
  assert.equal(foreignOwner({ session: 'sB' }, ''), '', '本会话 id 未就位时不得拒弹')
})

// 页签标题:服务端名可能是会话 id 本身,那种要退成短形(否则页签就是一串时间戳)。
import { tabTitle } from './frame-routing.ts'
test('页签标题:人写的名优先,id 样的名退成短形', () => {
  assert.equal(tabTitle('修 bug', '20260107-143022'), '修 bug')
  assert.equal(tabTitle('', '20260107-143022'), '143022')
  assert.equal(tabTitle('20260107-143022', '20260107-143022'), '143022', '名就是 id 时退成短形')
  assert.equal(tabTitle('会话 20260107-143022 的记录', '20260107-143022'), '143022', '名里含 id 也退成短形')
  assert.equal(tabTitle('', 'main'), '主会话')
  assert.equal(tabTitle('主会话', 'main'), '主会话')
})

// 会话级设置的来源标注:前端据此显示"本页签独立"还是"跟随全局"。
// 混起来说"这个值生效了"是不够的 —— 用户改了全局却发现某个页签没跟着变,会以为界面坏了。
import { prefSourceLabel } from './frame-routing.ts'
test('偏好来源标注', () => {
  assert.equal(prefSourceLabel('session'), '本会话')
  assert.equal(prefSourceLabel('role'), '角色')
  assert.equal(prefSourceLabel(''), '跟随全局')
  assert.equal(prefSourceLabel('approval'), '审批联动')
})
