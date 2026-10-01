// 与 web/ 后端 JSON 契约(冻结 v1;改动需同步增/改字段并兼容旧帧)。
// 契约来源:web/server.go 的 SSE 帧与 REST 响应。

// —— SSE 帧 ——
// 与 web/events.go FrameXxx 常量一一对应(新增帧类型必须两处同步;
// 前端 transport 订阅表同样需要同步,否则新帧静默丢弃)。
export type FrameType =
  | 'session'
  | 'status'
  | 'error'
  | 'confirm'
  | 'command'
  | 'question'
  | 'doc'
  | 'questiondone'
  | 'confirmdone'
  | 'schedule'
  | 'diff'
  | 'baseline'
  | 'notice'
  | 'steer_dropped'

export interface Frame {
  id: number
  type: FrameType
  ts?: number
  // session 会话帧的会话 id(空 = 主会话;非会话帧无此字段)。
  // 多窗口/多会话并行后,前端据此把事件归到正确的会话(每条连接只收自己会话的帧)。
  session?: string
  payload: unknown
  replay?: boolean
}

// 用户提示(NOND-N1;FrameNotice payload / GET /api/notices?since=<id> 同载荷)。
// 与 Go 侧 sdk.Notice 字段名逐字对应(id/level/title/body/source/ts/key);**不进会话记录**,
// 因此刷新后的历史只能靠 /api/notices 回填,前端按 id 单调去重(实时帧与回填可能重叠)。
export interface Notice {
  id: number
  level: 'info' | 'warn' | 'error'
  title: string
  body?: string
  source?: string
  ts: string
  key?: string // 服务端去重键(客户端不使用,只随帧透传)
}

// 提示回填页(GET /api/notices):gap=true 表示 since 之后有提示已被服务端环形缓冲丢弃,
// 本次回填不完整 —— 前端须如实标注,不得谎报完整。
// suppressed = 进程启动以来被服务端按 Key 去重的条数(同一种错误 60s 内只提示一次)。
export interface NoticePage {
  items: Notice[]
  max_id: number
  gap?: boolean
  suppressed?: number
}

// 首帧基线(FrameBaseline payload;S-P1-2 长会话):首连时服务端只回放**尾部窗口**,
// 本帧描述窗口边界(From/To/HasMore)。前端据此显示「上滚加载更早」。
export interface Baseline {
  from: number
  to: number
  count: number
  has_more: boolean
  window: number
}

// 会话事件分页响应(GET /api/session/events;上滚加载更早历史)。
export interface SessionEventsPage {
  events: SessionEvent[]
  from: number
  to: number
  count: number
  has_more: boolean
}

// 会话事件(JSON 序列化后的 sdk.SessionEvent;Kind/Seq/Payload/TS 字段名保持 Go 原样)
export interface SessionEvent {
  Kind: string
  Seq: number
  Payload: unknown
  TS: string
}

// 结构化提问弹层载荷(FrameQuestion payload;P3 语义交互)
export interface QuestionRequest {
  id: string
  prompt: string
  options?: { value: string; desc?: string }[]
  multiple?: boolean
  free_text?: boolean
}

// —— 各 Kind 载荷 ——
// 附件(随 UserMessage 会话 JSON 序列化:Go Attachment 字段原样)
export interface AttachmentInfo {
  Kind: string // image | file
  Name: string
  MimeType: string
  Rel: string // 相对附件根(/attachments/<Rel> 预览)
  Path?: string // 运行时绝对路径(会话重放无)
}
export interface UserMessage {
  Content: string
  Attachments?: AttachmentInfo[]
}
// 上传响应视图(POST /api/attachments)
export interface AttachmentView {
  name: string
  path: string
  url: string
  size: number
}
export interface AssistantMessage {
  Content: string
  ToolCalls: ToolCall[]
}
export interface LLMStreamDelta {
  Delta?: string
  ToolCallID?: string
  ToolCallName?: string
  ToolCallArgs?: string
  Done?: boolean
}
export interface ToolCall {
  ID: string
  Name: string
  Arguments: string
}
export interface ToolResult {
  CallID: string
  Name: string
  Content: string
  Error: string
}
export interface UsageEvent {
  Model: string
  Usage: Usage
}
export interface Usage {
  PromptTokens: number
  CompletionTokens: number
  CachedTokens: number
  OutputTokens: number
}

