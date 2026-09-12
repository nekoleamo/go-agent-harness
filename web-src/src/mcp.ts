// MCP server 配置 UI 纯逻辑(NOND-M1 第 2/3 步)。
//
// 设计口径:前端不做命令解析 —— 用户按「一行启动命令」填(与在终端里输入一致),
// 拆参数由后端用同一套引号解析(sdk.SplitArgs),避免两套语义漂移;
// 模式选择的影响面(全量注册 vs 按需检索)在文案里说清代价与限制。

import type { McpServer, McpView } from './types'

// McpDraft 编辑态的一项(保存时整体提交给后端)。
export interface McpDraft {
  name: string
  command: string
  enabled: boolean
  mode: string
}

// MCP_MODE_DIRECT 全量注册:工具直接进模型工具表(每轮都占固定前缀)。
// MCP_MODE_SEARCH 按需检索:只给模型 mcp_search / mcp_call,工具清单用到才付代价。
export const MCP_MODE_DIRECT = 'direct'
export const MCP_MODE_SEARCH = 'search'

// canEdit 只有配置文件里的条目可编辑(env 条目来自 GAH_MCP_COMMAND(S),改不了)。
export function canEdit(s: McpServer): boolean {
  return s.source !== 'env'
}

// draftFrom 视图项 → 编辑态(把 args 拼回命令行)。
export function draftFrom(s: McpServer): McpDraft {
  return { name: s.name, command: fmtCommand(s), enabled: s.enabled, mode: s.mode || MCP_MODE_DIRECT }
}

// fmtCommand 命令行回显:命令 + 参数按空格拼接(参数含空格时加引号)。
export function fmtCommand(s: { command: string; args?: string[] }): string {
  const parts = [s.command, ...(s.args ?? [])].filter((p) => p !== '')
  return parts.map(quoteIfNeeded).join(' ')
}

// normalizeCommand 提交前规范化空白(单一空格分隔,去掉首尾)。
export function normalizeCommand(cmd: string): string {
  return cmd.trim().split(/\s+/).filter((s) => s.length > 0).join(' ')
}

// draftKey 草稿指纹(脏检测 + 变更比对;字段顺序固定,便于 JSON 比较)。
export function draftKey(rows: McpDraft[]): string {
  return JSON.stringify(
    rows.map((r) => ({
      name: r.name.trim(),
      command: normalizeCommand(r.command),
      enabled: r.enabled,
      mode: r.mode || MCP_MODE_DIRECT,
    })),
  )
}

// validateDraft 逐项校验(返回空串 = 通过);名称重复也在这里拦(后端也会拒)。
export function validateDraft(rows: McpDraft[]): string {
  const seen = new Set<string>()
  for (let i = 0; i < rows.length; i++) {
    const r = rows[i]
    const no = i + 1
    const name = r.name.trim()
    if (name !== '' && !/^[A-Za-z0-9_-]+$/.test(name)) {
      return `第 ${no} 项名称「${name}」只能用字母、数字、_ 和 -`
    }
    if (seen.has(name)) return `第 ${no} 项名称「${name || '(留空)'}」重复(名称决定工具前缀,不能重名)`
    seen.add(name)
    if (normalizeCommand(r.command) === '') return `第 ${no} 项缺少启动命令(如 npx -y @modelcontextprotocol/server-filesystem /tmp)`
    if (r.mode !== MCP_MODE_DIRECT && r.mode !== MCP_MODE_SEARCH) {
      return `第 ${no} 项模式非法:${r.mode}`
    }
  }
  return ''
}

// serverStateLabel 运行期状态文案(是否真的连上并注册了工具)。
export function serverStateLabel(s: { enabled: boolean; loaded: boolean; tools: number }): string {
  if (!s.enabled) return '已停用'
  if (!s.loaded) return '未连接或元工具未注册'
  return `${s.tools} 个工具可用`
}

// modeLabel 模式中文名。
export function modeLabel(mode?: string): string {
  return mode === MCP_MODE_SEARCH ? '按需检索' : '全量注册'
}

// modeHint 模式选择的影响面(帮非程序员判断该选哪个)。
export function modeHint(mode?: string): string {
  if (mode === MCP_MODE_SEARCH) {
    return '只给模型两个工具(mcp_search 查、mcp_call 调),省固定前缀;按工具名设的审批规则照样生效(会作用到真实工具)'
  }
  return '全部工具直接进模型工具表,模型一上手就能用;代价:工具多时每轮都占上下文'
}

// sourceLabel 配置来源徽标。
export function sourceLabel(source?: string): string {
  if (source === 'env') return '环境变量'
  if (source === 'file') return '配置文件'
  return ''
}

// viewNotices 顶部提示集(插件未加载 / 重载失败 / 配置解析告警 / 无条目)。
export function viewNotices(v: McpView): string[] {
  const out: string[] = []
  if (v.reload_err) out.push(v.reload_err)
  for (const n of v.notes ?? []) out.push(n)
  if (v.servers.length > 0 && !v.plugin_loaded) {
    out.push('MCP 插件未在运行(配置已保存但没生效):检查启动命令是否正确,或重启 gah')
  }
  return out
}

// searchSummary 检索模式的一句话总览(空查询得到的工具总数)。
export function searchSummary(v: McpView): string {
  const searchable = v.servers.filter((s) => s.mode === MCP_MODE_SEARCH)
  if (searchable.length === 0) return ''
  const n = searchable.reduce((acc, s) => acc + s.tools, 0)
  return `检索模式共 ${n} 个工具可被 mcp_search 查到(不进固定上下文)`
}

function quoteIfNeeded(part: string): string {
  return /[\s"]/.test(part) ? '"' + part.replace(/"/g, '\\"') + '"' : part
}
