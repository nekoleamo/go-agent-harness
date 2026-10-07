// 工具定义与执行流水线(对齐设计 §5.5:工具定义 MCP 兼容 JSON Schema)。
package sdk

import "context"

// ToolDefinition MCP 兼容工具定义:name/description/inputSchema(JSON Schema)。
// TimeoutMs 工具级执行超时(P0-2,host-bridge 覆写全局 3s 桥超时);0 = 使用默认。
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any // JSON Schema 对象
	TimeoutMs   int64          `json:"timeout_ms,omitempty"` // 毫秒;0 = 桥默认

	// PathParams 路径参数**能力声明**:本工具哪些参数承载路径、读写意图如何。
	// 宿主 pre-execute 的路径沙箱按此裁决 —— **声明优先**。
	// 未声明时宿主不再放行(fail-open):按 schema 参数名与工具名推断(见 sdk.InferPathParams),
	// 仍未命中则做值级兜底(参数值一眼是路径就裁决),并在注册期提示一次。
	// 因此插件的**建议**是显式声明(推断只是兼容后手);确实没有任何路径参数的工具用
	// PathParamsDeclared 显式声明,以免被推断误伤。
	// 只在宿主与插件协议间流转,不下发模型(适配层仅取 Name/Description/InputSchema)。
	PathParams []PathParam `json:"PathParams,omitempty"`

	// PathParamsDeclared 显式声明位:作者确认**本工具没有任何承载路径的参数**。
	// 与 PathParams 的空值区分:JSON 里二者都是"空",omitempty 无从分辨 —— 故单列一个布尔。
	// true 时宿主跳过推断与值级兜底(见 sdk.InferPathParams 与 policy-guard CheckToolCallAt)。
	PathParamsDeclared bool `json:"path_params_declared,omitempty"`

	// ProxyArgsParam 代理工具(见 ApprovalTargetParam)的「内层参数对象」字段名。
	// 如 search 模式的 mcp_call 声明 "arguments":宿主按**真实目标工具**的声明/推断裁决内层参数 ——
	// 否则 `mcp_call{name:"mcp_srv_write",arguments:{path:"…"}}` 的内层路径不经任何裁决
	// (审批维度已按真实名匹配,路径维度此前没有,属同一类间接绕过)。
	ProxyArgsParam string `json:"proxy_args_param,omitempty"`

	// ApprovalTargetParam 可选**能力声明**:本工具是「代理工具」—— 一次调用代表对**另一个
	// 工具**的调用(如 MCP 检索模式的 mcp_call:name 参数承载真实工具名)。
	// 声明后宿主审批把 approval_tools 里针对**真实工具名**的规则作用到本次调用
	// (否则逐工具规则会被一个间接名整体绕过),确认弹层也显示「代理工具 → 真实工具」。
	// 只在宿主与插件协议间流转,不下发模型。
	ApprovalTargetParam string `json:"ApprovalTargetParam,omitempty"`
}

// PathAccess 工具对某个路径参数的访问意图(能力声明用)。
type PathAccess string

const (
	PathRead  PathAccess = "read"  // 读:read-only/workspace-write 下限 workspace ∪ $GAH_HOME;凭据类一律拒
	PathWrite PathAccess = "write" // 写:read-only 全拒;workspace-write 限 workspace 内
)

// PathParam 一个承载路径的参数声明(顶层 JSON 字段名 + 访问意图)。
type PathParam struct {
	Arg      string     `json:"arg"`                // JSON 参数名
	Access   PathAccess `json:"access"`             // read / write
	Many     bool       `json:"many,omitempty"`     // 值为字符串数组:逐元素裁决
	Optional bool       `json:"optional,omitempty"` // 缺省即跳过(如“默认当前工作区根”)
	// Nested 嵌套取值路径(2026-09-27 F2 收敛):非空时按路径段取值,`*` = 数组展开,
	// 如 ["files","*","path"] = files 数组每个元素的 path(未声明工具的深层路径因此不再漂白)。
	// Arg 仍记顶层字段名(日志/文案用);值不是字符串的命中点直接跳过(不报错)。
	Nested []string `json:"nested,omitempty"`
}

// Tool 一个可执行工具。Execute 的 args 是 JSON 字符串(模型生成)。
// 返回 any 将序列化为工具结果(可附结构化视图,见 M3)。
type Tool interface {
	Definition() ToolDefinition
	Execute(ctx context.Context, args string) (any, error)
}

