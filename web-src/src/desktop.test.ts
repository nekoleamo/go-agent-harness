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

// —— Go + Wails v3 壳:控制面凭据从 fragment 解析 ——
import { parseShellCtl, stripShellCreds, wailsUpdateSnapshot } from './desktop.ts'

test('控制面凭据:从 fragment 取端口与 token', () => {
  assert.deepEqual(parseShellCtl('#shellport=61720&shelltoken=' + 'a'.repeat(32)), {
    base: 'http://127.0.0.1:61720',
    token: 'a'.repeat(32),
  })
  // 与 gah 自己的 token 共存(gah token 必须最后一个,但它在本函数里无关)
  assert.deepEqual(parseShellCtl('#shellport=1&shelltoken=0123456789&token=xyz'), {
    base: 'http://127.0.0.1:1',
    token: '0123456789',
  })
})

test('控制面凭据:残缺/伪造一律返回 null(否则壳功能会半可用)', () => {
  assert.equal(parseShellCtl(''), null)
  assert.equal(parseShellCtl('#token=abc'), null)
  assert.equal(parseShellCtl('#shellport=99999&shelltoken=0123456789'), null)
  assert.equal(parseShellCtl('#shellport=1234&shelltoken=short'), null)
  assert.equal(parseShellCtl('#shellport=ab&shelltoken=0123456789'), null)
})

test('抹凭据:只删控制面两个参数,保留其余 fragment', () => {
  assert.equal(stripShellCreds('#shellport=1&shelltoken=0123456789&token=xyz'), '#token=xyz')
  assert.equal(stripShellCreds('#shellport=1&shelltoken=0123456789'), '')
  assert.equal(stripShellCreds(''), '')
})

test('控制面精简状态补成面板形状(busy/seq 给默认值,不猜状态)', () => {
  assert.deepEqual(wailsUpdateSnapshot({ status: 'idle', version: '0.7.4' }), {
    busy: false,
    seq: 0,
    status: 'idle',
    message: '',
    version: '0.7.4',
  })
  assert.equal(wailsUpdateSnapshot(null), null)
  assert.equal(wailsUpdateSnapshot('x'), null)
})
