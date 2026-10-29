// 桌面壳桥接:只有壳内嵌的页面才有这些能力,浏览器直连 127.0.0.1 时为 undefined。
//
// **两代壳并存**(迁移期,见 docs/DESKTOP_FEASIBILITY.md §11):
//  - Tauri v2(Rust 壳):`window.__TAURI_INTERNALS__.invoke` 走 IPC;
//  - Go + Wails v3 壳:页面由 gah 自己提供(外部 URL),v3.0 拿不到 bindings
//    (上游 wails#4473 未进 3.0),所以壳侧能力改由**壳自建的本地控制面 HTTP API** 提供:
//    端口与一次性 token 由壳经 URL fragment 传入(`#shellport=<p>&shelltoken=<t>`)。
//    这个文件只做两件事:① 从 fragment 取凭据并立刻从地址栏抹掉;② 把「命令名」翻译成
//    控制面的 HTTP 调用。上游页面代码看到的仍是同一组异步函数。
//
// 为什么不用 window.__TAURI__:那是 withGlobalTauri 注入的全局(整套 Tauri API 加各插件的
// JS),2026-09-15 真机白屏排查后刻意关掉了它。壳的 init 脚本总会注入 __TAURI_INTERNALS__
// (IPC 通道本体),这里只需要它;两种都认,是为了兼容将来万一重新打开该开关的构建。

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

// ShellCtl 控制面凭据(Go 壳经 fragment 下发)。
export interface ShellCtl {
  base: string
  token: string
}