// ToolResult 工具执行的结果载体(结构化错误回传模型,见设计 §11)。
type ToolResult struct {
	Content string // 序列化结果(JSON/文本)
	Error   string // 错误时非空;不中断 turn,原样回传模型
}

// ToolConflict 注册期同名工具冲突(B3,2026-09-27 审计观察项)。
//
// 背景:注册是 first-wins(先到者胜)—— 后到的同名工具被**忽略**,只留一行日志。
// 后果是“插件启用成功但工具不存在”这种状态在界面上看不见,排查只能翻日志;
// 若后到者是修复版插件,还会静默沿用旧实现。这里把“被忽略者”记下来供可见性面使用。
type ToolConflict struct {
	Name    string `json:"name"`    // 冲突的工具名(已被先注册者占用)
	Ignored string `json:"ignored"` // 被忽略者的描述(定位是哪个插件——插件清单暂不带提供者字段)
}

// ToolConflictReporter 可选能力:注册期冲突的可见性(host-tools 实现)。
//
// 为何不扩 ToolRegistry 接口:实现方只需**自愿**实现,消费者按类型断言读取,避免为了
// 一个诊断面把所有实现方+测试替身都撑大。
type ToolConflictReporter interface {
	ToolConflicts() []ToolConflict
}

// ToolCatalogue 可选能力:角色工具集过滤(host-tools 实现,第九十一批)。
//
// 为何不扩 ToolRegistry 接口:与 ToolConflictReporter 同款 —— 只让 host-tools **自愿**实现,
// 测试替身/极简宿主不受影响(未实现 = 不做工具过滤,即今天行为;不是静默降级)。
//
// 与技能可见性(host-skills 的 SetFilter)**分工明确**:技能是“有没有这份知识”,
// 工具是“能不能动手” —— 同一个角色表达两件事,分别落在两个 Registry 上。
// 过滤必须同时作用于 List 与 Execute:只过 List 会让模型凭记忆调用绕过
// (工具名在历史上下文里出现过)。
//
// 豁免:Get **不**过过滤 —— 它是裁决面(policy-guard 靠它取真实目标工具定义做
// 路径/审批裁决)与插件自查面,过滤它会让被排除的工具连裁决都拿不到定义。
// 管理面需“注册了什么”而非“模型看得见什么”时用 ListAll。
//
// 单一判据:判定函数用 sdk.ToolVisible(role, name),运行期与展示端共用。
// 未装配 ctx.tools 的宿主:host-roles 侧跳过(显式不报错)。
type ToolCatalogue interface {
	// SetFilter 安装可见性判定函数(nil = 不过滤,恢复全量);返回 Disposer 幂等撤销。
	SetFilter(visible func(ToolDefinition) bool) Disposer
	// ListAll 全部已注册工具(不过滤);未注册工具时返回空切片。
	ListAll() []ToolDefinition
}

// ContextualToolCatalogue 可选扩展(ctx.tools 实现者):按**调用所属会话**过滤可见工具。
//
// 为什么需要(第一百一十六批):SetFilter 的判定是无参的,而 List() 在组装请求时调用 ——
// 那时 ctx 里带着会话 id。多页签各用各的角色时,角色 A 排除的工具不该在页签 B 的
// 工具列表里(那等于让 A 的收窄失去意义)。
type ContextualToolCatalogue interface {
	// SetContextFilter 安装按会话的可见性判定(nil = 回落 SetFilter 的判定)。
	SetContextFilter(visible func(ctx context.Context, def ToolDefinition) bool) Disposer
	// ListFor 该会话下模型可见的工具定义。
	ListFor(ctx context.Context) []ToolDefinition
}

// ToolRegistry 服务(ctx.tools):注册/列举/带流水线执行。
type ToolRegistry interface {
	// Register 注册工具,返回 Disposer。
	Register(t Tool) Disposer
	// List 返回模型可见的工具定义列表。
	List() []ToolDefinition
	// Get 取回单个工具定义。
	Get(name string) (ToolDefinition, bool)
	// Execute 经全流水线执行一个工具:
	//   tools/pre-execute(waterfall,veto 阻止)
	//   tools/execute(waterfall,默认实现为实际调用)
	//   tools/post-execute(waterfall)
	//   tool/result(emit 广播结果)
	Execute(ctx context.Context, name, args string) (*ToolResult, error)
}
