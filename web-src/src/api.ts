// REST 客户端(上行)+ 工具函数。核心交互纯 REST,不绕模板渲染。
import type { AttachmentView, AuditEntry, CommandOptionsResp, InstallView, CommandView, DocTree, DocView, InstructionsView, Job, McpView, MemoryView, ModelsAllResp, NoticePage, PluginInfo, ProviderInfo, RolePackResult, RoleSpec, RolesView, SkillPackResult, Schedule, SessionEventsPage, SessionInfo, StateView, ToolDef, TrashView, WorkspaceInfo } from './types'

// 本窗口绑定的会话(多窗口/多会话作用域)。为何是本文件自己持有而不是 import 一个
// session 模块:本文件被 Node 内置测试以「./api.ts」直接加载(那条路要求 import 带 .ts),
// 而浏览器侧类型检查禁止应用代码写 .ts 后缀 —— 两者只能取交集 = 这里**零运行时相对 import**。
// 绑定入口是 api.bindSession(id),由 main.ts 在启动时从 URL ?session= 注入。
let boundSessionId = ''

// sessionQS 会话作用域的 query 串(含前导 ?;未绑定为空串,便于直接拼 URL)。
function sessionQS(extra?: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams()
  if (boundSessionId) q.set('session', boundSessionId)
  for (const [k, val] of Object.entries(extra || {})) {
    if (val !== undefined && val !== '') q.set(k, String(val))
  }
  const s = q.toString()
  return s ? '?' + s : ''
}

const BASE = ''
const json = {
  'Content-Type': 'application/json',
}

// bindSession 绑定本窗口的会话 id(空 = 当前主会话)。main.ts 启动时从 URL 调一次。
function bindSession(id: string): void {
  boundSessionId = id || ''
}

// errText 把失败响应体读成人话:后端两种契约并存 —— JSON {error}(新写入口,如 /api/models 的 501)
// 与纯文本(http.Error 的存量)。直接透传 JSON 原文会把 {"error":"…"} 这种开发者形状甩给用户。
function errText(body: string): string {
  const s = body.trim()
  if (!s) return ''
  if (s.startsWith('{')) {
    try {
      const v = JSON.parse(s) as { error?: unknown }
      if (typeof v.error === 'string' && v.error) return v.error
    } catch {
      /* 不是合法 JSON:按纯文本处理 */
    }
  }
  return s
}

// snippet 压成单行短片段(成功码却不是 JSON 时,得说清"拿到的到底是什么")
function snippet(s: string, max = 120): string {
  const one = s.replace(/\s+/g, ' ').trim()
  return one.length <= max ? one : one.slice(0, max) + '…'
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(BASE + path, init)
  const body = await resp.text()
  if (!resp.ok) {
    throw new Error(`HTTP ${resp.status}: ${errText(body) || resp.statusText || '请求失败'}`)
  }
  // 204 / 空体:契约上就是"无返回"(调用方按 void 用)
  if (!body) return undefined as T
  try {
    return JSON.parse(body) as T
  } catch {
    // 成功码却不是 JSON(代理/中间层的错误页、静态兜底返回 HTML):说清是什么形状
    // —— 别抛 "Unexpected token '<'…",那句对用户与排查都没有信息量。
    throw new Error(`HTTP ${resp.status}: 响应不是 JSON(${snippet(body) || '空体'})`)
  }
}

