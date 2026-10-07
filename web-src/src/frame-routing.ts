// 非会话帧的归属判定(纯逻辑,可单测;理由同 session-parallel.ts:
// api.ts / transport.ts 被 Node 以 .ts 直载,视图逻辑得住自己的模块)。
//
// 问题是什么:一次请求之后推来的不只是会话事件,还有「这个操作要你点头」的审批弹层、
// 结构化提问、斜杠命令输出。多会话并行时(桌面壳多窗口已是多会话,页签更是),
// 这些帧若不看归属,就会落在**别的**会话视图上 —— 用户能在那儿按"同意",
// 替另一个会话做了决定。服务端已在帧上带 Session(见 sdk.WithSessionContext),
// 这里只回答一个问题:这帧归不归本会话。

/** 帧的归属会话 id。空 = 主会话 ⇒ 映射成本会话自己的 id(与服务端同口径)。 */
export function frameOwner(f: { session?: string }, curId: string): string {
  return (f && f.session) || curId || ''
}

/** 这帧是否属于当前视图的会话。 */
export function ownedHere(f: { session?: string }, curId: string): boolean {
  return frameOwner(f, curId) === (curId || '')
}

/**
 * 这帧是不是**别的**会话的?返回那个会话 id;不是则返回空串。
 *
 * 与 ownedHere 的关键区别:**拿不准就当自己的**(两边任一为空 ⇒ 返回空串)。
 * 审批弹层被误拒的代价是回合永远阻塞(审批默认不限时地等)—— 那比多弹一次严重得多;
 * 所以只有「两边都拿到、且明确不等」才判为外来帧。
 */
export function foreignOwner(f: { session?: string }, curId: string): string {
  const owner = (f && f.session) || ''
  const cur = curId || ''
  if (!owner || !cur || owner === cur) return ''
  return owner
}

/**
 * 会话 id 的短形(用于文案):20260107-143022 → 143022。
 *
 * id 形如 `<日期>-<时间>[-<序号>]`(同分钟冲突时 host-cwd-sessions 追加序号),
 * 序号段对用户没有识别价值,所以要先把它剔掉 —— 否则 -2 会被当成时间。
 */
export function shortSessionId(id: string): string {
  const s = (id || '').trim()
  if (!s) return ''
  const parts = s.split('-')
  const last = parts[parts.length - 1]
  const isSeq = parts.length >= 3 && /^\d{1,2}$/.test(last)
  const seg = isSeq ? parts[parts.length - 2] : last
  return seg.slice(-6)
}

/**
 * 页签标题:有**像人写的**名字就用它,否则用会话 id 的短形。
 *
 * 为何要判"像人写的":服务端有时把名字回填成会话 id 本身(概述生成/未命名),
 * 直接显示就是 20261007-200600 —— 对用户零识别价值,不如短形 200600。
 */
export function tabTitle(name: string, id: string): string {
  const key = id || 'main'
  const n = (name || '').trim()
  if (key === 'main') return n || '主会话'
  if (!n || n === key || n.indexOf(key) >= 0) return shortSessionId(key) || key
  return n
}

/**
 * 别的会话有待办时的会话流提示文案。
 *
 * 为何是提示而不是弹层:那一步的决定会作用在**另一个**会话上,在本会话弹出来等于
 * 骗用户替他决定。也不能静默丢 —— 审批默认不限时地等,没人知道就永远挂着。
 */
export function foreignTodoText(kind: 'confirm' | 'question' | 'command', ownerId: string, prompt: string): string {
  const who = shortSessionId(ownerId) ? `会话 ${shortSessionId(ownerId)}` : '另一个会话'
  const what = kind === 'confirm' ? '有待确认的操作' : kind === 'question' ? '有待你作答的问题' : '有命令输出'
  const body = (prompt || '').trim().slice(0, 80)
  return body ? `${who}${what}:${body}` : `${who}${what}`
}