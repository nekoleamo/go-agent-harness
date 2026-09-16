// 壳命令桥的回归:pickDirectory / shellLog 必须走壳自有命令通道。
//
// 起因(2026-09-17 Windows 真机):「＋ 打开」「浏览…」点击后毫无反应,页面上也没有可见报错 ——
// 当时 pickDirectory 走的是 plugin:dialog|open 那条 JS 插件通道(静默失败),而壳自有命令
// 通道在同一台机器上是实测通的(shell_probe → ipc: ok),且 Rust 侧 pick_folder 与真机已验证
// 能弹出的「关于 gah」用的是同一套 dialog 实现。这里把「走哪条通道」钉成回归。
//
// 必须在设置全局之后再导入模块:desktop.ts 在模块加载时就抓一次 IPC 通道(快照)。
import { test } from 'node:test'
import assert from 'node:assert/strict'

const calls: Array<{ cmd: string; args?: Record<string, unknown> }> = []
let pickResult: unknown = 'D:\\work\\proj'

;(globalThis as unknown as { __TAURI_INTERNALS__: unknown }).__TAURI_INTERNALS__ = {
  invoke: (cmd: string, args?: Record<string, unknown>) => {
    calls.push({ cmd, args })
    if (cmd === 'pick_folder') return Promise.resolve(pickResult)
    // shell_log 故意失败:诊断通道坏掉绝不能让页面自己炸
    if (cmd === 'shell_log') return Promise.reject(new Error('壳拒绝了 shell_log'))
    return Promise.resolve(null)
  },
}

const mod = (): Promise<typeof import('./desktop.ts')> => import('./desktop.ts')

test('pickDirectory 走壳自有命令 pick_folder,并把标题原样传下去', async () => {
  calls.length = 0
  pickResult = 'D:\\work\\proj'
  const { pickDirectory, isDesktop } = await mod()
  assert.equal(isDesktop, true)
  assert.equal(await pickDirectory('选择工作区目录'), 'D:\\work\\proj')
  assert.deepEqual(calls, [{ cmd: 'pick_folder', args: { title: '选择工作区目录' } }])
})

test('用户取消(null/空串)与异常形状一律当没选', async () => {
  const { pickDirectory } = await mod()
  pickResult = null
  assert.equal(await pickDirectory(), null)
  pickResult = ''
  assert.equal(await pickDirectory(), null)
  pickResult = ['D:\\a', 'D:\\b'] // 插件形状(多选)不该出现在这条通道上
  assert.equal(await pickDirectory(), null)
})

test('shellLog 把内容写进壳日志,且壳拒绝时静默(不留未处理拒绝)', async () => {
  calls.length = 0
  const { shellLog } = await mod()
  shellLog('切换工作区失败: E:\\nope')
  await new Promise((r) => setTimeout(r, 20))
  assert.deepEqual(calls, [{ cmd: 'shell_log', args: { msg: '切换工作区失败: E:\\nope' } }])
})
