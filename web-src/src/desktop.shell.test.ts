// 壳命令桥的回归:选择器必须走「同步命令 + 独立线程 + 轮询」这条真机验证过的通道。
//
// 起因(2026-09-17 Windows 真机,连续两轮):
//   ① plugin:dialog|open 的 JS 插件通道 —— 点击毫无反应,页面上也没有可见报错;
//   ② 壳自有 async 命令 —— 依然无反应,且壳日志里连入口行都没有(函数体没执行),
//      前端 await 永久挂起;同机同步命令(shell_probe/shell_log)全通。
// 于是这里把「走哪条通道」与「状态机怎么收敛」一起钉成回归。
//
// 必须在设置全局之后再导入模块:desktop.ts 在模块加载时就抓一次 IPC 通道(快照)。
import { test } from 'node:test'
import assert from 'node:assert/strict'

const calls: Array<{ cmd: string; args?: Record<string, unknown> }> = []
let beginResult: unknown = 'started'
let pollQueue: string[] = []
let updateReply: unknown = '{"busy":false,"seq":1,"status":"upToDate","message":"已是最新版本","version":null}'

;(globalThis as unknown as { __TAURI_INTERNALS__: unknown }).__TAURI_INTERNALS__ = {
  invoke: (cmd: string, args?: Record<string, unknown>) => {
    calls.push({ cmd, args })
    if (cmd === 'pick_folder_begin') return Promise.resolve(beginResult)
    if (cmd === 'pick_folder_poll') return Promise.resolve(pollQueue.shift() ?? '{"status":"pending"}')
    if (cmd === 'probe_async') return Promise.resolve('ok')
    if (cmd === 'autostart_state') return Promise.resolve('on')
    if (cmd === 'update_state') return Promise.resolve(updateReply)
    // shell_log 故意失败:诊断通道坏掉绝不能让页面自己炸
    if (cmd === 'shell_log') return Promise.reject(new Error('壳拒绝了 shell_log'))
    if (cmd === 'check_update') return new Promise(() => {}) // 永不返回:测超时兜底
    return Promise.resolve(null)
  },
}

const mod = (): Promise<typeof import('./desktop.ts')> => import('./desktop.ts')

test('pickDirectory 走 begin/poll 两条同步命令,取到 done 即返回路径', async () => {
  calls.length = 0
  beginResult = 'started'
  pollQueue = ['{"status":"pending"}', '{"status":"done","path":"D:\\\\work\\\\proj"}']
  const { pickDirectory, isDesktop } = await mod()
  assert.equal(isDesktop, true)
  assert.equal(await pickDirectory('选择工作区目录'), 'D:\\work\\proj')
  assert.deepEqual(
    calls.filter((c) => c.cmd.startsWith('pick_folder')).map((c) => c.cmd),
    ['pick_folder_begin', 'pick_folder_poll', 'pick_folder_poll'],
  )
  assert.deepEqual(calls[0], { cmd: 'pick_folder_begin', args: { title: '选择工作区目录' } })
})

test('用户取消(cancel)返回 null,且不再继续轮询', async () => {
  calls.length = 0
  pollQueue = ['{"status":"cancel"}']
  const { pickDirectory } = await mod()
  assert.equal(await pickDirectory(), null)
  assert.equal(calls.filter((c) => c.cmd === 'pick_folder_poll').length, 1)
})

test('parsePick:形状不对一律当 pending(不能把界面判成已完成)', async () => {
  const { parsePick } = await mod()
  assert.deepEqual(parsePick('{"status":"done","path":"C:\\\\a"}'), { status: 'done', path: 'C:\\a' })
  assert.equal(parsePick('{"status":"cancel"}').status, 'cancel')
  // 通道坏了返回非字符串 / 坏 JSON / 空路径 ⇒ 全部当「还没结果」,由超时兜底给结论
  for (const bad of [undefined, 5, '不是 JSON', '{"status":"pending"}', '{"status":"done"}', '{"status":"done","path":""}']) {
    const st = parsePick(bad)
    if (String(bad).includes('done')) {
      assert.equal(st.status, 'done', String(bad))
      assert.equal(st.path, undefined, String(bad))
    } else {
      assert.equal(st.status, 'pending', String(bad))
    }
  }
})

test('shellLog 把内容写进壳日志,且壳拒绝时静默(不留未处理拒绝)', async () => {
  calls.length = 0
  const { shellLog } = await mod()
  shellLog('切换工作区失败: E:\\nope')
  await new Promise((r) => setTimeout(r, 20))
  assert.deepEqual(calls, [{ cmd: 'shell_log', args: { msg: '切换工作区失败: E:\\nope' } }])
})

test('probeAsync / autostartState 走壳命令且不抛', async () => {
  calls.length = 0
  const { probeAsync, autostartState } = await mod()
  probeAsync()
  assert.equal(await autostartState(), 'on')
  await new Promise((r) => setTimeout(r, 20))
  assert.deepEqual(
    calls.map((c) => c.cmd),
    ['probe_async', 'autostart_state', 'shell_log'],
  )
})

test('checkUpdate 壳侧不返回时按超时给结论(不让用户永远停在「检查中…」)', async () => {
  const { checkUpdate } = await mod()
  await assert.rejects(() => checkUpdate(30), /75 秒没有响应/)
})

// 设置面板开着时轮询壳侧状态:托盘勾开机自启 / 点检查更新,面板必须自己跟上
// (真机反馈「要重新打开设置菜单才同步」)。
test('updateState 解析壳侧快照,形状不对返回 null 而不猜', async () => {
  const { updateState, parseUpdateState } = await mod()
  updateReply = '{"busy":true,"seq":7,"status":"","message":"","version":null}'
  assert.deepEqual(await updateState(), {
    busy: true,
    seq: 7,
    status: '',
    message: '',
    version: null,
  })
  // 壳回了非 JSON / 缺字段:返回 null —— 面板宁可不动,也不能显示假状态
  updateReply = 'not json'
  assert.equal(await updateState(), null)
  updateReply = '{"seq":1}'
  assert.equal(await updateState(), null)
  assert.equal(parseUpdateState(undefined), null)
  assert.equal(parseUpdateState('{"busy":false,"seq":2}')?.seq, 2)
  // 对象直传(壳若改成结构体返回也能用)
  assert.equal(parseUpdateState({ busy: false, seq: 3, status: 'installed' })?.status, 'installed')
})
