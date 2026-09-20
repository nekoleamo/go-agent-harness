// A-5#124 通知降级单测:本机来源判定、级别门槛、非本机来源**零请求**、
// 手势内一次申请、被拒后不再打扰、无 Notification 能力时安全空操作。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { type Notice } from './types.ts'
import { createNotifier, isLocalHost, wantsSystemNotify } from './notify.ts'

function notice(level: Notice['level'], body = 'b'): Notice {
  return { id: 1, level, title: 'gah 回合', body, ts: new Date().toISOString() }
}

// fakeNotification 记录 requestPermission 调用与真正弹出的通知。
function fakeNotification(permission: string, result = 'granted') {
  const calls = { request: 0, fired: [] as { title: string; body?: string }[] }
  const api: { permission: string; requestPermission(): Promise<string> } = {
    permission,
    requestPermission: () => {
      calls.request++
      if (result === 'granted') api.permission = 'granted' // 与真实浏览器一致:授权后 permission 回写
      return Promise.resolve(result)
    },
  }
  const ctor = (title: string, opts?: { body?: string }) => {
    calls.fired.push({ title, body: opts?.body })
  }
  return { api, ctor, calls }
}

test('isLocalHost:回环字面量与 *.localhost 算本机,LAN IP / 域名为外来源', () => {
  for (const h of ['localhost', '127.0.0.1', '::1', '[::1]', 'a.localhost', 'LOCALHOST']) {
    assert.equal(isLocalHost(h), true, h)
  }
  for (const h of ['192.168.1.66', 'example.com', '0.0.0.0', '10.0.0.5', '']) {
    assert.equal(isLocalHost(h), false, h)
  }
})

test('wantsSystemNotify:只有 warn/error 打扰人,info 与未知级别不弹', () => {
  assert.equal(wantsSystemNotify('warn'), true)
  assert.equal(wantsSystemNotify('error'), true)
  assert.equal(wantsSystemNotify('info'), false)
  assert.equal(wantsSystemNotify(undefined), false)
  assert.equal(wantsSystemNotify('critical'), false)
})

test('LAN IP 来源:fire 不弹,maybeRequest 也**不申请**任何权限', () => {
  const { api, ctor, calls } = fakeNotification('default')
  const n = createNotifier({ hostname: '192.168.1.66', api, ctor })
  n.maybeRequest()
  assert.equal(calls.request, 0, '非本机来源绝不调用 requestPermission')
  assert.equal(n.fire(notice('error')), 0)
  assert.equal(calls.fired.length, 0)
  assert.equal(n.enabled(), false)
})

test('本机来源:手势内申请一次;授权后 warn/error 弹出、info 不弹', () => {
  const { api, ctor, calls } = fakeNotification('default')
  const n = createNotifier({ hostname: 'localhost', api, ctor })
  n.maybeRequest()
  n.maybeRequest() // 第二次不应再申请
  assert.equal(calls.request, 1)
  return Promise.resolve().then(() => {
    assert.equal(n.enabled(), true)
    assert.equal(n.fire(notice('info')), 0, 'info 只进页内 toast')
    assert.equal(n.fire(notice('warn', '磁盘将满')), 1)
    assert.equal(calls.fired.length, 1)
    assert.equal(calls.fired[0].body, '磁盘将满')
  })
})

test('用户拒绝后:不再打扰(申请只一次,后续 fire 恒 0)', async () => {
  const { api, ctor, calls } = fakeNotification('default', 'denied')
  const n = createNotifier({ hostname: '127.0.0.1', api, ctor })
  n.maybeRequest()
  await Promise.resolve()
  await Promise.resolve()
  assert.equal(calls.request, 1)
  assert.equal(n.enabled(), false)
  assert.equal(n.fire(notice('error')), 0)
  n.maybeRequest()
  assert.equal(calls.request, 1, '被拒后不再弹权限条')
})

test('权限已是 denied:maybeRequest 不开权限条,fire 也不弹', () => {
  const { api, ctor, calls } = fakeNotification('denied')
  const n = createNotifier({ hostname: 'localhost', api, ctor })
  n.maybeRequest()
  assert.equal(calls.request, 0)
  assert.equal(n.fire(notice('error')), 0)
})

test('无 Notification 能力(旧浏览器/非安全上下文):全部安全空操作', () => {
  const n = createNotifier({ hostname: 'localhost' })
  n.maybeRequest()
  assert.equal(n.fire(notice('error')), 0)
  assert.equal(n.enabled(), false)
})
