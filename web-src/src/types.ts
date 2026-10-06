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
  // Thinking 思维增量(reasoning_content/thinking,与 Delta 互斥)。
  // 服务端一直有这个字段,Web 侧此前**整个丢弃** —— 于是推理模型的思考过程
  // 一条都不显示,用户看到的是一段没有铺垫的结论(2026-10-03 用户反馈:
  // 「思考过程和最终输出结果展示区分度不够」)。
  Thinking?: string
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
// ModelVerdict 由**后端算好下发**(Go 侧 sdk.AssessModel 是单一事实源)。
//
// 为什么前端不自己判「这个模型能不能当 agent 用」:那是一条会让用户按它选错模型的结论
// (没有工具调用能力的模型当不了 agent 的脑子)。前端算一份就会与 TUI 选择器的结论漂移,
// 而两端都在同一个界面体系里 —— 用户从 Web 选、在 TUI 里看到另一套说法,没法排查。
export interface ModelVerdict {
  Free: boolean
  /** id = openrouter/free(官方自动路由到某个免费模型;不会随免费清单变动而失效) */
  AutoRouter?: boolean
  ToolsKnown: boolean
  Tools: boolean
  Vision: boolean
  ContextWindow: number
  Usable: boolean
  Tags: string[]
  Warn: string
}
export interface ModelInfo {
  ID: string
  OwnedBy?: string
  // 端点自述的可选元信息(不在就别显示;字段多时纯属向后兼容的补充)
  ContextWindow?: number
  SupportsTools?: boolean | null
  InputModalities?: string[]
  MaxOutputTokens?: number
  /** 端点给的输入价(单位=每 token 美元;见 Go 侧 ModelInfo.PromptPrice 的注释) */
  PromptPrice?: number
  PriceKnown?: boolean
  // 后端算好的判定 + 厂商提示(端点不返回 owned_by 时由 id 前缀补)
  Verdict?: ModelVerdict
  Vendor?: string
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
  /** provider 级自定义请求头(网关按头路由/限流时才需要;值里的 ${session} 等占位符未展开) */
  Headers?: Record<string, string>
}
export interface PluginInfo {
  ID: string
  Type: string
  Bundle: string
  State: string // loaded | configured
  // 管理域(web 设置面板):host=宿主运行可卸载 | external=已外部化勿启停 | scenario=场景专用勿启 | web=常规可启用
  // 单一事实源=Go 侧 catalogue 声明(PluginInfo.Manage),server 透传;新增外部化/场景插件仅需在 catalogue 声明
  manage: string
  // rejected 非空 = 这条是**被拒绝加载**的外部插件(哈希白名单不符/清单坏了等),
  // State 会是 "rejected"。带原因是为了让「没装」与「被拦」在界面上可区分 ——
  // 被拦的表现是工具整组消失,没有原因就只剩一个查不到出处的空缺。
  rejected?: string
}

// 一次白名单登记的审计行(非安全边界:能改插件目录的人也能改它,见 internal/plugintrust)。
export interface AuditEntry {
  Time: string
  Source: string
  Name: string
  Hash: string
}

// 已安装插件 + 信任状态 + 最近登记来源(GET /api/plugins/install)。
export interface InstallView {
  id: string
  protocol: string
  binary: string
  dir: string
  trusted: boolean // 白名单里有它、且哈希与盘上那份一致
  enforced: boolean // 白名单是否存在(= 是否强制)
  hash?: string
  audit_time?: string // RFC3339
  audit_source?: string // embed | install:<spec> | trust:manual
  loadable: boolean // 二进制在不在(不在 = 装了但没构建成功)
  // —— 批一:装机来源(仓库/ref/commit) ——
  // 与 audit_source 分开:审计行答「谁把它登记进白名单」,来源账答「它当初是从哪份代码装的」。
  source_repo?: string // 归一化后的 host/path
  source_ref?: string // tag/branch 名;默认分支为空
  source_kind?: string // tag | branch | default | commit | local
  source_commit?: string // 短显示(12 位)
  drifted?: boolean // 移动引用换过 sha(不是风险,但你应该知道)
  origin?: string // user | official
  prebuilt?: string // 产物是下载来的(URL);非空 = 没在本机构建
  api_version?: string // 装机时记下的协议版本
  compat_ok: boolean // 声明版本是否在本版 gah 支持范围内
  // —— 批二:生命周期三态 ——
  disabled?: boolean // 被用户停用(文件还在,只是不加载)
  loaded?: boolean // 当前是否有进程在跑
}

// 检查更新(GET /api/plugins/update-check;批一 §1.5)。
// 一个已安装的 UI 插件在设置面板上的视图事实(GET /api/ui-plugins/state)。
export interface UIPluginView {
  id: string
  version?: string
  dir?: string
  slots: number
  /** 是否已登记进完整性闸。批四起**默认强制**:没登记的不会被加载。 */
  trusted: boolean
  disabled: boolean
  /** 被闸拦下的原因(空 = 没被拦)。 */
  reject?: string
}