export const api = {
  // bindSession 绑定本窗口会话(多窗口各看各的);空 = 当前主会话。启动时由 main.ts 调一次。
  bindSession,
  state(): Promise<StateView> {
    return req('/api/state' + sessionQS())
  },
  // S-P1-2 会话事件分页:上滚加载更早历史(before = 已加载的最老事件 Seq;0 = 尾部窗口)
  sessionEvents(before: number, limit: number): Promise<SessionEventsPage> {
    return req('/api/session/events' + sessionQS({ before, limit }))
  },
  commands(): Promise<CommandView[]> {
    return req('/api/commands')
  },
  // 命令参数级(逐级确认;与 TUI 选择器同一注册表声明):picked 不含命令名
  commandOptions(name: string, picked: string[]): Promise<CommandOptionsResp> {
    return req('/api/commands/' + encodeURIComponent(name) + '/options', {
      method: 'POST',
      headers: json,
      body: JSON.stringify({ picked }),
    })
  },
  sessions(): Promise<SessionInfo[]> {
    return req('/api/sessions')
  },
  // F 组 F2:置顶/取消置顶(经 action 分派,不新增端点)
  sessionPin(id: string, pinned: boolean): Promise<void> {
    return req('/api/sessions', {
      method: 'POST',
      headers: json,
      body: JSON.stringify({ action: pinned ? 'pin' : 'unpin', id }),
    })
  },
  // F 组 F3:生成/取回会话概述(会调用模型;force = 忽略缓存)
  sessionSummary(id: string, force = false): Promise<{ summary: { text: string; topics?: string[] }; auto: boolean }> {
    return req('/api/sessions/summary', {
      method: 'POST',
      headers: json,
      body: JSON.stringify({ id, force }),
    })
  },
  workspaces(): Promise<WorkspaceInfo[]> {
    return req('/api/workspaces')
  },
  // 提交输入(回合)或斜杠命令;回合运行中普通消息**注入当前回合**(转向,响应 accepted=steer),
  // 无法注入时后端 409 拒绝;attachments=附件本地路径(/api/attachments 返回)
  // input 提交回合。回执 accepted:'turn' = 开了新回合;'steer' = 回合进行中,
  // 消息已注入当前回合(转向,模型下一次请求可见)。
  input(content: string, attachments?: string[]): Promise<{ ok?: boolean; accepted?: string }> {
    return req('/api/input', {
      method: 'POST',
      headers: json,
      body: JSON.stringify({ content, attachments: attachments ?? [], session: boundSessionId }),
    })
  },
  // 附件上传(multipart;返回落盘视图;后端大小/类型/数量白名单)。
  // 线上响应是 {ok, attachments:[视图]}(见 web/server.go handleAttachments),**不是单个视图**
  // —— 直接当视图用会读到 undefined 的 url,提交时 JSON.stringify 把 undefined 变 null,
  // 服务端解成空串,于是提交被拒:「附件不可用: (空路径)」(2026-09-16 Windows 真机)。
  // 拆包只放在这一处:调用方不必知道包装层。
  async upload(file: File): Promise<AttachmentView> {
    const fd = new FormData()
    fd.append('file', file)
    const r = await req<{ attachments?: AttachmentView[] }>('/api/attachments', { method: 'POST', body: fd })
    const v = r?.attachments?.[0]
    // 缺 url 必须显式失败:否则又会退化成「提交时才发现」的隐性错误
    if (!v || !v.url) throw new Error('附件上传响应异常(缺少 url)')
    return v
  },
  // 状态栏级控制(模型/思考/沙箱/审批/工作区;M17 审批档位 open|smart|strict)
  // cancel 中止运行中的回合(经 ctx.turnControl)
  control(body: {
    model?: string
    thinking?: string
    sandbox?: string
    approval?: string
    sandbox_sync?: boolean
    workspace?: string
    cancel?: boolean
    // session 取消作用域:非空 = 只停该会话的回合(需后端支持 SessionRunner);
    // 空 = 停全部(TUI 语义)。
    session?: string
  }): Promise<void> {
    return req('/api/control', { method: 'POST', headers: json, body: JSON.stringify(body) })
  },

  // —— 整体备份(M18) ——
  // 列表项 = sdk.BackupInfo(无 json tag → 序列化为 Go 原样大写,与 ProviderInfo/PluginInfo 同规则)。
  // 曾按小写读取 → 面板行渲染成「最近备份: · NaN KB」(第二十二批验收逮到并修)。
  backups(): Promise<{ Name: string; Size: number; Time: number }[]> {
    return req('/api/backup')
  },
  backupNow(dest?: string): Promise<{ ok: true; name: string }> {
    return req('/api/backup', { method: 'POST', headers: json, body: JSON.stringify({ action: 'backup', dest: dest ?? '' }) })
  },
  backupRestore(name: string): Promise<{ ok: true; restored: string }> {
    return req('/api/backup', { method: 'POST', headers: json, body: JSON.stringify({ action: 'restore', name }) })
  },
  confirm(id: string, ok: boolean): Promise<void> {
    return req('/api/confirm', { method: 'POST', headers: json, body: JSON.stringify({ id, ok, session: boundSessionId }) })
  },
  // 结构化提问作答(P3;values=选项值,text=自由文本,二者可并存)
  questionAnswer(id: string, values: string[], text: string): Promise<void> {
    return req('/api/question', { method: 'POST', headers: json, body: JSON.stringify({ id, values, text }) })
  },
  sessionSwitch(id: string): Promise<void> {
    return req('/api/sessions', { method: 'POST', headers: json, body: JSON.stringify({ action: 'switch', id }) })
  },
  sessionNew(): Promise<{ id: string }> {
    return req('/api/sessions', { method: 'POST', headers: json, body: JSON.stringify({ action: 'new' }) })
  },
  // 删除会话记录(仅删该会话 jsonl 与名字索引,不动任何目录/文件夹)
  sessionDelete(id: string): Promise<{ session?: unknown }> {
    return req('/api/sessions', { method: 'POST', headers: json, body: JSON.stringify({ action: 'delete', id }) })
  },
  // 会话改名(仅作用于当前会话;改名即切换目标会话)
  sessionRename(name: string): Promise<void> {
    return req('/api/sessions/rename', { method: 'POST', headers: json, body: JSON.stringify({ name }) })
  },
  // 删除工作区记录(仅移除记录,不删除对应文件夹)
  workspaceForget(key: string): Promise<void> {
    return req('/api/workspaces/' + encodeURIComponent(key), { method: 'DELETE' })
  },
  // —— 设置面板 ——
  models(): Promise<ModelsAllResp> {
    return req('/api/models?all=1')
  },
  providers(): Promise<ProviderInfo[]> {
    return req('/api/providers')
  },
  providerAdd(p: { name: string; base_url: string; api_key: string; model?: string }): Promise<void> {
    return req('/api/providers', { method: 'POST', headers: json, body: JSON.stringify(p) })
  },
  providerUse(name: string): Promise<void> {
    return req('/api/providers/' + encodeURIComponent(name) + '/use', { method: 'POST', headers: json, body: '{}' })
  },
  providerDelete(name: string): Promise<void> {
    return req('/api/providers/' + encodeURIComponent(name), { method: 'DELETE' })
  },
  plugins(): Promise<PluginInfo[]> {
    return req('/api/plugins')
  },
  pluginToggle(id: string, on: boolean): Promise<void> {
    return req('/api/plugins/' + encodeURIComponent(id) + (on ? '/load' : '/unload'), { method: 'POST', headers: json, body: '{}' })
  },
  // —— 插件安装 / 卸载 / 信任(2026-10-03)——
  // 安装走两段:preview 先取确认文案所需事实(与服务端 ConfirmPrompt 同一份),
  // confirmed=true 才真装 —— 服务端没有确认服务,不能替用户点确认。
  pluginInstallList(): Promise<InstallView[]> {
    return req('/api/plugins/install')
  },
  pluginInstallPreview(spec: string): Promise<{ prompt: string }> {
    return req('/api/plugins/install', { method: 'POST', headers: json, body: JSON.stringify({ spec, preview: true }) })
  },
  pluginInstall(spec: string): Promise<{ ok: boolean; id: string; dir: string; audit: AuditEntry; hint: string }> {
    return req('/api/plugins/install', { method: 'POST', headers: json, body: JSON.stringify({ spec, confirmed: true }) })
  },
  pluginUninstall(id: string): Promise<{ ok: boolean }> {
    return req('/api/plugins/uninstall', { method: 'POST', headers: json, body: JSON.stringify({ id, confirmed: true }) })
  },
  pluginTrust(id: string): Promise<{ ok: boolean }> {
    return req('/api/plugins/trust', { method: 'POST', headers: json, body: JSON.stringify({ id, confirmed: true }) })
  },
  pluginUntrust(id: string): Promise<{ ok: boolean }> {
    return req('/api/plugins/untrust', { method: 'POST', headers: json, body: JSON.stringify({ id, confirmed: true }) })
  },
  compact(prompt?: string): Promise<{ summary: string; folded: number }> {
    return req('/api/compact', { method: 'POST', headers: json, body: JSON.stringify({ prompt: prompt ?? '' }) })
  },
  settingsHistory(n: number): Promise<{ history: number }> {
    return req('/api/settings/history', { method: 'POST', headers: json, body: JSON.stringify({ n }) })
  },
  reload(): Promise<void> {
    return req('/api/reload', { method: 'POST', headers: json, body: '{}' })
  },
  // —— 角色(第七十九批 1b;未装配 ctx.roles → 503,面板据此隐藏「角色」段) ——
  roles(): Promise<RolesView> {
    return req('/api/roles')
  },
  // instructions 全局指令读($GAH_HOME/AGENTS.md;缺文件 = 空 + exists:false,不是错误)
  instructions(): Promise<InstructionsView> {
    return req('/api/instructions')
  },
  // instructionsSave 覆盖写全局指令(PUT 后服务端会触发指令重载;warning 非空 = 文件已写但没生效)
  instructionsSave(text: string): Promise<{ ok: true; bytes: number; applied: boolean; warning?: string }> {
    return req('/api/instructions', { method: 'PUT', headers: json, body: JSON.stringify({ text }) })
  },
  // memory 跨会话记忆读(治理面板;未装配 host-memory → 503,面板整段隐藏)
  memory(): Promise<MemoryView> {
    return req('/api/memory')
  },
  // memoryAct 四个动作:add / remove(按展示序号) / remove_source(按来源会话整段删) / toggle。
  // 响应就是刷新后的视图(+ deleted),面板不必再拉一次。
  memoryAct(
    body:
      | { action: 'add'; content: string }
      | { action: 'remove'; index: number }
      | { action: 'remove_source'; source: string }
      | { action: 'toggle'; enabled: boolean }
      // 候选池(M2 前置件):propose 提候选(不进上下文),accept/reject 转正或丢弃。
      | { action: 'propose'; content: string }
      | { action: 'accept'; index: number }
      | { action: 'reject'; index: number }
      | { action: 'accept_all' }
      | { action: 'reject_all' },
  ): Promise<MemoryView> {
    return req('/api/memory', { method: 'POST', headers: json, body: JSON.stringify(body) })
  },
  // tools 工具清单(GET /api/tools);all=true = **全量**(管理面:被角色排除的也要列出来,
  // 否则面板分不清"被排除"与"没装这个插件");默认 = 模型可见(已过滤)。
  tools(all = false): Promise<ToolDef[]> {
    return req('/api/tools' + (all ? '?all=1' : ''))
  },
  // roleGet 详情(含 AGENTS.md 正文;列表接口不带正文,避免面板轮询拖大响应)
  roleGet(id: string): Promise<RoleSpec> {
    return req('/api/roles/' + encodeURIComponent(id))
  },
  roleCreate(p: { id: string; name?: string; description?: string; identity?: string; exclude_global?: boolean }): Promise<RoleSpec> {
    return req('/api/roles', { method: 'POST', headers: json, body: JSON.stringify(p) })
  },
  // roleUpdate 部分更新(只改传了的字段;未传 = 保持现值;model/thinking 传空串 = 清掉)
  roleUpdate(id: string, p: Partial<{ name: string; description: string; identity: string; exclude_global: boolean; skills_set: boolean; skills: string[]; skills_inherit: boolean; model: string; thinking: string; tools_exclude: string[]; approval: string; sandbox: string }>): Promise<RoleSpec> {
    return req('/api/roles/' + encodeURIComponent(id), { method: 'PATCH', headers: json, body: JSON.stringify(p) })
  },
  roleSetAgents(id: string, agents: string): Promise<{ ok: true; bytes: number }> {
    return req('/api/roles/' + encodeURIComponent(id) + '/agents', { method: 'PUT', headers: json, body: JSON.stringify({ agents }) })
  },
  roleRename(id: string, p: { id?: string; name?: string }): Promise<RoleSpec> {
    return req('/api/roles/' + encodeURIComponent(id) + '/rename', { method: 'POST', headers: json, body: JSON.stringify(p) })
  },
  // roleUse 切换(id 空 = 停用回基线角色);下一回合生效,**不换会话**
  roleUse(id: string): Promise<{ ok: true; current: string }> {
    return req('/api/roles/' + encodeURIComponent(id || '-') + '/use', { method: 'POST', headers: json, body: '{}' })
  },
  roleDelete(id: string): Promise<void> {
    return req('/api/roles/' + encodeURIComponent(id), { method: 'DELETE' })
  },
  // —— 角色包(第九十三批):单文件导出/导入 ——
  // rolePackImport 上传角色包(multipart "file")。overwrite 缺省时同名目标后端**显式拒绝**(400),
  // 由调用方问过用户后再带 overwrite=1 重试;as 空 = 用包里的原始 ID。
  // skillPackImport 上传技能包(multipart "file")。overwrite 缺省时同名目标后端**显式拒绝**(400),
  // 导入为(As)会同步改写正文 frontmatter 的 name —— skills.Write 显式校验两者一致。
  async skillPackImport(file: File, p: { as?: string; overwrite?: boolean } = {}): Promise<SkillPackResult> {
    const fd = new FormData()
    fd.append('file', file, file.name)
    const q = new URLSearchParams()
    if (p.as) q.set('as', p.as)
    if (p.overwrite) q.set('overwrite', '1')
    const qs = q.toString()
    return req('/api/skillpack' + (qs ? '?' + qs : ''), { method: 'POST', body: fd })
  },

  async rolePackImport(file: File, p: { as?: string; overwrite?: boolean } = {}): Promise<RolePackResult> {
    const fd = new FormData()
    fd.append('file', file, file.name)
    const q = new URLSearchParams()
    if (p.as) q.set('as', p.as)
    if (p.overwrite) q.set('overwrite', '1')
    const qs = q.toString()
    return req('/api/rolepack' + (qs ? '?' + qs : ''), { method: 'POST', body: fd })
  },
  // commandRun 直接跑一个斜杠命令(POST /api/commands/{name} {args})。
  // 桌面壳专用:WebView 没有下载通道(`<a download>` 点了什么都不会发生),
  // 导出改由服务端写文件 —— 与 TUI 的 /role export 是同一实现,界面只负责挑目录。
  commandRun(name: string, args: string[]): Promise<{ output?: string; error?: string }> {
    return req('/api/commands/' + encodeURIComponent(name), {
      method: 'POST',
      headers: json,
      body: JSON.stringify({ args, session: boundSessionId }),
    })
  },
  // 新建**独立**会话且不动当前会话(多窗口用:POST action=spawn)。
  // 与 sessionNew() 的区别 = 后者会把当前会话切走(本窗口历史当场换掉)。
  sessionSpawn(): Promise<{ id: string }> {
    return req('/api/sessions', { method: 'POST', headers: json, body: JSON.stringify({ action: 'spawn' }) })
  },
  // 技能:role 空 = 共享库;传 content(原文)走原样写入,否则由服务端按表单拼 frontmatter
  skillCreate(p: { role?: string; name: string; description?: string; triggers?: string[]; body?: string; content?: string; overwrite?: boolean }): Promise<{ name: string; path: string; warning?: string }> {
    return req('/api/skills', { method: 'POST', headers: json, body: JSON.stringify(p) })
  },
  skillGet(name: string, role = ''): Promise<{ name: string; content: string; path: string }> {
    return req('/api/skills/' + encodeURIComponent(name) + (role ? '?role=' + encodeURIComponent(role) : ''))
  },
  skillDelete(name: string, role = ''): Promise<void> {
    return req('/api/skills/' + encodeURIComponent(name) + (role ? '?role=' + encodeURIComponent(role) : ''), { method: 'DELETE' })
  },
  // skillRelocate 技能改名 / 跨库移动(第八十四批):to_name 空 = 只换库,to_role 空 = 只改名。
  // 后端改了目录名后会**同步改所有挂载它的角色**(返回 mounts_updated),所以这动作不可从面板一键回退。
  skillRelocate(
    name: string,
    p: { role?: string; to_name?: string; to_role?: string },
  ): Promise<{ ok: true; name: string; role: string; from_name: string; from_role: string; mounts_updated?: string[]; warning?: string }> {
    const q = p.role ? '?role=' + encodeURIComponent(p.role) : ''
    return req('/api/skills/' + encodeURIComponent(name) + '/relocate' + q, {
      method: 'POST',
      headers: json,
      body: JSON.stringify({ to_name: p.to_name ?? '', to_role: p.to_role ?? '' }),
    })
  },
  // 回收站(第八十三批):删除的角色/技能移进 .trash 后可在这里列出并恢复
  trash(): Promise<TrashView> {
    return req('/api/trash')
  },
  // trashRestore:name 是**回收站目录名**(<名>-<时间戳>),不是原 ID/技能名
  trashRestore(p: { kind: 'role' | 'skill'; name: string; role?: string }): Promise<{ ok: true; warning?: string; id?: string; name?: string }> {
    return req('/api/trash/restore', { method: 'POST', headers: json, body: JSON.stringify(p) })
  },
  // —— 文档预览(D1;/api/doc/*;未装配 503,前端据首次探测隐藏入口) ——
  docPreview(path: string, opts?: { page?: number; sheet?: number; max?: number }): Promise<DocView> {
    const q = new URLSearchParams({ path })
    if (opts?.page) q.set('page', String(opts.page))
    if (opts?.sheet) q.set('sheet', String(opts.sheet))
    if (opts?.max) q.set('max', String(opts.max))
    return req('/api/doc/preview?' + q.toString())
  },
  docTree(path: string, depth = 2): Promise<DocTree> {
    const q = new URLSearchParams({ path, depth: String(depth) })
    return req('/api/doc/tree?' + q.toString())
  },
  docRender(text: string): Promise<DocView> {
    return req('/api/doc/render', { method: 'POST', headers: json, body: JSON.stringify({ text }) })
  },
  // —— 后台任务 ——
  jobs(): Promise<Job[]> {
    return req('/api/jobs')
  },
  jobKill(id: string): Promise<void> {
    return req('/api/jobs/' + encodeURIComponent(id) + '/kill', { method: 'POST', headers: json, body: '{}' })
  },
  // NOND-N1 提示回填:提示不进会话记录 → 页面加载/重连后靠这个端点补上错过的那几条
  // (since=0 = 取服务端环形缓冲内的全部;返回值带 max_id 与 gap,前端按 id 去重)。
  notices(since = 0): Promise<NoticePage> {
    return req('/api/notices?since=' + encodeURIComponent(String(since)))
  },
  // —— 定时计划(NOND-W4) ——
  schedules(): Promise<Schedule[]> {
    return req('/api/schedules')
  },
  scheduleAdd(p: { name: string; cron: string; prompt: string; enabled?: boolean }): Promise<Schedule> {
    return req('/api/schedules', { method: 'POST', headers: json, body: JSON.stringify(p) })
  },
  // 仅覆盖传入字段(未传 = 不改;enabled 走 undefined 区分 false)
  scheduleUpdate(id: string, p: { name?: string; cron?: string; prompt?: string; enabled?: boolean }): Promise<Schedule> {
    return req('/api/schedules/' + encodeURIComponent(id), { method: 'PATCH', headers: json, body: JSON.stringify(p) })
  },
  scheduleDelete(id: string): Promise<void> {
    return req('/api/schedules/' + encodeURIComponent(id), { method: 'DELETE', headers: json, body: '{}' })
  },
  scheduleRun(id: string): Promise<void> {
    return req('/api/schedules/' + encodeURIComponent(id) + '/run', { method: 'POST', headers: json, body: '{}' })
  },
  // —— MCP server 配置(NOND-M1 第 2/3 步;保存 = 写 mcp.yaml + 重启 tool-mcp 插件) ——
  mcp(): Promise<McpView> {
    return req('/api/mcp')
  },
  mcpSave(servers: McpSaveServer[], reload = true): Promise<McpView> {
    return req('/api/mcp', { method: 'POST', headers: json, body: JSON.stringify({ servers, reload }) })
  },
}

