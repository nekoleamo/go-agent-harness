import { test } from 'node:test'
import assert from 'node:assert/strict'
import { fetchShellNotices, hasShellQuery, sniffTauriInvoke, wailsUpdateSnapshot } from './desktop.ts'

const invoke = (): Promise<unknown> => Promise.resolve('ok')

test('壳内页面:认 __TAURI_INTERNALS__(withGlobalTauri 关掉时的唯一通道)', () => {
  assert.equal(sniffTauriInvoke({ __TAURI_INTERNALS__: { invoke } }), invoke)
})

test('兼容 withGlobalTauri 打开时的 __TAURI__.core.invoke', () => {
  assert.equal(sniffTauriInvoke({ __TAURI__: { core: { invoke } } }), invoke)
})

test('两者同时存在时优先 internals', () => {
  const other = (): Promise<unknown> => Promise.resolve('x')
  assert.equal(sniffTauriInvoke({ __TAURI_INTERNALS__: { invoke }, __TAURI__: { core: { invoke: other } } }), invoke)
})

test('浏览器直连:两者皆无 → undefined(壳专属入口一律不显示)', () => {
  assert.equal(sniffTauriInvoke({}), undefined)
  assert.equal(sniffTauriInvoke(null), undefined)
  assert.equal(sniffTauriInvoke(undefined), undefined)
})

test('形状不对(同名但非函数)不能误判成桌面', () => {
  assert.equal(sniffTauriInvoke({ __TAURI_INTERNALS__: {} }), undefined)
  assert.equal(sniffTauriInvoke({ __TAURI_INTERNALS__: { invoke: 5 } }), undefined)
  assert.equal(sniffTauriInvoke({ __TAURI__: { core: {} } }), undefined)
})

// —— Go + Wails v3 壳:靠 ?shell=desktop 判定,凭据走 gah 的同源代理 ——

test('壳标记:只有 ?shell=desktop 才算(与布局开关同一来源)', () => {
  assert.equal(hasShellQuery('?shell=desktop'), true)
  assert.equal(hasShellQuery('?a=1&shell=desktop&session=x'), true)
  assert.equal(hasShellQuery('?shell=web'), false)
  assert.equal(hasShellQuery(''), false)
  assert.equal(hasShellQuery('?session=1'), false)
})

test('控制面状态:busy/seq 用壳侧真实值(缺省才补 false/0)', () => {
  assert.deepEqual(wailsUpdateSnapshot({ status: 'checking', busy: true, seq: 7 }), {
    busy: true,
    seq: 7,
    status: 'checking',
    message: '',
    version: null,
  })
})

test('壳侧提示:非壳环境不请求(浏览器里不发多余请求)', async () => {
  assert.deepEqual(await fetchShellNotices(), [])
})
