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

export interface Frame {
  id: number
  type: FrameType
  ts?: number
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
  Window: number
}
export interface SessionView {
  id: string
  name: string
  path: string
  key: string
}
export interface StateView {
  model: string
  thinking: string
  sandbox: string
  // 档位联动(approval 为权威档时覆盖沙箱):仅当有效档 != 声明档时后端下发;
  // 展示"实际生效档"用 sandbox_effective ?? sandbox(旧后端无此字段时语义不变)
  sandbox_effective?: string
  sandbox_derived?: boolean
  // 审批档→沙箱有效档 的联动开关(R10 ②-2;后端未实现 sdk.SandboxSync 时省略 = 不显示该项)
  sandbox_sync?: boolean
  approval?: string
  stats: UsageStats
  session?: SessionView
  running: boolean
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
  items: CommandOption[]
  freeArgs: string[]
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