// — 状态栏快照(/api/state) —
export interface UsageStats {
  PromptTokens: number
  CompletionTokens: number
  CachedTokens: number
  Requests: number
  // LastPromptTokens 最近一次请求的实测输入 token(= 当前上下文占用;累计量不能当水位)
  LastPromptTokens: number
  Window: number
}
export interface SessionView {
  id: string
  name: string
  path: string
  key: string
}
export interface StateView {
  // model/thinking 是**生效值**(角色声明优先):与真正发出去的请求同源,
  // 不是会话档原值(那两个是 model_session/thinking_session)
  model: string
  thinking: string
  model_from?: string // role|session(旧后端省略 = 无角色概念,按 session 处理)
  thinking_from?: string
  model_session?: string
  thinking_session?: string
  sandbox: string
  // 档位联动(approval 为权威档时覆盖沙箱):仅当有效档 != 声明档时后端下发;
  // 展示"实际生效档"用 sandbox_effective ?? sandbox(旧后端无此字段时语义不变)
  sandbox_effective?: string
  sandbox_derived?: boolean
  // sandbox_from 偏离**来源**(role|approval,第九十二批):角色收紧与审批联动走同一条
  // "有效档 != 声明档"通道,但处置完全不同(换角色 vs 改审批档)—— 由后端下发,不靠猜。
  sandbox_from?: string
  // 审批档→沙箱有效档 的联动开关(R10 ②-2;后端未实现 sdk.SandboxSync 时省略 = 不显示该项)
  sandbox_sync?: boolean
  approval?: string
  // approval_effective/approval_from 角色**收紧后**的有效审批档与来源(第九十二批):
  // 只报声明档会把"危险命令已被直接拒"显示成"会弹确认框"。
  approval_effective?: string
  approval_from?: string
  // A-5#125 数据根可写性(后端 GAH_HOME 未注入时省略):false → 页面出只读提示条
  data_root?: string
  data_root_writable?: boolean
  stats: UsageStats
  session?: SessionView
  // 当前角色(第七十九批;未装配 ctx.roles / 未启用角色时省略 → 不显示徽标)
  role?: string
  role_name?: string
  running: boolean
  // running_sessions 当前有回合在跑的**会话 id** 列表(第一百零二批;多窗口并行时用)。
  // 本窗口的会话可能不在其中(别的窗口在跑)—— 主会话也在列表里(有自己的 id)。
  running_sessions?: string[]
  version: string
}

// 命令列表项(/api/commands DTO)
export interface CommandView {
  name: string
  usage: string
  desc: string
}

// 命令参数级(逐级确认;/api/commands/{name}/options)
export interface CommandOption {
  value: string
  desc: string
}
export interface CommandOptionsResp {
  level: number
  // 空枚举/空自由参数可能为 null(后端已归零为 [],这里兼容旧响应)
  items: CommandOption[] | null
  freeArgs: string[] | null
  done: boolean
}

// 会话列表项(/api/sessions)
export interface SessionInfo {
  ID: string
  Path: string
  Name: string
  Preview: string // 会话内容省略版(首条用户消息截断;空 = 无内容)
  MTime: number
  Frames: number
  // F 组 F0/F2/F3(omitempty:旧后端不返回则 undefined)
  Pinned?: boolean
  PinnedAt?: number
  Summary?: string
  SummaryTopics?: string[]
  SummaryCoveredFrames?: number
  SummaryState?: string // ready | stale | missing | unavailable
}

// 工作区(项目)历史项(/api/workspaces)
export interface WorkspaceInfo {
  key: string
  dir: string
  ts: number
}

// 审批弹层载荷(confirm 帧)
export interface ConfirmRequest {
  id: string
  prompt: string
}

// 全局二次确认请求(askConfirm):danger = 删除类(按钮标红);确认后执行 run
// (异步可接受——run 返回 Promise 由调用方自行 await/处理错误)。
export interface AskConfirm {
  title: string
  danger?: boolean
  run: () => void
}

