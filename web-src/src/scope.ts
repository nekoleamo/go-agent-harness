// 作用域判据(第一百三十四批 · 设置作用域收敛)。
//
// 单一事实源:**只看 `state.session_prefs`**,不猜任何 `*_from` 字段。
//
// 为什么必须单独立一个判据(实测踩出来的坑):`model_from` / `thinking_from` 走
// sdk.EffectiveModel / EffectiveThinking,那两个函数**只要没有角色声明就返回
// "session"** —— 它们答的是「生效值是不是角色给的」,不是「这个会话有没有自己压过全局」。
// 拿 `model_from === 'session'` 当「独立」的判据,结果**每个会话都会被判成独立**
// (跟随全局的也算),页签条于是挂满方块。
//
// `sandbox_from` / `approval_from` 倒是真信号(由后端 resolved.FromSessionOf 填),但那条
// 通道被「偏离归因」占着(角色收紧 / 审批联动都走它),语义不干净。四项口径必须一致,
// 所以后端单开了 `session_prefs` 这张表 —— 「这一项跟随全局吗」只有它答得准。

type Field = 'model' | 'thinking' | 'sandbox' | 'approval'

const SESSION_PREF_FIELDS: readonly Field[] = ['model', 'thinking', 'sandbox', 'approval']

/** 最小状态形状:判据只需要 session_prefs 一处,不必依赖整个 StateView。 */
export interface PrefScope {
  session_prefs?: Record<string, boolean>
}

/** 该项是否**独立于全局**(本会话自己设过)。旧后端不下发该字段时一律 false = 跟随全局。 */
export function isSessionSet(s: PrefScope, field: Field): boolean {
  return s.session_prefs?.[field] === true
}

/** 本会话有几项独立于全局。 */
export function sessionSetCount(s: PrefScope): number {
  return SESSION_PREF_FIELDS.filter((f) => isSessionSet(s, f)).length
}

/** 是否有任一项独立于全局(页签方块标记 / 状态栏「本会话」前缀共用这条)。 */
export function hasSessionOverride(s: PrefScope): boolean {
  return sessionSetCount(s) > 0
}