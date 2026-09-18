// 连接状态机单测(S-P1-3;node --test,零新增依赖)。
// 覆盖:三态迁移、网络层优先级、探活语义(成功只解拦截不谎报已连接)、
// 无变化返回原对象(避免重渲染)、提交拦截与文案口径。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { connLabel, connReduce, offlineHint, submitAllowed, type ConnModel } from './conn.ts'

const m = (state: ConnModel['state'], netOnline = true): ConnModel => ({ state, netOnline })

test('链路事件:open/retrying/closed 三态迁移', () => {
  assert.equal(connReduce(m('reconnecting'), { k: 'link-open' }).state, 'open')
  assert.equal(connReduce(m('open'), { k: 'link-retrying' }).state, 'reconnecting')
  assert.equal(connReduce(m('open'), { k: 'link-closed' }).state, 'offline')
  assert.equal(connReduce(m('reconnecting'), { k: 'link-closed' }).state, 'offline')
})

test('网络层优先:浏览器说没网时链路事件不足以判定可用', () => {
  const off = m('open', false)
  assert.equal(connReduce(off, { k: 'link-open' }).state, 'offline') // 睡眠后旧 socket 自认为开着
  assert.equal(connReduce(off, { k: 'link-retrying' }).state, 'offline')
  assert.equal(connReduce(m('open'), { k: 'net', online: false }).state, 'offline')
})

test('网络恢复:先落 reconnecting(等链路确认),不直接谎报已连接', () => {
  const back = connReduce(m('offline', false), { k: 'net', online: true })
  assert.deepEqual(back, { state: 'reconnecting', netOnline: true })
  assert.equal(connReduce(back, { k: 'link-open' }).state, 'open')
})

test('net 事件重复上报不改变状态(在线→在线保持原态)', () => {
  assert.equal(connReduce(m('open'), { k: 'net', online: true }).state, 'open')
  assert.equal(connReduce(m('reconnecting'), { k: 'net', online: true }).state, 'reconnecting')
})

test('探活:失败即离线;成功只解除离线拦截(事件通道未确认前保持 reconnecting)', () => {
  assert.equal(connReduce(m('open'), { k: 'probe', ok: false }).state, 'offline')
  assert.equal(connReduce(m('reconnecting'), { k: 'probe', ok: false }).state, 'offline')
  assert.equal(connReduce(m('offline'), { k: 'probe', ok: true }).state, 'reconnecting')
  assert.equal(connReduce(m('reconnecting'), { k: 'probe', ok: true }).state, 'reconnecting')
  assert.equal(connReduce(m('open'), { k: 'probe', ok: true }).state, 'open')
  // 探活成功但浏览器已报无网 → 不可信,保持离线
  assert.equal(connReduce(m('open', false), { k: 'probe', ok: true }).state, 'offline')
})

test('无变化时返回原对象(引用相等,避免无谓重渲染)', () => {
  const a = m('open')
  assert.equal(connReduce(a, { k: 'link-open' }), a)
  assert.equal(connReduce(a, { k: 'net', online: true }), a)
  assert.equal(connReduce(a, { k: 'probe', ok: true }), a)
  const b = m('offline', false)
  assert.equal(connReduce(b, { k: 'link-closed' }), b)
  assert.equal(connReduce(b, { k: 'net', online: false }), b)
  // 有变化则返回新对象
  assert.notEqual(connReduce(a, { k: 'link-closed' }), a)
})

test('提交拦截:仅明确离线时禁止(重连中仍允许 —— REST 上行与事件通道相互独立)', () => {
  assert.equal(submitAllowed(m('open')), true)
  assert.equal(submitAllowed(m('reconnecting')), true)
  assert.equal(submitAllowed(m('offline')), false)
})

test('标签与横幅文案:重连中不弹横幅(状态栏角标已足够)', () => {
  assert.equal(connLabel('open'), '已连接')
  assert.equal(connLabel('reconnecting'), '重连中')
  assert.equal(connLabel('offline'), '已断开')
  assert.equal(offlineHint(m('open')), '')
  assert.equal(offlineHint(m('reconnecting')), '')
  assert.match(offlineHint(m('offline')), /草稿与附件已保留/)
  assert.match(offlineHint(m('offline', false)), /网络不可用/)
  assert.match(offlineHint(m('offline', false)), /草稿与附件已保留/)
})

test('离线 → 恢复完整链路迁移(睡眠/切网真场景)', () => {
  let c = m('open')
  c = connReduce(c, { k: 'net', online: false }) // 合盖/切网
  assert.equal(c.state, 'offline')
  c = connReduce(c, { k: 'net', online: true }) // 唤醒
  assert.equal(c.state, 'reconnecting')
  c = connReduce(c, { k: 'probe', ok: true }) // 主动探活:服务端还在
  assert.equal(c.state, 'reconnecting')
  c = connReduce(c, { k: 'link-open' }) // WS 重连成功
  assert.equal(c.state, 'open')
  assert.equal(offlineHint(c), '')
})