export interface UIPluginState {
  /** 盘上装了哪些(不论是否登记/是否停用)。 */
  installed: UIPluginView[]
  disabled: string[]
  /** 完整性闸是否强制。 */
  enforced: boolean
  note: string
}

export interface UpdateCheck {
  plugin_id: string
  repo: string
  ref?: string
  kind?: string
  current?: string
  remote?: string
  // no_update | moved | tag_changed | unreachable
  status: string
  message: string
}

export interface UpdateCheckResp {
  checks: UpdateCheck[]
  notice?: string // 本地兼容性提示汇总(空 = 无)
  hint: string
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
  cron: string // 5 字段:分 时 日 月 周(日字段可含 L = 当月最后一天)
  prompt: string
  enabled: boolean
  created_at: string
  once?: boolean // 只跑一次(触发后自动停用)
  once_date?: string // 一次性目标日期 YYYY-MM-DD(once 时的权威,cron 表达不了年)
  // 农历年度排期(如 "08-15";"12-00" = 腊月最后一天即除夕)。
  // 设了它时 cron 只承载时分 —— 农历日期在公历上每年都在变,cron 表达不了。
  lunar_date?: string
  last_run_at?: string
  last_status?: string // ok | failed | skipped
  last_error?: string
  next_run?: string // 宿主机算;零值(未启用/无匹配时刻)序列化为零时刻字符串
  // 中文排期描述与预览时刻:不会 cron 的用户没有别的验收手段 ——
  // 界面显示的是这两个,不是 cron。
  cron_label?: string // 空串 = 控件表达不了(repeat=custom),界面按只读展示
  next_runs?: string[] // 接下来 1-3 次(once 只 1 次)
}

// 排期档位(与后端 hostschedule.RepeatKind 同名同值;custom = 控件表达不了)。
export type RepeatKind =
  | 'daily'
  | 'weekly'
  | 'weekdays'
  | 'monthly_day'
  | 'monthly_nth'
  | 'monthly_last'
  | 'monthly_last_workday'
  | 'hourly'
  | 'every_n_min'
  | 'every_n_hour'
  | 'annual_date' // 每年某个公历日期(cron 可表达)
  | 'lunar_annual' // 每年某个农历日期(中秋/春节;cron 表达不了,靠 lunar_date)
  | 'once'
  | 'custom'

// ScheduleView 排期解释结果(sdk.ScheduleView 原样):控件回填 + 人话描述 + 预览。
export interface ScheduleView {
  cron: string
  label: string // 中文排期描述(custom 时为空串)
  next_runs: string[] // 接下来 1-3 次触发时刻
  repeat: RepeatKind
  minute?: number // 时刻(分)
  hour?: number // 时刻(时)
  dows?: number[] // weekly 选中的周几(0=周日)
  month?: number // annual_date / lunar_annual 的月
  day?: number // monthly_day 几号;annual/lunar 的日(0 = 该月最后一天)
  festival?: string // 节日名(中秋/春节…),仅用于文案
  nth?: number // monthly_nth 第几个(1-5)
  every?: number // every_n_min/every_n_hour 的间隔数
  once?: boolean
  once_date?: string
  assumed_time?: boolean // 时刻是补的默认值(用户没说)→ 界面必须回显告知
}

// ScheduleResolve resolve 响应。ok=false **不是**故障:那是「这句话没看懂」,
// 界面要说的是「没看懂,可以直接用下面的选择器」,不是红字报错。
export interface ScheduleResolve extends Partial<ScheduleView> {
  ok: boolean
  reason?: string
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

// MemoryView 跨会话记忆(记忆治理面板;与 `/memory` 命令同一份实现)。
// user/project = 展示行(新的在前,每行带序号与来源标注)。
// project 缺省 = 该实现不提供项目级只读列表(前端不渲染那一组,而不是显示空的骗人)。
export interface MemoryView {
  enabled: boolean
  budget: number
  user: string[]
  project?: string[]
  user_path: string
  project_path?: string
  project_key?: string
  /** 写动作回执:删了几条(remove 恒为 1;remove_source 可能是 0 = 没有来自该会话的记忆) */
  deleted?: number
  /**
   * 候选池(记忆层 M2 前置件)。**候选永不进上下文**,只有 accept 之后才进。
   * 整个字段组缺席 = 该构建没有候选能力(老版本),前端不渲染这一块。
   */
  candidates?: string[]
  candidate_path?: string
  candidate_used?: number
  candidate_limit?: number
  candidate_today?: number
  candidate_today_max?: number
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
