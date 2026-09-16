// 桌面壳(gah-desktop)桥接:只有壳内嵌的页面才有这些能力,浏览器直连 127.0.0.1 时为 undefined。
//
// 为什么不用 window.__TAURI__:那是 withGlobalTauri 注入的全局(整套 Tauri API 加各插件的 JS),
// 2026-09-15 真机白屏排查后刻意关掉了它。壳的 init 脚本总会注入 __TAURI_INTERNALS__(IPC 通道
// 本体),这里只需要它;两种都认,是为了兼容将来万一重新打开该开关的构建。

export type TauriInvoke = (cmd: string, args?: Record<string, unknown>) => Promise<unknown>

// sniffTauriInvoke 从宿主全局里取出 IPC 通道。
// 故意做成纯函数并配单测:这条判定决定了「关于 gah」「浏览…」等壳专属入口显不显示 ——
// 它一旦判错,用户看到的就是「桌面版里没有桌面功能」,而这种现象在页面上无从自证。
export function sniffTauriInvoke(g: unknown): TauriInvoke | undefined {
  const o = g as
    | {
        __TAURI_INTERNALS__?: { invoke?: unknown }
        __TAURI__?: { core?: { invoke?: unknown } }
      }
    | null
    | undefined
  const inv = o?.__TAURI_INTERNALS__?.invoke ?? o?.__TAURI__?.core?.invoke
  return typeof inv === 'function' ? (inv as TauriInvoke) : undefined
}

export const tauriInvoke = sniffTauriInvoke(globalThis)
export const isDesktop = typeof tauriInvoke === 'function'

// 选择器轮询节奏与总上限(用户可能在原生框里翻很久,5 分钟足够;超时必须给结论,不能空等)。
const PICK_POLL_MS = 300
const PICK_TIMEOUT_MS = 300_000
// 检查更新的前端兜底:壳侧另有 75 秒看门狗,这里保证界面一定能拿到结果。
const CHECK_TIMEOUT_MS = 75_000

// parsePick 解析 pick_folder_poll 的返回(JSON 文本)。
// 做成纯函数 + 单测:形状判错在页面上只表现为「点了没反应」,自证成本极高。
export function parsePick(raw: unknown): { status: string; path?: string } {
  if (typeof raw !== 'string') return { status: 'pending' }
  try {
    const o = JSON.parse(raw) as { status?: unknown; path?: unknown }
    const status = typeof o?.status === 'string' ? o.status : 'pending'
    const path = typeof o?.path === 'string' && o.path.length > 0 ? o.path : undefined
    return { status, path }
  } catch {
    return { status: 'pending' }
  }
}

// pickDirectory 调系统文件夹选择器(桌面壳专属;用户取消或非壳环境返回 null)。
//
// 为什么是「同步命令 + 轮询」而不是一条 await 的 async 命令(2026-09-17 真机两轮实测):
//   ① plugin:dialog|open 的 JS 插件通道 —— 点击毫无反应,页面上也无可见报错;
//   ② 壳自有 async 命令 —— 依旧无反应,且壳日志里连入口行都没有(函数体没执行),
//      前端 await 永久挂起;同机同步命令(shell_probe/shell_log)全通。
// 于是只依赖已被真机验证的通道:begin 起线程弹框,poll 取结果。全程写壳日志,
// 「到底走到哪一步」不再靠猜。
export async function pickDirectory(title = '选择工作区目录'): Promise<string | null> {
  if (!tauriInvoke) return null
  const started = await tauriInvoke('pick_folder_begin', { title })
  shellLog('选择器 begin → ' + String(started))
  const t0 = Date.now()
  for (;;) {
    await new Promise((r) => setTimeout(r, PICK_POLL_MS))
    const st = parsePick(await tauriInvoke('pick_folder_poll'))
    if (st.status === 'done') {
      shellLog('选择器 poll → done: ' + String(st.path))
      return st.path ?? null
    }
    if (st.status === 'cancel') {
      shellLog('选择器 poll → 用户取消')
      return null
    }
    if (Date.now() - t0 > PICK_TIMEOUT_MS) {
      shellLog('选择器 poll → 超时(5 分钟无结果)')
      return null
    }
  }
}

// shellLog 把页面侧诊断写进壳日志(gah-shell.log,与壳侧/sidecar stderr 同一份文件)。
// 桌面版没有终端:失败必须自证,否则真机上只剩「点了没反应」。非壳环境静默丢弃。
export function shellLog(msg: string): void {
  if (!tauriInvoke) return
  void Promise.resolve(tauriInvoke('shell_log', { msg })).catch(() => {})
}

// probeAsync 异步命令通道探针:启动自检时调一次,只写日志不返回给界面。
// 真机上出现过「async 命令连函数体都没进」,这条日志是区分「任务没被调度」与
// 「请求没到处理函数」的唯一证据。
export function probeAsync(): void {
  if (!tauriInvoke) return
  void Promise.resolve(tauriInvoke('probe_async'))
    .then((r) => shellLog('probe_async 返回: ' + String(r)))
    .catch((e) => shellLog('probe_async 失败: ' + (e as Error).message))
}

// autostartState 查询开机自启的实际状态(设置面板「关于 gah」展示;失败返回空串=未知)。
export async function autostartState(): Promise<'on' | 'off' | ''> {
  if (!tauriInvoke) return ''
  try {
    return (await tauriInvoke('autostart_state')) === 'on' ? 'on' : 'off'
  } catch {
    return ''
  }
}

// checkUpdate 壳侧检查更新(与托盘菜单同一实现);有更新时壳自己下载安装并重启。
// 前端也加超时:真机上出现过壳侧异步任务不返回、界面永远停在「检查中…」——
// 宁可给出「没有结果」的结论,也不让用户空等。
export async function checkUpdate(timeoutMs = CHECK_TIMEOUT_MS): Promise<{ status?: string; message?: string }> {
  if (!tauriInvoke) throw new Error('当前环境不支持检查更新(需桌面版)')
  let timer: ReturnType<typeof setTimeout> | undefined
  const timeout = new Promise<never>((_, rej) => {
    timer = setTimeout(
      () => rej(new Error('检查更新 75 秒没有响应:壳的异步任务没有返回。请把壳日志发给开发者。')),
      timeoutMs,
    )
  })
  try {
    return (await Promise.race([
      tauriInvoke('check_update') as Promise<{ status?: string; message?: string }>,
      timeout,
    ])) as { status?: string; message?: string }
  } finally {
    if (timer) clearTimeout(timer)
  }
}
