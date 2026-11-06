import { test } from 'node:test'
import assert from 'node:assert/strict'
import { fetchShellNotices, hasShellQuery, requestAttention, sniffTauriInvoke, wailsUpdateSnapshot } from './desktop.ts'

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

// requestAttention 非壳环境恒 false:浏览器端由 notifier.fireEvent 负责,
// 这条护栏保证调用方不必自己分支(也不会往 /shell/* 发无意义的请求)。
test('requestAttention 在浏览器里是 no-op(不抛、不发请求)', async () => {
  assert.equal(await requestAttention('t', 'b'), false)
})

// updateTarget:壳侧快照里「能升级到哪个版本」。
//
// 回归自 2026-11-06 真机反馈:0.8.5 上「检查更新」先显示「升级到 0.8.8-preview」,
// 一秒后翻回「升级到 0.8.5」—— 壳侧把 version 报成了当前版本,面板轮询照单全收。
// 反向验证:把 updateTarget 里的 `t === cur` 判断去掉,第 2 条就会转红。
test('updateTarget:只有 available 且目标 ≠ 当前版本才给按钮', async () => {
  const snap = (status: string, version: string | null) =>
    ({ busy: false, seq: 1, status, message: '', version }) as Parameters<typeof updateTarget>[0]
  const { updateTarget } = await import('./desktop.ts')
  assert.equal(updateTarget(snap('available', '0.8.8-preview'), '0.8.5'), '0.8.8-preview')
  assert.equal(
    updateTarget(snap('available', '0.8.5'), '0.8.5'),
    '',
    '目标 == 当前版本 = 壳报错了字段:不能给「升级到自己」的按钮',
  )
  assert.equal(updateTarget(snap('upToDate', '0.8.8-preview'), '0.8.5'), '', '已最新不提供升级')
  assert.equal(updateTarget(snap('available', ''), '0.8.5'), '', '没有版本号不猜')
  assert.equal(updateTarget(snap('available', ' v0.8.9 '), '0.8.5'), 'v0.8.9', '两端空白要裁掉再判')
})
