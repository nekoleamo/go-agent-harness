// 本窗口绑定的会话(多窗口 / 多会话作用域)。
//
// 形态:桌面壳开「新会话窗口」时 navigate 到 `?session=<id>`,前端启动时读一次,
// 之后所有**读侧**请求(state / 历史分页)与事件流都带这个 id ⇒ 每个窗口看各自的会话。
// 空 = 当前主会话(未带参数的普通浏览器访问,行为与从前完全一致)。
//
// 写侧(input / confirm / command)同样带上 id:后端在「多会话并行回合」落地前会对
// 非当前会话**显式 409**,前端如实显示,不会静默写进另一个会话(见 web/session_scope.go)。
let bound = ''

// 绑定会话 id(空 = 主会话)。initSessionFromURL 之外只有测试会直接调。
export function setBoundSession(id: string): void {
  bound = id || ''
}

// 当前绑定的会话 id('' = 主会话)。
export function boundSession(): string {
  return bound
}

// 从 URL 读 ?session= 并绑定(main.ts 启动时调一次)。
export function initSessionFromURL(): string {
  try {
    bound = new URLSearchParams(window.location.search).get('session') || ''
  } catch {
    bound = ''
  }
  return bound
}

// 会话作用域的 query 串(含前导 ?;未绑定时为空串,便于直接拼 URL)。
export function sessionQS(extra?: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams()
  if (bound) q.set('session', bound)
  for (const [k, v] of Object.entries(extra || {})) {
    if (v !== undefined && v !== '') q.set(k, String(v))
  }
  const s = q.toString()
  return s ? '?' + s : ''
}

// shouldFollowSwitch 「服务端当前会话已切到 id」的显式信号(FrameSessionSwitched)到达时,
// 本窗口该不该跟随。
//
// 三条否定判据分别对应三种「不该动」:
//   ① mainTabKey 为 null —— 本窗口没有「代表服务端当前会话」的页签(带 ?session= 启动的多窗口
//      / 刷新恢复出的具体会话),它钉在某个会话上,不跟随;
//   ② 当前页签不是那个主页签 —— 用户正在看别的会话,不能把**它**改绑走(主页签等切回去时再追);
//   ③ id 为空或已等于本流绑定 —— 空 id(未装配 CwdSessions / 旧事件形态)交给快照兜底,
//      相等则已经在看它了。
export function shouldFollowSwitch(
  id: string,
  mainTabKey: string | null,
  curTabId: string,
  streamSessionId: string,
): boolean {
  if (mainTabKey === null) return false
  if (mainTabKey !== (curTabId || 'main')) return false
  const s = (id || '').trim()
  if (!s) return false
  return s !== streamSessionId
}
