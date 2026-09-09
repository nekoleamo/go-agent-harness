// 与 web/ 后端 JSON 契约(冻结 v1;改动需同步增/改字段并兼容旧帧)。
// 契约来源:web/server.go 的 SSE 帧与 REST 响应。

// —— SSE 帧 ——
export type FrameType = 'session' | 'status' | 'error' | 'confirm' | 'command'

export interface Frame {
  id: number
  type: FrameType
  ts?: number
  payload: unknown
  replay?: boolean
}

// 会话事件(JSON 序列化后的 sdk.SessionEvent;Kind/Seq/Payload/TS 字段名保持 Go 原样)
export interface SessionEvent {
  Kind: string
  Seq: number
  Payload: unknown
  TS: string
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

// 会话列表项(/api/sessions)
export interface SessionInfo {
  ID: string
  Path: string
  Name: string
  Preview: string // 会话内容省略版(首条用户消息截断;空 = 无内容)
  MTime: number
  Frames: number
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