// McpSaveServer 保存用的最小字段(command 为整行启动命令,后端按引号规则拆参数)。
export interface McpSaveServer {
  name: string
  command: string
  args?: string[]
  enabled: boolean
  mode: string
}

// 工具结果摘要:截断长文本(对齐 TUI 折叠语义)
export function summarize(text: string, max = 600): string {
  if (text.length <= max) return text
  return text.slice(0, max) + '\n…'
}

// skillPackDownloadUrl 技能包下载地址(浏览器 <a download> 直接吃;桌面壳走 commandRun)。
// 独立前缀 /api/skillpack —— 不挂在 /api/skills/{name} 下(技能名里就有 import/export)。
export function skillPackDownloadUrl(name: string): string {
  return '/api/skillpack/' + encodeURIComponent(name)
}

// skillPackName 建议文件名(与后端 skillpack.FileName 同款:gah-skill-<名>.zip)。
export function skillPackName(name: string): string {
  return 'gah-skill-' + name + '.zip'
}

// rolePackDownloadUrl 角色包下载地址(浏览器 <a download> 直接吃;桌面壳走 commandRun)。
export function rolePackDownloadUrl(id: string): string {
  return '/api/rolepack/' + encodeURIComponent(id)
}

// rolePackName 角色包建议文件名(与后端 Content-Disposition / 命令默认名同口径)。
export function rolePackName(id: string): string {
  return 'gah-role-' + id + '.zip'
}

// sessionExportUrl 会话导出地址(浏览器下载 href)。format 缺省 jsonl(后端缺省同此)。
// id 空 = 主会话:走无 id 路由(/api/sessions/export)——空段的 /api/sessions//export
// 不匹配任何路由(404),主会话导出曾因此静默失败。
export function sessionExportUrl(id: string, format: 'html' | 'jsonl' = 'jsonl'): string {
  const q = format === 'html' ? '?format=html' : ''
  if (!id) return '/api/sessions/export' + q
  return '/api/sessions/' + encodeURIComponent(id) + '/export' + q
}

// sessionExportName 导出的建议文件名(与后端 Content-Disposition 同名)。
export function sessionExportName(id: string, format: 'html' | 'jsonl'): string {
  return 'session-' + (id || 'main') + '.' + format
}
