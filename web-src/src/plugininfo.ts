// UI 插件完整性与信任模型的展示逻辑(R10 ⑤-2/⑤-3)。
//
// 后端已经把事实摆在 /api/ui-plugins 上(trusted / trust_note / sha256 / hash_scope /
// hash_note);这里只做**如实转述** —— 不把降级说成完整、不把摘要说成安全边界。
//
// 为什么单独一个自包含模块:它被 node --test 直跑(见 plugininfo.test.ts),
// 不依赖 vue / DOM,也就不需要为测试搭运行时。

/** UIPluginDigest 面向前端的插件精简视图(只取展示需要的字段)。 */
export interface UIPluginDigest {
  id: string
  version?: string
  sha256?: string
  hashScope?: string
  hashNote?: string
  trusted?: boolean
  trustNote?: string
}

/** 短哈希展示长度:够人眼比对前缀,又不撑破设置面板一行。 */
export const SHORT_HASH_LEN = 12

/** UIPluginRaw /api/ui-plugins 原样下发的一条(Go JSON tag 是 snake_case)。 */
export interface UIPluginRaw {
  id?: string
  version?: string
  sha256?: string
  hash_scope?: string
  hash_note?: string
  trusted?: boolean
  trust_note?: string
}

/**
 * toDigest 边界映射:把 API 的 snake_case 形状翻成展示层的字段名。
 * 为什么要显式映射:直接当 camelCase 用会「字段名对不上 → 静默取到 undefined」,
 * 结果是校验值看着有、覆盖范围与降级原因却整段消失(恰是最不能静默的失效)。
 */
export function toDigest(p: UIPluginRaw): UIPluginDigest {
  const out: UIPluginDigest = { id: (p.id || '').trim() }
  if (p.version) out.version = p.version
  if (p.sha256) out.sha256 = p.sha256
  if (p.hash_scope) out.hashScope = p.hash_scope
  if (p.hash_note) out.hashNote = p.hash_note
  if (p.trusted !== undefined) out.trusted = p.trusted
  if (p.trust_note) out.trustNote = p.trust_note
  return out
}

/** shortHash 取展示用短哈希(不足则原样;空 = 无可展示值)。 */
export function shortHash(sha?: string): string {
  const s = (sha || '').trim()
  if (!s) return ''
  return s.length > SHORT_HASH_LEN ? s.slice(0, SHORT_HASH_LEN) : s
}

/** scopeLabel 覆盖范围→人话(未知/空按「完整」处理:后端省略 scope 只在 full 时;未知值如实原文)。 */
export function scopeLabel(scope?: string): string {
  switch ((scope || 'full').trim()) {
    case 'full':
      return ''
    case 'entry':
      return '仅入口 + manifest'
    case 'none':
      return '无可校验产物'
    default:
      return (scope || '').trim()
  }
}

/**
 * digestLine 单行展示文案:插件 id + 短哈希 + 覆盖范围 + 降级原因。
 * 无哈希 → 明说「无法校验」而不是留空(留空会被当成「没问题」)。
 */
export function digestLine(p: UIPluginDigest): string {
  const head = p.version ? `${p.id} ${p.version}` : p.id
  const h = shortHash(p.sha256)
  const scope = scopeLabel(p.hashScope)
  if (!h) return `${head} · 无法校验${p.hashNote ? ':' + p.hashNote : ''}`
  const parts = [`sha256 ${h}`]
  if (scope) parts.push(scope)
  let line = `${head} · ${parts.join(' · ')}`
  if (p.hashNote && scope) line += `(${p.hashNote})`
  return line
}

/** trustNoteOf 取第一条信任模型文案(空数组/无文案 → 空串,面板据此不显示该行)。 */
export function trustNoteOf(list: UIPluginDigest[]): string {
  return list.find((p) => p.trustNote)?.trustNote || ''
}

/** digestRows 需要展示校验值的插件(无 id 的跳过:不是有效插件行)。 */
export function digestRows(list: UIPluginDigest[]): UIPluginDigest[] {
  return list.filter((p) => !!p && typeof p.id === 'string' && p.id.trim() !== '')
}