// —— 设置面板数据(契约 /api/models /api/providers /api/plugins) ——
export interface ModelInfo {
  ID: string
  OwnedBy?: string
}
export interface ProviderModelGroup {
  Name: string
  Models: ModelInfo[]
  // W3:该端点拉模型失败时的原因(Go 侧 error → 字符串;成功时字段缺省)。
  // 首启自检靠它给 401/404/DNS 人话提示(见 providers.ts explainProbeError)。
  Err?: string
}
// /api/models?all=1 多 provider 聚合
export interface ModelsAllResp {
  providers: ProviderModelGroup[]
}
// ProviderProfile 无 json tag:序列化字段为 Go 原样大写(与 PluginInfo 同规则)。
export interface ProviderInfo {
  Name: string
  BaseURL: string
  APIKey: string
  Model: string
  Active: boolean
}
export interface PluginInfo {
  ID: string
  Type: string
  Bundle: string
  State: string // loaded | configured
  // 管理域(web 设置面板):host=宿主运行可卸载 | external=已外部化勿启停 | scenario=场景专用勿启 | web=常规可启用
  // 单一事实源=Go 侧 catalogue 声明(PluginInfo.Manage),server 透传;新增外部化/场景插件仅需在 catalogue 声明
  manage: string
}

// 命令结果帧
export interface CommandResult {
  raw: string
  output: string
  error: string
}

// —— 后台任务(/api/jobs 契约,host-jobs DTO) ——
export interface Job {
  id: string
  state: string // running | done | failed | killed
  command?: string
  output?: string
  result?: unknown
  error?: string
  created_at: string
  done_at?: string
}

// —— 定时计划(/api/schedules 契约,sdk.Schedule 原样 NOND-W4) ——
export interface Schedule {
  id: string
  name: string
  cron: string // 5 字段:分 时 日 月 周
  prompt: string
  enabled: boolean
  created_at: string
  last_run_at?: string
  last_status?: string // ok | failed | skipped
  last_error?: string
  next_run?: string // 宿主机算;零值(未启用/无匹配时刻)序列化为零时刻字符串
}

// —— 文档预览(D1;/api/doc/* 契约,sdk.DocView 原样) ——
export type DocBlockKind =
  | 'heading'
  | 'paragraph'
  | 'list'
  | 'quote'
  | 'code'
  | 'table'
  | 'image'
  | 'divider'
  | 'page'
  | 'sheet'
  | 'slide'
  | 'note'
  | 'unsupported'
export interface DocRun {
  text: string
  bold?: boolean
  italic?: boolean
  strike?: boolean
  code?: boolean
  link?: string
}
export interface DocCell {
  text: string
  colSpan?: number
  rowSpan?: number
  align?: string
  numeric?: boolean
}
export interface DocAsset {
  id: string
  mime?: string
  name?: string
  w?: number
  h?: number
  bytes?: number
}
export interface DocBlock {
  kind: DocBlockKind
  level?: number
  text?: string
  runs?: DocRun[]
  head?: string[]
  rows?: DocCell[][]
  lang?: string
  asset?: DocAsset
  page?: number
  meta?: Record<string, string>
}
export interface DocSheet {
  name: string
  rows: number
  cols: number
  hidden?: boolean
}
export interface DocView {
  path?: string
  name?: string
  format?: string
  size?: number
  modTime?: string
  title?: string
  author?: string
  blocks?: DocBlock[]
  sheets?: DocSheet[]
  pages?: number
  kind?: string
  rawUrl?: string
  truncated?: string[]
  warnings?: string[]
  meta?: Record<string, string>
}
export interface DocEntry {
  name: string
  path: string
  dir: boolean
  size?: number
  modTime?: string
  format?: string
  previewable?: boolean
}
export interface DocTree {
  path?: string
  name?: string
  entries: DocEntry[]
  truncated?: string[]
  warnings?: string[]
}