// parseShellCtl 从 URL fragment 解析控制面凭据。纯函数 + 单测:
// 解析失败的表现是「桌面版里所有壳功能点了没反应」,页面上无从自证。
export function parseShellCtl(hash: string): ShellCtl | null {
  if (!hash) return null
  const q = new URLSearchParams(hash.replace(/^#/, ''))
  const port = q.get('shellport') ?? ''
  const token = q.get('shelltoken') ?? ''
  // 端口必须是**有效**的 TCP 端口(1..65535):99999 这种看着像端口的值不能放行,
  // 否则壳功能会「半可用」——地址拿到了、请求全失败,而报错只有一行 fetch 失败。
  if (!/^\d{1,5}$/.test(port)) return null
  const n = Number(port)
  if (!Number.isInteger(n) || n < 1 || n > 65535) return null
  if (token.length < 8) return null
  return { base: `http://127.0.0.1:${n}`, token }
}

// stripShellCreds 把控制面凭据从 fragment 去掉(保留其余参数,例如 gah 自己的 token)。
// 凭据留在地址栏会进截图/录屏/复制分享,而这层凭据能弹原生框、能写文件。
export function stripShellCreds(hash: string): string {
  if (!hash) return ''
  const q = new URLSearchParams(hash.replace(/^#/, ''))
  q.delete('shellport')
  q.delete('shelltoken')
  const rest = q.toString()
  return rest ? '#' + rest : ''
}

export const tauriInvoke = sniffTauriInvoke(globalThis)

// ctl 控制面凭据(仅 Go 壳内有);浏览器与 Tauri 壳都是 null。
export const shellCtl: ShellCtl | null =
  typeof location === 'undefined' ? null : parseShellCtl(location.hash)

/** 是否是 Go + Wails v3 壳(壳侧能力走本地控制面 HTTP)。 */
export const isWailsShell = shellCtl !== null
export const isDesktop = typeof tauriInvoke === 'function' || isWailsShell

// 启动即抹掉地址栏里的控制面凭据(取不到就不动)。
if (shellCtl && typeof history !== 'undefined') {
  try {
    const next = location.pathname + location.search + stripShellCreds(location.hash)
    history.replaceState(null, '', next)
  } catch {
    // 无关紧要:抹不掉也不影响功能,只是地址栏里多留一会儿凭据。
  }
}

// ctlFetch 调一次控制面接口(token 走头,不进 URL;跨源由壳侧按 Origin 白名单放行)。
async function ctlFetch(path: string, init?: { method?: string; body?: unknown }): Promise<unknown> {
  if (!shellCtl) throw new Error('不是桌面壳环境')
  const resp = await fetch(shellCtl.base + path, {
    method: init?.method ?? 'GET',
    headers: {
      'X-Shell-Token': shellCtl.token,
      ...(init?.body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: init?.body === undefined ? undefined : JSON.stringify(init.body),
  })
  let body: unknown = undefined
  try {
    body = await resp.json()
  } catch {
    body = undefined
  }
  if (!resp.ok) {
    const msg =
      body && typeof body === 'object' && typeof (body as { error?: unknown }).error === 'string'
        ? String((body as { error?: string }).error)
        : `HTTP ${resp.status}`
    throw new Error(msg)
  }
  return body
}

// 选择器轮询节奏与总上限(用户可能在原生框里翻很久,5 分钟足够;超时必须给结论,不能空等)。
const PICK_POLL_MS = 300
const PICK_TIMEOUT_MS = 300_000
// 检查更新的前端兜底:壳侧另有 75 秒看门狗,这里保证界面一定能拿到结果。
const CHECK_TIMEOUT_MS = 75_000
// INSTALL_TIMEOUT_MS 下载安装的前端兜底:远宽于检查(包 30+ MB,慢网要几分钟),
// 拿检查的 75 秒套上去会把正常下载判成超时。
const INSTALL_TIMEOUT_MS = 30 * 60_000

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
// Tauri 壳为什么是「同步命令 + 轮询」而不是一条 await 的 async 命令(2026-09-17 真机两轮实测):
//   ① plugin:dialog|open 的 JS 插件通道 —— 点击毫无反应,页面上也无可见报错;
//   ② 壳自有 async 命令 —— 依旧无反应,且壳日志里连入口行都没有(函数体没执行),
//      前端 await 永久挂起;同机同步命令(shell_probe/shell_log)全通。
// Go 壳没有那套 IPC 语义,一次请求阻塞到用户选定即可 —— 所以这里按壳类型分两条路。
export async function pickDirectory(title = '选择工作区目录'): Promise<string | null> {
  if (isWailsShell) {
    try {
      const r = (await ctlFetch('/shell/pick-folder', { method: 'POST', body: { title } })) as {
        path?: string
        cancelled?: boolean
      }
      shellLog('选目录(控制面)→ ' + (r?.cancelled ? '用户取消' : String(r?.path ?? '')))
      return r?.path ?? null
    } catch (e) {
      shellLog('选目录(控制面)失败: ' + (e as Error).message)
      return null
    }
  }
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
  if (isWailsShell) {
    void ctlFetch('/shell/log', { method: 'POST', body: { line: msg } }).catch(() => {})
    return
  }
  if (!tauriInvoke) return
  void Promise.resolve(tauriInvoke('shell_log', { msg })).catch(() => {})
}

// probeAsync 异步命令通道探针:启动自检时调一次,只写日志不返回给界面。
// 真机上出现过「async 命令连函数体都没进」,这条日志是区分「任务没被调度」与
// 「请求没到处理函数」的唯一证据。Go 壳没有这条通道,改为打一次控制面 ping。
export function probeAsync(): void {
  if (isWailsShell) {
    void ctlFetch('/shell/ping')
      .then((r) => shellLog('控制面 ping 返回: ' + JSON.stringify(r)))
      .catch((e) => shellLog('控制面 ping 失败: ' + (e as Error).message))
    return
  }
  if (!tauriInvoke) return
  void Promise.resolve(tauriInvoke('probe_async'))
    .then((r) => shellLog('probe_async 返回: ' + String(r)))
    .catch((e) => shellLog('probe_async 失败: ' + (e as Error).message))
}

// autostartState 查询开机自启的实际状态(设置面板「关于 gah」展示;失败返回空串=未知)。
export async function autostartState(): Promise<'on' | 'off' | ''> {
  if (isWailsShell) {
    try {
      const r = (await ctlFetch('/shell/autostart')) as { enabled?: boolean }
      return r?.enabled ? 'on' : 'off'
    } catch {
      return ''
    }
  }
  if (!tauriInvoke) return ''
  try {
    return (await tauriInvoke('autostart_state')) === 'on' ? 'on' : 'off'
  } catch {
    return ''
  }
}

// UpdateSnapshot 壳侧检查更新的当前状态。托盘与设置面板是两个视图,状态只有一个源。
export interface UpdateSnapshot {
  busy: boolean
  seq: number
  status: string
  message: string
  version: string | null
}

// parseUpdateState 解析壳侧 `update_state` 的 JSON(纯函数,配单测)。
// 形状不对时返回 null 而不是猜:猜错了会让面板显示假状态。
export function parseUpdateState(raw: unknown): UpdateSnapshot | null {
  let v: unknown = raw
  if (typeof raw === 'string') {
    try {
      v = JSON.parse(raw)
    } catch {
      return null
    }
  }
  if (!v || typeof v !== 'object') return null
  const o = v as Record<string, unknown>
  if (typeof o.busy !== 'boolean' || typeof o.seq !== 'number') return null
  return {
    busy: o.busy,
    seq: o.seq,
    status: typeof o.status === 'string' ? o.status : '',
    message: typeof o.message === 'string' ? o.message : '',
    version: typeof o.version === 'string' ? o.version : null,
  }
}

// wailsUpdateSnapshot 把控制面的精简状态({status,version})补成面板要的完整形状。
// 控制面还没有 busy/seq(更新器未迁移),补默认值而不是让面板直接报「形状不对」。
export function wailsUpdateSnapshot(raw: unknown): UpdateSnapshot | null {
  if (!raw || typeof raw !== 'object') return null
  const o = raw as Record<string, unknown>
  return {
    busy: false,
    seq: 0,
    status: typeof o.status === 'string' ? o.status : '',
    message: '',
    version: typeof o.version === 'string' ? o.version : null,
  }
}

// updateState 读一次壳侧更新状态(设置面板开着时轮询;失败返回 null)。
export async function updateState(): Promise<UpdateSnapshot | null> {
  if (isWailsShell) {
    try {
      return wailsUpdateSnapshot(await ctlFetch('/shell/update-state'))
    } catch {
      return null
    }
  }
  if (!tauriInvoke) return null
  try {
    return parseUpdateState(await tauriInvoke('update_state'))
  } catch {
    return null
  }
}

// saveExport 导出落盘(壳侧 save_export):把导出内容写进用户选定的文件,返回绝对路径。
// 为什么不让 WebView 自己下载:壳的窗口没注册下载委托 ⇒ `<a download>` 点了什么都不会发生
// (桌面端导出静默失效的根因)。
// 失败回空串(调用方据此报错)。
// shortcut: Go 壳暂未实现「写完后在文件管理器里高亮」那一步(旧壳的 open 参数),
// 迁移完成前桌面端导出只落盘、不弹目录。
export async function saveExport(name: string, text: string, open: boolean): Promise<string> {
  if (isWailsShell) {
    try {
      const r = (await ctlFetch('/shell/export-save', {
        method: 'POST',
        body: { filename: name, content: text },
      })) as { path?: string }
      if (open) shellLog('导出: 已写入(本版本不自动打开所在目录)')
      return typeof r?.path === 'string' ? r.path : ''
    } catch (e) {
      shellLog('导出(控制面)失败: ' + (e as Error).message)
      return ''
    }
  }
  if (!tauriInvoke) return ''
  try {
    const raw = await tauriInvoke('save_export', { name, text, open })
    return typeof raw === 'string' ? raw : ''
  } catch {
    return ''
  }
}

/** 壳侧升级结果。status:available=有更新待确认 / installed=已装待重启 / upToDate / failed / portable。 */
export interface UpdateResult {
  status?: string
  message?: string
  version?: string | null
}

// checkUpdate 壳侧检查更新(与托盘菜单同一实现)。
//
// **只查不装**(2026-10-03):有更新时返回 available,由用户确认后走 installUpdate ——
// 一次「检查更新」不该顺手把整个应用换掉。
// 前端也加超时:真机上出现过壳侧异步任务不返回、界面永远停在「检查中…」——
// 宁可给出「没有结果」的结论,也不让用户空等。
export async function checkUpdate(timeoutMs = CHECK_TIMEOUT_MS): Promise<UpdateResult> {
  if (isWailsShell) {
    // 更新器还没迁移到 Go 壳:控制面明确回 501 + 原因,这里原样抛给界面。
    const r = (await raceWails('/shell/update-check', timeoutMs)) as UpdateResult
    return r
  }
  if (!tauriInvoke) throw new Error('当前环境不支持检查更新(需桌面版)')
  return raceUpdate('check_update', timeoutMs, '检查更新 75 秒没有响应:壳的异步任务没有返回。请把壳日志发给开发者。')
}

// installUpdate 确认升级:下载并安装上一次 checkUpdate 找到的更新,完成后壳自动重启。
// 超时给得比检查宽得多:下载 30+ MB 在慢网络下要几分钟,拿检查的 75 秒套上去会假红。
export async function installUpdate(timeoutMs = INSTALL_TIMEOUT_MS): Promise<UpdateResult> {
  if (isWailsShell) {
    throw new Error('Go 壳的更新器尚未接入(见 docs/DESKTOP_FEASIBILITY §11.7),请到项目 Release 页下载')
  }
  if (!tauriInvoke) throw new Error('当前环境不支持升级(需桌面版)')
  return raceUpdate(
    'install_update',
    timeoutMs,
    '下载安装 30 分钟没有响应:壳的异步任务没有返回。请把壳日志发给开发者。',
  )
}

// raceWails 控制面调用的超时包装(与 raceUpdate 同口径:永不让界面停在「进行中」)。
async function raceWails(path: string, timeoutMs: number): Promise<unknown> {
  let timer: ReturnType<typeof setTimeout> | undefined
  const timeout = new Promise<never>((_, rej) => {
    timer = setTimeout(() => rej(new Error('壳侧 ' + timeoutMs + 'ms 没有响应')), timeoutMs)
  })
  try {
    return await Promise.race([ctlFetch(path, { method: 'POST' }), timeout])
  } finally {
    if (timer) clearTimeout(timer)
  }
}

// raceUpdate 两条升级命令共用的超时包装(纯函数式抽出,免得两处各写一份计时器)。
async function raceUpdate(cmd: string, timeoutMs: number, timeoutMsg: string): Promise<UpdateResult> {
  // 两条调用方都先判过 isDesktop;这里再取一次是为了让 TS 收窄(tauriInvoke 是可选值)。
  const invoke = tauriInvoke
  if (!invoke) throw new Error('当前环境不支持升级(需桌面版)')
  let timer: ReturnType<typeof setTimeout> | undefined
  const timeout = new Promise<never>((_, rej) => {
    timer = setTimeout(() => rej(new Error(timeoutMsg)), timeoutMs)
  })
  try {
    return (await Promise.race([
      invoke(cmd) as Promise<UpdateResult>,
      timeout,
    ])) as UpdateResult
  } finally {
    if (timer) clearTimeout(timer)
  }
}
