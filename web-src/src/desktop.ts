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

// pickDirectory 调系统文件夹选择器(桌面壳专属;用户取消或非壳环境返回 null)。
//
// 走壳自有命令 pick_folder,不走 plugin:dialog|open:真机(2026-09-17 Windows)测到 JS 插件
// 通道点击后毫无反应且页面上无错,而自有命令通道在同机实测是通的(shell_probe → ipc: ok);
// 且 Rust 侧 pick_folder 与真机已验证能弹出的「关于 gah」用的是同一个 dialog 实现。
export async function pickDirectory(title = '选择工作区目录'): Promise<string | null> {
  if (!tauriInvoke) return null
  const r = await tauriInvoke('pick_folder', { title })
  return typeof r === 'string' && r.length > 0 ? r : null
}

// shellLog 把页面侧诊断写进壳日志(gah-shell.log,与壳侧/sidecar stderr 同一份文件)。
// 桌面版没有终端:失败必须自证,否则真机上只剩「点了没反应」。非壳环境静默丢弃。
export function shellLog(msg: string): void {
  if (!tauriInvoke) return
  void Promise.resolve(tauriInvoke('shell_log', { msg })).catch(() => {})
}

// checkUpdate 壳侧检查更新(与托盘菜单同一实现);有更新时壳自己下载安装并重启。
export async function checkUpdate(): Promise<{ status?: string; message?: string }> {
  if (!tauriInvoke) throw new Error('当前环境不支持检查更新(需桌面版)')
  return (await tauriInvoke('check_update')) as { status?: string; message?: string }
}
