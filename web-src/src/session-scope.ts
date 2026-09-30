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