// —— MCP server 配置(NOND-M1 第 2/3 步) ——
export interface McpServer {
  name: string
  command: string
  args?: string[]
  enabled: boolean
  // mode: direct(工具全量注册)/ search(只暴露 mcp_search + mcp_call)
  mode: string
  // source: file(配置文件,可编辑)/ env(GAH_MCP_COMMAND(S),只读)
  source?: string
  // 运行期状态:是否检测到该 server 已连接(工具已注册进索引/注册表)
  loaded: boolean
  tools: number
}
export interface McpView {
  path: string
  servers: McpServer[]
  reload_available: boolean
  plugin_loaded: boolean
  reload_err?: string
  notes?: string[]
  search_total?: number
}

// —— 角色(第七十九批 1b;sdk.RoleSpec 原样 + 派生字段) ——
export interface RoleSpec {
  id: string
  name: string
  description?: string
  identity?: string
  // exclude_global:true = 不注入全局 AGENTS.md(非开发角色不想背满屏编码规范)
  exclude_global?: boolean
  // 技能挂载:skills_set 区分"没写这个键"(= 默认池全给)与"写了 []"(= 一个都不挂)
  skills?: string[]
  skills_set: boolean
  skills_inherit?: boolean
  // 角色携带的模型/思考档(第八十六批;空 = 跟随会话)。生效值见 StateView.model_from。
  model?: string
  thinking?: string
  // approval/sandbox 角色**收紧**档(第九十二批;空 = 跟随全局)。值域只有更严的那几个:
  // 审批 smart|strict、沙箱 read-only|workspace-write —— open/full-access 属放宽,后端 400。
  approval?: string
  sandbox?: string
  // tools_exclude 角色**排除**的工具名(第九十一批;空/未写 = 不排除任何工具)。
  // 方向与 skills 相反:工具默认全给,这里是减项 —— 新装插件对老角色依然可见。
  tools_exclude?: string[]
  agents?: string
  agents_bytes: number
  own_skills?: string[] // 角色私有技能(roles/<id>/skills/,只增不减)
  effective_skills?: string[] // 切换后实际可见(服务端派生,只读展示)
  seed?: boolean // 来自预置 seed(同样可改可删)
  // group 场景分组(纯展示;空 = 不分组,面板收在「其它」一节)
  group?: string
}
// ToolDef 工具定义(POST/GET /api/tools 的子集:面板只展示名字与一句话描述)。
export interface ToolDef {
  name: string
  description?: string
}
export interface SkillInfo {
  name: string
  description?: string
  triggers?: string[]
  role?: string // 归属角色(空 = 共享技能库)
}
// InstructionsView 全局指令($GAH_HOME/AGENTS.md)只读视图(第八十一批)。
// over = 手改超限的文件:能读能看,但面板保存会被拒(不静默截断用户的话)。
export interface InstructionsView {
  path: string
  text: string
  bytes: number
  exists: boolean
  max_bytes: number
  over: boolean
}
export interface RolesView {
  current: string
  max_agents_bytes: number
  roles: RoleSpec[]
  library?: SkillInfo[]
  problems?: { id: string; error: string }[]
}
// TrashEntry 回收站里的一份角色/技能(第八十三批)。name = 回收站目录名(<名>-<时间戳>),
// 恢复时按它定位;id/skill 为空 = 目录名不合约定(面板照实显示但恢复会被拒)。
export interface TrashRoleEntry {
  name: string
  id: string
  deleted_at: string
}
export interface TrashSkillEntry {
  name: string
  skill: string
  role: string // 空 = 共享技能库
  deleted_at: string
}
export interface TrashView {
  roles: TrashRoleEntry[]
  skills: TrashSkillEntry[]
}
// SkillPackResult 导入技能包的回执(第一百零六批)。replaced=true 时覆盖了同名旧技能;
// from 是包里的原名(用「导入为」改名时与 name 不同)。
export interface SkillPackResult {
  name: string
  path: string
  bytes: number
  replaced: boolean
  from: string
  warning?: string
}

// RolePackResult 导入角色包的回执(第九十三批)。replaced=true 时 backup_name 是那份被覆盖的旧角色
// 在 roles/.trash/ 里的条目名(可恢复);manifest 是包里的清单(原本的 ID/显示名/导出时间)。
export interface RolePackResult {
  id: string
  name?: string
  agents_bytes: number
  skills: string[]
  replaced: boolean
  backup_name?: string
  manifest: { format: string; version: number; id: string; name?: string; exported_at: string; gah_version?: string }
}
