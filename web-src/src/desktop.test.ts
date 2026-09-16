import { test } from 'node:test'
import assert from 'node:assert/strict'
import { sniffTauriInvoke } from './desktop.ts'

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
